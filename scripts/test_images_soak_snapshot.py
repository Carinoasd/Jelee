"""Snapshot boundaries, immutable committed contents, and formal-run gate."""
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import images_soak_snapshot as snapshot
import start_images_soak as launcher


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.addCleanup(self.cleanup)

    def cleanup(self):
        for directory, dirs, files in os.walk(self.root):
            Path(directory).chmod(0o700)
            for name in files:
                path = Path(directory) / name
                if not path.is_symlink():
                    path.chmod(0o600)
        self.temporary.cleanup()

    def archive(self, name="scripts/test.py", kind=tarfile.REGTYPE):
        path = self.root / "source.tar"
        with tarfile.open(path, "w") as stream:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.mode = 0o755
            member.linkname = "../outside"
            member.size = 4 if kind == tarfile.REGTYPE else 0
            stream.addfile(member, io.BytesIO(b"test"))
        return path

    def test_traversal_links_devices_and_reserved_paths_rejected(self):
        for index, (name, kind) in enumerate((("../outside", tarfile.REGTYPE), ("/outside", tarfile.REGTYPE),
                (".git/config", tarfile.REGTYPE), (".tools/injected", tarfile.REGTYPE),
                ("link", tarfile.SYMTYPE), ("hard", tarfile.LNKTYPE), ("device", tarfile.CHRTYPE))):
            with self.subTest(name=name):
                destination = self.root / str(index)
                destination.mkdir()
                with self.assertRaises(snapshot.SnapshotFailure):
                    snapshot.extract(self.archive(name, kind), destination)

    @unittest.skipUnless(sys.platform == "linux", "native POSIX sealing")
    def test_valid_archive_is_sealed_and_changed_source_rejected(self):
        destination = self.root / "source"
        destination.mkdir()
        records = snapshot.extract(self.archive(), destination)
        snapshot.verify(destination, records)
        self.assertEqual(records["scripts/test.py"]["sha256"], hashlib.sha256(b"test").hexdigest())
        target = destination / "scripts/test.py"
        self.assertEqual(target.stat().st_mode & 0o777, 0o555)
        target.chmod(0o755)
        target.write_bytes(b"edit")
        target.chmod(0o555)
        with self.assertRaisesRegex(snapshot.SnapshotFailure, "changed"):
            snapshot.verify(destination, records)

    @unittest.skipUnless(sys.platform == "linux", "native POSIX sealing and Git")
    def test_prepare_uses_commit_even_when_worktree_changes(self):
        workspace = self.root / "repo"
        workspace.mkdir()
        def git(*args):
            subprocess.run(["git", "-C", str(workspace), *args], stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, check=True)
        git("init")
        (workspace / "file").write_bytes(b"committed")
        git("add", "file")
        git("-c", "user.name=Carinoasd", "-c", "user.email=46304809+Carinoasd@users.noreply.github.com",
            "commit", "-m", "snapshot fixture")
        (workspace / "file").write_bytes(b"mutable worktree")
        owned = self.root / "owned"
        owned.mkdir()
        with patch.object(snapshot, "provision"):
            record = snapshot.prepare(workspace, owned)
        self.assertEqual((owned / "source/file").read_bytes(), b"committed")
        self.assertEqual(len(record["commit"]), 40)
        snapshot.verify(owned / "source", record["files"])

    def test_copy_requires_pinned_bytes_and_cleanup_requires_owned_native_path(self):
        source, target = self.root / "input", self.root / "output"
        source.write_bytes(b"wrong")
        with self.assertRaisesRegex(snapshot.SnapshotFailure, "dependency"):
            snapshot.copy_verified(source, target, "0" * 64)
        with self.assertRaisesRegex(snapshot.SnapshotFailure, "cleanup"):
            snapshot.remove_snapshot(self.root)
        self.assertTrue(self.root.exists())

    def test_formal_requires_matching_successful_smoke_with_all_cleanup(self):
        directory = self.root / "soak-launch-123"
        directory.mkdir()
        path = directory / "result.json"
        good = {"scope": "smoke", "sourceCommit": "a" * 40, "result": "passed", "soakWorkloadPassed": True,
                "snapshotVerified": True, "launcherArtifactsCleaned": True, "testArtifactsCleaned": True}
        path.write_text(json.dumps(good))
        self.assertTrue(launcher.smoke_passed(self.root, "a" * 40))
        self.assertFalse(launcher.smoke_passed(self.root, "b" * 40))
        for key in good:
            value = dict(good)
            value.pop(key)
            path.write_text(json.dumps(value))
            self.assertFalse(launcher.smoke_passed(self.root, "a" * 40), key)


if __name__ == "__main__":
    unittest.main()
