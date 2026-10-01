#!/usr/bin/env python3
"""Regression for the developer fixture wrapper's output boundary."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class FixtureWrapperTest(unittest.TestCase):
    @unittest.skipUnless(os.name == "posix", "POSIX wrapper regression needs Linux; Windows uses Assert-LocalPath")
    def test_generator_binary_symlink_never_reaches_go_or_overwrites_source(self):
        source_root = Path(__file__).resolve().parent.parent
        # Keep all newly created test paths in the project's ignored test area.
        test_parent = source_root / ".testdata"
        test_parent.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="fixture-wrapper-", dir=test_parent) as temporary:
            parent = Path(temporary)
            project = parent / "project"
            (project / "scripts").mkdir(parents=True)
            (project / ".bin").mkdir()
            binary_dir = project / ".tools/cache/fixture-bin"
            binary_dir.mkdir(parents=True)
            for name in ("gen-fixtures", "toolchain.py"):
                shutil.copyfile(source_root / "scripts" / name, project / "scripts" / name)
            marker = project / "go-was-called"
            go = project / ".bin/go"
            go.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\nexit 0\n', encoding="utf-8")
            go.chmod(0o700)
            original = parent / "original"
            original.write_bytes(b"preserve this original")
            (binary_dir / "gen-fixtures").symlink_to(original)
            result = subprocess.run(["sh", "scripts/gen-fixtures"], cwd=project,
                                    capture_output=True, timeout=10, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())
            self.assertEqual(original.read_bytes(), b"preserve this original")


if __name__ == "__main__":
    unittest.main(verbosity=2)
