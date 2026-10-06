#!/usr/bin/env python3
"""Tests for scripts/release.py (G01.3). A stub go executable stands in for
the compiler, so the tests run in milliseconds and need no toolchain."""

import hashlib
import io
import json
import os
import sys
import tarfile
import tempfile
import unittest
import zipfile
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import release  # noqa: E402

CHANGELOG = """# Changelog

## Unreleased

- upcoming work

## [1.2.0] - 2026-10-06

- feature A
- feature B

## 1.1.0

## v1.0.0-rc.1 (2026-09-01)

- candidate
"""

STUB_GO = """#!/usr/bin/env python3
import os, sys
args = sys.argv[1:]
out = args[args.index("-o") + 1]
with open(out, "w") as f:
    f.write("binary " + os.environ["GOOS"] + "/" + os.environ["GOARCH"] + " " + args[args.index("-ldflags") + 1] + " " + args[-1])
os.chmod(out, 0o755)
"""


def make_repo(root: Path, version: str = "1.2.0") -> None:
    (root / "web").mkdir(parents=True)
    (root / "web/package.json").write_text(json.dumps({"name": "@jelee/web", "version": version}), encoding="utf-8")
    (root / "internal/platform/buildinfo").mkdir(parents=True)
    (root / "internal/platform/buildinfo/buildinfo.go").write_text(
        f'package buildinfo\n\nconst DefaultVersion = "{version}"\n', encoding="utf-8")
    (root / "CHANGELOG.md").write_text(CHANGELOG, encoding="utf-8")
    (root / "docs").mkdir()
    for name in release.BUNDLED_FILES:
        (root / name).write_text(name + "\n", encoding="utf-8")
    (root / "CHANGELOG.md").write_text(CHANGELOG, encoding="utf-8")
    go = root / "go-stub"
    go.write_text(STUB_GO, encoding="utf-8")
    go.chmod(0o755)


class TagTest(unittest.TestCase):
    def test_valid_tags(self):
        for tag, version in [("v1.2.3", "1.2.3"), ("v0.1.0", "0.1.0"), ("v1.0.0-rc.1", "1.0.0-rc.1"), ("v2.0.0+build.7", "2.0.0+build.7")]:
            self.assertEqual(release.parse_tag(tag), version)

    def test_invalid_tags(self):
        for tag in ["1.2.3", "v1.2", "v01.2.3", "v1.2.3.4", "v1.2.3-", "vx.y.z", "v10.11.0 ", "upstream-csharp-final"]:
            with self.assertRaises(release.ReleaseError, msg=tag):
                release.parse_tag(tag)


class ChangelogTest(unittest.TestCase):
    def test_section_variants(self):
        self.assertEqual(release.changelog_section(CHANGELOG, "1.2.0"), "- feature A\n- feature B")
        self.assertEqual(release.changelog_section(CHANGELOG, "1.0.0-rc.1"), "- candidate")
        self.assertEqual(release.changelog_section(CHANGELOG, "Unreleased"), "- upcoming work")

    def test_missing_and_empty(self):
        with self.assertRaisesRegex(release.ReleaseError, "no '## \\[9.9.9\\]' section"):
            release.changelog_section(CHANGELOG, "9.9.9")
        with self.assertRaisesRegex(release.ReleaseError, "is empty"):
            release.changelog_section(CHANGELOG, "1.1.0")
        # 1.2 must not match the 1.2.0 heading.
        with self.assertRaises(release.ReleaseError):
            release.changelog_section(CHANGELOG, "1.2")


