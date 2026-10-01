#!/usr/bin/env python3
import io
from pathlib import Path
import tarfile
import shutil
import uuid
import unittest
from unittest.mock import patch
import sys
sys.dont_write_bytecode = True
import toolchain
from toolchain import ROOT, assert_hash, local_path, safe_extract


class BootstrapSecurityTest(unittest.TestCase):
    def setUp(self):
        (ROOT / ".tools").mkdir(exist_ok=True)
        self.root = ROOT / ".tools" / ("bootstrap-tests-" + uuid.uuid4().hex)
        self.root.mkdir()

    def tearDown(self):
        shutil.rmtree(self.root)

    def archive(self, name, kind=tarfile.REGTYPE):
        path = self.root / "sample.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            item = tarfile.TarInfo(name)
            item.type = kind
            item.linkname = "../../outside"
            item.size = 4 if kind == tarfile.REGTYPE else 0
            archive.addfile(item, io.BytesIO(b"safe") if item.size else None)
        return path

    def test_valid_archive(self):
        out = self.root / "out"
        out.mkdir()
        safe_extract(self.archive("go/bin/go"), out)
        self.assertEqual((out / "go/bin/go").read_bytes(), b"safe")

    def test_unsafe_entries(self):
        for name, kind in [("../escape", tarfile.REGTYPE), ("/escape", tarfile.REGTYPE),
                           ("C:/escape", tarfile.REGTYPE), ("go/./bad", tarfile.REGTYPE),
                           ("go/link", tarfile.SYMTYPE), ("go/hardlink", tarfile.LNKTYPE),
                           ("go/device", tarfile.CHRTYPE)]:
            with self.subTest(name=name, kind=kind):
                out = self.root / "out"
                out.mkdir(exist_ok=True)
                with self.assertRaises(ValueError):
                    safe_extract(self.archive(name, kind), out)

    def test_wrong_hash(self):
        with self.assertRaises(ValueError):
            assert_hash(self.archive("go/bin/go"), "0" * 64)

    def test_bootstrap_removes_bad_cache(self):
        spec = {"url": "https://example.invalid/go-test.tar.gz", "sha256": "0" * 64,
                "installPath": "go/1.27.1/linux-amd64", "executable": "go/bin/go"}
        archive = self.root / ".tools/downloads/go-test.tar.gz"
        archive.parent.mkdir(parents=True)
        archive.write_bytes(b"untrusted archive bytes")
        with patch.object(toolchain, "ROOT", self.root), patch.object(toolchain, "selected", return_value=({"version": "1.27.1"}, spec, "linux-amd64")):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                toolchain.bootstrap(offline=True)
        self.assertFalse(archive.exists(), "failed bootstrap must remove corrupt cache")

    def test_path_escape(self):
        with self.assertRaises(ValueError):
            local_path(self.root, self.root.parent / "outside")
        with self.assertRaises(ValueError):
            local_path(self.root, self.root / "../outside")

    def test_linked_parent(self):
        link = self.root / "linked"
        try:
            link.symlink_to(self.root.parent, target_is_directory=True)
        except OSError as error:
            self.skipTest("Host cannot create test symlinks: " + str(error))
        with self.assertRaises(ValueError):
            local_path(self.root, link / "outside")


if __name__ == "__main__":
    unittest.main(verbosity=2)
