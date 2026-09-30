#!/usr/bin/env python3
"""Offline synthetic archives exercise media bootstrap without downloading tools."""
import copy
import fcntl
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import unittest
import uuid
from unittest.mock import patch

sys.dont_write_bytecode = True
import toolchain

module_spec = importlib.util.spec_from_file_location("media_tools", Path(__file__).with_name("media-tools.py"))
media = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(media)


def sha(data):
    return hashlib.sha256(data).hexdigest()


class MediaSecurityTest(unittest.TestCase):
    def setUp(self):
        self.root = toolchain.ROOT / ".tools" / ("media-tests-" + uuid.uuid4().hex)
        self.root.mkdir(parents=True)
        self.patches = [patch.object(media, "ROOT", self.root), patch.object(toolchain, "ROOT", self.root)]
        for item in self.patches:
            item.start()
        self.spec = {
            "url": "https://example.invalid/media.tar.xz", "sha256": "0" * 64,
            "sizeBytes": 1, "archive": "tar.xz", "archiveRoot": "media-test",
            "vendorVersion": "test-1", "installPath": "media/linux-amd64/test-1", "executables": {},
            "licenseFiles": [{"path": "media-test/LICENSE", "sha256": sha(b"test license")}],
        }
        self.files = {"media-test/LICENSE": b"test license"}
        for name in ("ffmpeg", "ffprobe"):
            data = ("#!/bin/sh\nprintf '%s\\n' '" + name + " version test-1 Copyright test'\n").encode()
            self.files["media-test/bin/" + name] = data
            self.spec["executables"][name] = {"path": "media-test/bin/" + name, "sha256": sha(data),
                                                "productionAllowed": name == "ffprobe"}
        self.archive, self.install, self.record = media.paths(self.spec, "linux-amd64")
        self.archive.parent.mkdir(parents=True)
        self.write_archive()
        self.selection = patch.object(media, "selected", return_value=(self.spec, "linux-amd64"))
        self.selection.start()

    def tearDown(self):
        self.selection.stop()
        for item in reversed(self.patches):
            item.stop()
        shutil.rmtree(self.root)

    def write_archive(self, extra=None):
        with tarfile.open(self.archive, "w:xz") as archive:
            for name, data in self.files.items():
                item = tarfile.TarInfo(name)
                item.size, item.mode = len(data), 0o755 if "/bin/" in name else 0o644
                archive.addfile(item, io.BytesIO(data))
            if extra:
                archive.addfile(extra)
        self.spec["sha256"] = sha(self.archive.read_bytes())
        self.spec["sizeBytes"] = self.archive.stat().st_size

    def test_offline_install_verify_and_exact_forwarding(self):
        media.validate_spec(self.spec, "linux-amd64")
        media.bootstrap(True)
        media.verify()
        arguments = ["-v", "error", "-i", "name with 空 格.mkv", "", "-show_entries", "stream=index"]
        with patch.object(media.os, "execve") as execute:
            media.run_tool("ffprobe", arguments)
        command, forwarded, environment = execute.call_args.args
        self.assertEqual(command, str(self.install / self.spec["executables"]["ffprobe"]["path"]))
        self.assertEqual(forwarded, [command] + arguments)
        self.assertNotIn("FFREPORT", environment)
        self.assertTrue(environment["TMP"].startswith(str(self.root)))
        self.assertIn('"$@"', (self.root / ".bin/ffprobe").read_text())

    def test_manifest_rejects_unpinned_or_unsafe_specs(self):
        edits = [("url", "http://example.invalid/a"), ("url", "https://user:pass@example.invalid/a"),
                 ("url", "https://example.invalid/a?secret=1"), ("sha256", "bad"), ("sizeBytes", True),
                 ("sizeBytes", 268435457), ("archive", "zip"), ("installPath", "media/linux-amd64/../escape"),
                 ("archiveRoot", "../escape"), ("vendorVersion", "test\nother")]
        for field, value in edits:
            with self.subTest(field=field, value=value):
                bad = copy.deepcopy(self.spec)
                bad[field] = value
                with self.assertRaises(ValueError):
                    media.validate_spec(bad, "linux-amd64")
        for name in ("ffmpeg", "ffprobe"):
            bad = copy.deepcopy(self.spec)
            bad["executables"][name]["productionAllowed"] = name == "ffmpeg"
            with self.assertRaises(ValueError):
                media.validate_spec(bad, "linux-amd64")

    def test_offline_missing_does_not_download(self):
        self.archive.unlink()
        with patch.object(media, "build_opener") as network:
            with self.assertRaisesRegex(ValueError, "offline"):
                media.bootstrap(True)
            network.assert_not_called()
        self.assertFalse(self.install.exists())

    def test_bad_cached_size_is_removed(self):
        self.spec["sizeBytes"] += 1
        with self.assertRaisesRegex(ValueError, "size differs"):
            media.bootstrap(True)
        self.assertFalse(self.archive.exists())
        self.assertFalse(self.install.exists())
        self.assertFalse(self.record.exists())

    def test_wrong_archive_fails_before_extraction(self):
        self.archive.write_bytes(b"corrupt")
        with patch.object(toolchain, "safe_extract") as extract:
            with self.assertRaisesRegex(ValueError, "SHA256"):
                media.bootstrap(True)
            extract.assert_not_called()
        self.assertFalse(self.install.exists())
        self.assertFalse(self.archive.exists())

    def test_bad_installed_binary_and_forged_record_cannot_be_blessed(self):
        media.bootstrap(True)
        binary = self.install / self.spec["executables"]["ffprobe"]["path"]
        binary.write_bytes(b"altered")
        record = json.loads(self.record.read_text())
        record["executables"]["ffprobe"]["sha256"] = sha(b"altered")
        self.record.write_text(json.dumps(record))
        with patch.object(media, "check_version") as execute:
            with self.assertRaisesRegex(ValueError, "record"):
                media.run_tool("ffprobe", [])
            with self.assertRaisesRegex(ValueError, "SHA256"):
                media.bootstrap(True)
            execute.assert_not_called()

    def test_missing_license_refuses_execution(self):
        media.bootstrap(True)
        (self.install / "media-test/LICENSE").unlink()
        with patch.object(media, "check_version") as execute:
            with self.assertRaises(OSError):
                media.run_tool("ffprobe", [])
            execute.assert_not_called()

    def test_wrong_version_or_timeout_never_publishes_install(self):
        self.spec["vendorVersion"] = "wrong-2"
        with self.assertRaisesRegex(ValueError, "version"):
            media.bootstrap(True)
        self.assertFalse(self.install.exists())
        self.assertFalse(self.record.exists())
        self.assertFalse(list(self.install.parent.glob("*.staging-*")))
        with patch.object(media, "version_output", side_effect=ValueError("media version process timed out")):
            with self.assertRaisesRegex(ValueError, "timed out"):
                media.bootstrap(True)
        self.assertFalse(self.install.exists())
        self.assertFalse(list(self.install.parent.glob("*.staging-*")))

    def test_live_version_output_flood_timeout_missing_and_environment(self):
        for stream in ("stdout", "stderr"):
            command = [sys.executable, "-c", "import sys,time;sys." + stream + ".write('x'*100000);sys." + stream + ".flush();time.sleep(20)"]
            with self.assertRaisesRegex(ValueError, "output exceeds"):
                media.version_output(command, timeout=2)
        with self.assertRaisesRegex(ValueError, "timed out"):
            media.version_output([sys.executable, "-c", "import time;time.sleep(20)"], timeout=0.1)
        with self.assertRaises(FileNotFoundError):
            media.version_output([str(self.root / "missing")])
        with patch.dict(os.environ, {"LD_PRELOAD": "untrusted", "LD_LIBRARY_PATH": "untrusted", "FFREPORT": "untrusted"}):
            env = media.environment()
        self.assertNotIn("LD_PRELOAD", env)
        self.assertNotIn("LD_LIBRARY_PATH", env)
        self.assertNotIn("FFREPORT", env)

    def test_xz_archive_rejects_traversal_links_special_and_duplicate(self):
        for name, kind in [("../escape", tarfile.REGTYPE), ("/absolute", tarfile.REGTYPE),
                           ("media-test/link", tarfile.SYMTYPE), ("media-test/hard", tarfile.LNKTYPE),
                           ("media-test/fifo", tarfile.FIFOTYPE), ("media-test/LICENSE", tarfile.REGTYPE)]:
            with self.subTest(name=name):
                extra = tarfile.TarInfo(name)
                extra.type, extra.linkname = kind, "../../escape"
                self.write_archive(extra)
                with self.assertRaises((ValueError, OSError)):
                    media.bootstrap(True)
                self.assertFalse(self.install.exists())
                self.assertFalse(self.record.exists())
                self.assertFalse(list(self.install.parent.glob("*.staging-*")))

    def test_linked_install_parent_refused(self):
        self.install.parent.parent.mkdir(parents=True)
        self.install.parent.symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "linked"):
            media.bootstrap(True)

    def test_concurrent_install_is_rejected_and_lock_can_be_reused(self):
        self.record.parent.mkdir(parents=True)
        with self.record.with_suffix(".lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(ValueError, "another"):
                media.bootstrap(True)
        media.bootstrap(True)
        self.assertTrue(self.record.exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