class VersionTest(unittest.TestCase):
    def test_versions_must_match(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            make_repo(root, "1.2.0")
            release.check_versions(root, "1.2.0")
            with self.assertRaisesRegex(release.ReleaseError, "web/package.json version is 1.2.0.*DefaultVersion is 1.2.0"):
                release.check_versions(root, "1.3.0")

    def test_repository_default_version_is_semver(self):
        version = release.default_version(release.ROOT)
        self.assertRegex(version, release.SEMVER)
        web = json.loads((release.ROOT / "web/package.json").read_text(encoding="utf-8"))["version"]
        self.assertEqual(version, web)


class BuildTest(unittest.TestCase):
    def run_main(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = release.main(list(args))
        return code, out.getvalue() + err.getvalue()

    def test_build_from_tag(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            make_repo(root)
            web = root / "web/dist"
            (web / "assets").mkdir(parents=True)
            (web / "index.html").write_text("<html></html>", encoding="utf-8")
            (web / "assets/app.js").write_text("console.log(1)", encoding="utf-8")
            out = root / "dist/release"
            os.environ["SOURCE_DATE_EPOCH"] = "1759708800"
            try:
                code, text = self.run_main("build", "--root", str(root), "--tag", "v1.2.0", "--go", str(root / "go-stub"),
                                           "--out", str(out), "--web-dist", str(web))
                self.assertEqual(code, 0, text)
                names = sorted(p.name for p in out.iterdir())
                self.assertEqual(names, ["SHA256SUMS", "jelee-1.2.0-linux-amd64.tar.gz", "jelee-1.2.0-linux-arm64.tar.gz",
                                         "jelee-1.2.0-windows-amd64.zip", "jelee-web-1.2.0.tar.gz", "release-notes.md"])
                with tarfile.open(out / "jelee-1.2.0-linux-arm64.tar.gz") as archive:
                    members = {m.name: m for m in archive.getmembers()}
                    self.assertIn("jelee-1.2.0-linux-arm64/jelee", members)
                    self.assertIn("jelee-1.2.0-linux-arm64/LICENSE-COMPLIANCE.md", members)
                    self.assertEqual(members["jelee-1.2.0-linux-arm64/jelee"].mode, 0o755)
                    self.assertEqual(members["jelee-1.2.0-linux-arm64/jelee"].mtime, 1759708800)
                    stamped = archive.extractfile("jelee-1.2.0-linux-arm64/jelee").read().decode()
                    self.assertIn("linux/arm64", stamped)
                    self.assertIn("buildinfo.version=1.2.0", stamped)
                    self.assertTrue(stamped.endswith("./cmd/jelee"))
                with zipfile.ZipFile(out / "jelee-1.2.0-windows-amd64.zip") as archive:
                    self.assertIn("jelee-1.2.0-windows-amd64/jelee-cli.exe", archive.namelist())
                with tarfile.open(out / "jelee-web-1.2.0.tar.gz") as archive:
                    self.assertEqual(sorted(archive.getnames()), ["jelee-web-1.2.0/assets/app.js", "jelee-web-1.2.0/index.html"])
                sums = (out / "SHA256SUMS").read_text(encoding="utf-8").splitlines()
                self.assertEqual(len(sums), 4)
                digest, name = sums[0].split("  ")
                self.assertEqual(hashlib.sha256((out / name).read_bytes()).hexdigest(), digest)
                notes = (out / "release-notes.md").read_text(encoding="utf-8")
                self.assertIn("# Jelee 1.2.0", notes)
                self.assertIn("- feature A", notes)
                self.assertIn("tag `v1.2.0`", notes)
                first = (out / "jelee-1.2.0-linux-amd64.tar.gz").read_bytes()
                # Same inputs, same bytes: archives are reproducible.
                code, text = self.run_main("build", "--root", str(root), "--tag", "v1.2.0", "--go", str(root / "go-stub"), "--out", str(out))
                self.assertEqual(code, 0, text)
                self.assertEqual((out / "jelee-1.2.0-linux-amd64.tar.gz").read_bytes(), first)
                self.assertFalse((out / "jelee-web-1.2.0.tar.gz").exists(), "stale archives must be removed")
            finally:
                del os.environ["SOURCE_DATE_EPOCH"]

    def test_refusals(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            make_repo(root)
            go = str(root / "go-stub")
            cases = [
                (("check", "--root", str(root), "--tag", "1.2.0"), "must start with 'v'"),
                (("check", "--root", str(root), "--tag", "v1.3.0"), "no '## [1.3.0]' section"),
                (("check", "--root", str(root), "--tag", "v1.0.0-rc.1"), "web/package.json version is 1.2.0"),
                (("build", "--root", str(root), "--tag", "v1.2.0", "--go", go, "--targets", "linux"), "bad target"),
                (("build", "--root", str(root), "--tag", "v1.2.0", "--go", go, "--web-dist", str(root / "nope")), "has no index.html"),
                (("build", "--root", str(root), "--tag", "v1.2.0", "--go", str(root / "missing-go")), "release:"),
                (("dry-run", "--root", str(root), "--version", "1.2"), "is not semantic versioning"),
            ]
            for args, want in cases:
                code, text = self.run_main(*args)
                self.assertEqual(code, 1, args)
                self.assertIn(want, text, args)
            code, text = self.run_main("check", "--root", str(root), "--tag", "v1.2.0")
            self.assertEqual(code, 0, text)
            self.assertIn("versions consistent", text)

    def test_dry_run_falls_back_to_unreleased(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            make_repo(root, "1.4.0")
            code, text = self.run_main("dry-run", "--root", str(root), "--go", str(root / "go-stub"), "--targets", "linux/amd64")
            self.assertEqual(code, 0, text)
            notes = (root / ".testdata/release-dry-run/release-notes.md").read_text(encoding="utf-8")
            self.assertIn("- upcoming work", notes)
            self.assertIn("dry run without a tag", notes)


if __name__ == "__main__":
    unittest.main()
