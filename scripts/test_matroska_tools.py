#!/usr/bin/env python3
"""Offline tests of the optional mkvtoolnix/MediaInfo bootstrap (E4).

No network access and no pinned tool is needed: synthetic SquashFS images
(when mksquashfs exists) and zip archives stand in for the vendor archives.
"""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import uuid
import zipfile
from unittest import mock

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent))
import squashfs_reader  # noqa: E402
import toolchain  # noqa: E402

_spec = importlib.util.spec_from_file_location("matroska_tools", Path(__file__).with_name("matroska-tools.py"))
matroska = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(matroska)


def sha(data):
    return hashlib.sha256(data).hexdigest()


class SquashFSTest(unittest.TestCase):
    def setUp(self):
        if shutil.which("mksquashfs") is None:
            if os.environ.get("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true":
                self.fail("mksquashfs is required for the SquashFS reader tests")
            self.skipTest("mksquashfs not installed; SquashFS reader tests skipped")
        self.dir = Path(tempfile.mkdtemp())
        tree = self.dir / "tree"
        (tree / "usr/bin").mkdir(parents=True)
        (tree / "usr/lib").mkdir(parents=True)
        self.small = b"small file in a fragment\n"
        self.large = os.urandom(300 * 1024) + b"tail"
        (tree / "usr/bin/tool").write_bytes(self.small)
        (tree / "usr/lib/libbig.so.1.2").write_bytes(self.large)
        (tree / "usr/lib/libbig.so.1").symlink_to("libbig.so.1.2")
        (tree / "usr/lib/escape").symlink_to("../../../outside")
        (tree / "usr/lib/absolute").symlink_to("/etc/passwd")
        for index in range(300):
            (tree / "usr/lib" / ("many-%03d" % index)).write_bytes(b"x")

    def tearDown(self):
        shutil.rmtree(self.dir, ignore_errors=True)

    def build(self, compressor):
        image = self.dir / (compressor + ".sqfs")
        result = subprocess.run(["mksquashfs", str(self.dir / "tree"), str(image), "-comp", compressor, "-noappend", "-quiet",
                                 "-all-root", "-no-progress"], capture_output=True)
        if result.returncode != 0:
            self.skipTest("mksquashfs cannot write " + compressor)
        # An AppImage is an ELF runtime followed by the image.
        return b"\x7fELF fake runtime" + bytes(1000) + image.read_bytes(), 1017

    def test_reads_regular_files_through_internal_links(self):
        for compressor in ("gzip", "zstd"):
            data, offset = self.build(compressor)
            image = squashfs_reader.SquashFS(data, offset)
            self.assertEqual(image.read("usr/bin/tool"), self.small)
            self.assertEqual(image.read("usr/lib/libbig.so.1"), self.large)
            listing = image.list("usr/lib")
            self.assertEqual(listing["libbig.so.1"], "symlink")
            self.assertEqual(len([name for name in listing if name.startswith("many-")]), 300)
            self.assertEqual(image.read("usr/lib/many-299"), b"x")

    def test_refuses_escapes_missing_paths_and_bad_images(self):
        data, offset = self.build("gzip")
        image = squashfs_reader.SquashFS(data, offset)
        for path in ("usr/lib/escape", "usr/lib/absolute", "usr/missing", "../usr/bin/tool", "usr/bin", ""):
            with self.assertRaises(squashfs_reader.SquashError, msg=path):
                image.read(path)
        with self.assertRaises(squashfs_reader.SquashError):
            squashfs_reader.SquashFS(data, offset + 1)
        truncated = data[:offset + 200]
        with self.assertRaises(squashfs_reader.SquashError):
            squashfs_reader.SquashFS(truncated, offset).read("usr/bin/tool")


class ManifestTest(unittest.TestCase):
    def test_committed_manifest_is_valid_for_both_platforms(self):
        section, packages = matroska.load()
        for tool in matroska.TOOLS:
            for platform_name in ("linux-amd64", "windows-amd64"):
                entry, spec = matroska.spec_for(section, tool, platform_name)
                self.assertTrue(spec["url"].startswith("https://"))
        self.assertEqual(set(packages), {"libstdc++6", "zlib1g", "libgmp10"})

    def test_rejects_unsafe_specifications(self):
        section, _ = matroska.load()
        cases = {
            "http": lambda s: s["platforms"]["linux-amd64"].__setitem__("url", "http://mkvtoolnix.download/x.AppImage"),
            "query": lambda s: s["platforms"]["linux-amd64"].__setitem__("url", "https://mkvtoolnix.download/x?y=1"),
            "hash": lambda s: s["platforms"]["linux-amd64"].__setitem__("sha256", "0" * 63),
            "editor": lambda s: s["platforms"]["linux-amd64"]["executables"]["mkvpropedit"].__setitem__("productionAllowed", True),
            "missing": lambda s: s["platforms"]["linux-amd64"]["executables"].pop("mkvextract"),
            "install": lambda s: s["platforms"]["linux-amd64"].__setitem__("installPath", "../outside"),
            "traversal": lambda s: s["platforms"]["linux-amd64"]["libraries"][0].__setitem__("path", "usr/../../x"),
            "archive": lambda s: s["platforms"]["linux-amd64"].__setitem__("archive", "exe"),
            "license": lambda s: s["platforms"]["linux-amd64"].__setitem__("licenseTexts", []),
        }
        for name, change in cases.items():
            broken = copy.deepcopy(section)
            change(broken["tools"]["mkvtoolnix"])
            with self.assertRaises((matroska.Rejected, KeyError), msg=name):
                matroska.spec_for(broken, "mkvtoolnix", "linux-amd64")


class ZipInstallTest(unittest.TestCase):
    def setUp(self):
        self.root = toolchain.ROOT / ".tools" / ("matroska-tests-" + uuid.uuid4().hex)
        self.root.mkdir(parents=True)
        self.patches = [mock.patch.object(matroska, "ROOT", self.root)]
        for item in self.patches:
            item.start()
        files = {}
        executables = {}
        for name in ("mkvmerge", "mkvextract", "mkvpropedit"):
            data = ("#!/bin/sh\nprintf '%s\\n' '" + name + " v1.0 test'\n").encode()
            files["kit/" + name] = data
            executables[name] = {"path": "kit/" + name, "sha256": sha(data), "sizeBytes": len(data), "productionAllowed": name != "mkvpropedit"}
        files["kit/COPYING"] = b"license text"
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as bundle:
            for name, data in files.items():
                bundle.writestr(name, data)
            bundle.writestr("kit/unlisted.exe", b"never extracted")
        self.archive = buffer.getvalue()
        self.spec = {"url": "https://mkvtoolnix.download/test-kit.zip", "sha256": sha(self.archive), "sizeBytes": len(self.archive),
                     "archive": "zip", "installPath": "matroska/mkvtoolnix/1.0/linux-amd64", "versionArguments": ["--version"],
                     "versionLine": "{name} v1.0 test", "executables": executables, "libraries": [],
                     "licenseFiles": [{"path": "kit/COPYING", "sha256": sha(b"license text"), "sizeBytes": 12}]}
        self.section = {"tools": {"mkvtoolnix": {"version": "1.0", "platforms": {"linux-amd64": self.spec}}}}
        downloads = self.root / ".tools/downloads"
        downloads.mkdir(parents=True)
        (downloads / "test-kit.zip").write_bytes(self.archive)

    def tearDown(self):
        for item in reversed(self.patches):
            item.stop()
        shutil.rmtree(self.root)

    def test_bootstrap_extracts_only_pinned_members_and_verify_detects_tampering(self):
        if os.name != "posix":
            self.skipTest("shell stand-in executables need POSIX")
        with mock.patch.object(matroska, "print"):
            matroska.bootstrap_tool("mkvtoolnix", "linux-amd64", self.section, {}, True)
        install = self.root / ".tools/matroska/mkvtoolnix/1.0/linux-amd64"
        installed = sorted(p.relative_to(install).as_posix() for p in install.rglob("*") if p.is_file())
        self.assertEqual(installed, ["kit/COPYING", "kit/mkvextract", "kit/mkvmerge", "kit/mkvpropedit"])
        self.assertTrue((self.root / ".bin/mkvmerge").is_file())
        with mock.patch.object(matroska, "print"):
            matroska.verify_tool("mkvtoolnix", "linux-amd64", self.section, {})
        target = install / "kit/COPYING"
        target.chmod(0o644)
        target.write_bytes(b"changed")
        with self.assertRaises(matroska.Rejected):
            matroska.verify_tool("mkvtoolnix", "linux-amd64", self.section, {})
        target.write_bytes(b"license text")
        (install / "kit/extra").write_bytes(b"x")
        with self.assertRaises(matroska.Rejected):
            matroska.verify_tool("mkvtoolnix", "linux-amd64", self.section, {})

    def test_cached_archive_mismatch_is_removed_and_offline_never_downloads(self):
        archive = self.root / ".tools/downloads/test-kit.zip"
        archive.write_bytes(b"tampered archive")
        with self.assertRaises(matroska.Rejected):
            matroska.download(self.spec, True)
        self.assertFalse(archive.exists())
        with self.assertRaises(matroska.Rejected):
            matroska.download(self.spec, True)

    def test_member_size_or_hash_differences_are_refused(self):
        spec = copy.deepcopy(self.spec)
        spec["executables"]["mkvmerge"]["sha256"] = "0" * 64
        with self.assertRaises(matroska.Rejected):
            matroska.read_archive(spec, self.root / ".tools/downloads/test-kit.zip")
        spec = copy.deepcopy(self.spec)
        spec["executables"]["mkvmerge"]["path"] = "kit/absent"
        with self.assertRaises(matroska.Rejected):
            matroska.read_archive(spec, self.root / ".tools/downloads/test-kit.zip")


class ToolchainDelegationTest(unittest.TestCase):
    def test_optional_tools_are_installed_only_when_named(self):
        calls = []
        with mock.patch.object(toolchain, "bootstrap"), mock.patch.object(toolchain, "verify"), \
                mock.patch.object(toolchain.subprocess, "run", side_effect=lambda command, check: calls.append(command)):
            for argv in (["toolchain.py", "bootstrap"], ["toolchain.py", "verify"], ["toolchain.py", "bootstrap", "--tool", "mkvtoolnix", "--offline"],
                         ["toolchain.py", "verify", "--tool", "mediainfo"]):
                with mock.patch.object(sys, "argv", argv):
                    toolchain.main()
        self.assertEqual(len(calls), 3)
        self.assertIn("--if-installed", calls[0])
        self.assertIn("bootstrap", calls[1])
        self.assertIn("--offline", calls[1])
        self.assertEqual(calls[1][-2:], ["--tool", "mkvtoolnix"])
        self.assertEqual(calls[2][-3:], ["verify", "--tool", "mediainfo"])


if __name__ == "__main__":
    unittest.main()
