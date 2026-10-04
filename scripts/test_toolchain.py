#!/usr/bin/env python3
import io
from pathlib import Path
import tarfile
import shutil
import stat
import uuid
import unittest
import zipfile
from unittest.mock import patch
import sys
sys.dont_write_bytecode = True
import toolchain
from toolchain import ROOT, assert_hash, local_path, safe_extract, safe_extract_zip


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

    def node_archive(self, link_target):
        path = self.root / "node.tar.xz"
        with tarfile.open(path, "w:xz") as archive:
            item = tarfile.TarInfo("node-v1.2.3-linux-x64/bin/node")
            item.size = 4
            item.mode = 0o755
            archive.addfile(item, io.BytesIO(b"node"))
            link = tarfile.TarInfo("node-v1.2.3-linux-x64/bin/npm")
            link.type = tarfile.SYMTYPE
            link.linkname = link_target
            archive.addfile(link)
        return path

    def test_node_links_skipped_only_when_exact(self):
        allowed = {"node-v1.2.3-linux-x64/bin/npm": "../lib/node_modules/npm/bin/npm-cli.js"}
        out = self.root / "ok"
        out.mkdir()
        safe_extract(self.node_archive("../lib/node_modules/npm/bin/npm-cli.js"), out, "r:xz", allowed)
        self.assertEqual((out / "node-v1.2.3-linux-x64/bin/node").read_bytes(), b"node")
        self.assertFalse((out / "node-v1.2.3-linux-x64/bin/npm").exists(), "skipped link must not be created")
        for target in ("../../../outside", "/etc/passwd"):
            with self.subTest(target=target):
                out = self.root / ("bad-" + uuid.uuid4().hex)
                out.mkdir()
                with self.assertRaises(ValueError):
                    safe_extract(self.node_archive(target), out, "r:xz", allowed)
        out = self.root / "unlisted"
        out.mkdir()
        with self.assertRaises(ValueError):
            safe_extract(self.node_archive("../lib/node_modules/npm/bin/npm-cli.js"), out, "r:xz", {})

    def test_node_bootstrap_removes_bad_cache(self):
        root = "node-v24.21.0-linux-x64"
        spec = {"url": "https://example.invalid/" + root + ".tar.xz", "sha256": "0" * 64,
                "installPath": "node/24.21.0/linux-amd64", "archiveRoot": root,
                "executable": root + "/bin/node", "npmCli": root + "/lib/node_modules/npm/bin/npm-cli.js",
                "skippedLinks": {}}
        archive = self.root / ".tools/downloads" / (root + ".tar.xz")
        archive.parent.mkdir(parents=True)
        archive.write_bytes(b"untrusted archive bytes")
        tool = {"name": "node", "version": "24.21.0", "npmCache": "npm-cache"}
        with patch.object(toolchain, "ROOT", self.root), patch.object(toolchain, "node_selected", return_value=(tool, spec, "linux-amd64")):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                toolchain.bootstrap(offline=True, tools=("node",))
        self.assertFalse(archive.exists(), "failed bootstrap must remove corrupt cache")
        self.assertFalse((self.root / ".bin").exists(), "no wrapper may be written for an unverified archive")

    def test_node_manifest_layout(self):
        tool, spec, target = toolchain.node_selected()
        self.assertRegex(spec["sha256"], "^[a-f0-9]{64}$")
        self.assertTrue(spec["url"].startswith("https://nodejs.org/dist/v" + tool["version"] + "/"))
        self.assertEqual(spec["installPath"], "node/" + tool["version"] + "/" + target)
        broken = dict(spec, installPath="../node")
        manifest = {"schemaVersion": 1, "tools": [dict(tool, platforms={target: broken})]}
        with patch.object(toolchain.Path, "read_text", return_value=__import__("json").dumps(manifest)):
            with self.assertRaisesRegex(ValueError, "invalid Node"):
                toolchain.node_selected()

    def test_golangci_bootstrap_removes_bad_cache(self):
        root = "golangci-lint-2.14.0-linux-amd64"
        spec = {"url": "https://example.invalid/" + root + ".tar.gz", "sha256": "0" * 64,
                "installPath": "golangci-lint/2.14.0/linux-amd64", "archiveRoot": root,
                "executable": root + "/golangci-lint", "licenseFile": root + "/LICENSE"}
        archive = self.root / ".tools/downloads" / (root + ".tar.gz")
        archive.parent.mkdir(parents=True)
        archive.write_bytes(b"untrusted archive bytes")
        tool = {"name": "golangci-lint", "version": "2.14.0", "cache": "cache/golangci-lint"}
        with patch.object(toolchain, "ROOT", self.root), patch.object(toolchain, "golangci_selected", return_value=(tool, spec, "linux-amd64")):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                toolchain.bootstrap(offline=True, tools=("golangci-lint",))
        self.assertFalse(archive.exists(), "failed bootstrap must remove corrupt cache")
        self.assertFalse((self.root / ".bin").exists(), "no wrapper may be written for an unverified archive")

    def test_golangci_manifest_layout(self):
        tool, spec, target = toolchain.golangci_selected()
        self.assertRegex(spec["sha256"], "^[a-f0-9]{64}$")
        self.assertTrue(spec["url"].startswith("https://github.com/golangci/golangci-lint/releases/download/v" + tool["version"] + "/"))
        self.assertEqual(spec["installPath"], "golangci-lint/" + tool["version"] + "/" + target)
        for field, value in (("installPath", "../escape"), ("url", "http://github.com/x.tar.gz"),
                             ("executable", "golangci-lint"), ("sha256", "0" * 63)):
            with self.subTest(field=field):
                broken = dict(spec, **{field: value})
                manifest = {"schemaVersion": 1, "tools": [dict(tool, platforms={target: broken})]}
                with patch.object(toolchain.Path, "read_text", return_value=__import__("json").dumps(manifest)):
                    with self.assertRaisesRegex(ValueError, "invalid golangci-lint"):
                        toolchain.golangci_selected()

    def test_golangci_environment_is_project_local(self):
        go_root = self.root / ".tools/go/1.27.1/linux-amd64/go/bin"
        go_root.mkdir(parents=True)
        (go_root / "go").write_text("", encoding="utf-8")
        go_spec = {"installPath": "go/1.27.1/linux-amd64"}
        tool = {"name": "golangci-lint", "version": "2.14.0", "cache": "cache/golangci-lint"}
        with patch.object(toolchain, "ROOT", self.root), \
                patch.object(toolchain, "selected", return_value=({"version": "1.27.1"}, go_spec, "linux-amd64")), \
                patch.object(toolchain, "golangci_selected", return_value=(tool, {}, "linux-amd64")):
            env = toolchain.golangci_environment()
        self.assertEqual(env["GOLANGCI_LINT_CACHE"], str(self.root / ".tools/cache/golangci-lint"))
        self.assertTrue(env["PATH"].startswith(str(go_root) + toolchain.os.pathsep))
        for key in ("GOCACHE", "GOPATH", "GOMODCACHE", "XDG_CONFIG_HOME"):
            self.assertTrue(env[key].startswith(str(self.root / ".tools")), key)

    def zip_archive(self, entries):
        path = self.root / ("sample-" + uuid.uuid4().hex + ".zip")
        with zipfile.ZipFile(path, "w") as archive:
            for name, mode, data in entries:
                info = zipfile.ZipInfo(name)
                info.external_attr = mode << 16
                archive.writestr(info, data)
        return path

    def test_zip_extract_keeps_modes(self):
        out = self.root / "zip-ok"
        out.mkdir()
        safe_extract_zip(self.zip_archive([("shell/", stat.S_IFDIR | 0o755, b""),
                                           ("shell/chrome-headless-shell", stat.S_IFREG | 0o755, b"elf"),
                                           ("shell/LICENSE.headless_shell", stat.S_IFREG | 0o644, b"text")]), out)
        self.assertEqual((out / "shell/chrome-headless-shell").read_bytes(), b"elf")
        self.assertTrue((out / "shell/chrome-headless-shell").stat().st_mode & 0o100)
        self.assertFalse((out / "shell/LICENSE.headless_shell").stat().st_mode & 0o111)

    def test_zip_unsafe_entries(self):
        for name, mode in [("../escape", stat.S_IFREG | 0o644), ("/escape", stat.S_IFREG | 0o644),
                           ("C:/escape", stat.S_IFREG | 0o644), ("shell/./bad", stat.S_IFREG | 0o644),
                           ("shell\\bad", stat.S_IFREG | 0o644), ("shell/link", stat.S_IFLNK | 0o777)]:
            with self.subTest(name=name):
                out = self.root / ("zip-bad-" + uuid.uuid4().hex)
                out.mkdir()
                with self.assertRaises(ValueError):
                    safe_extract_zip(self.zip_archive([(name, mode, b"x")]), out)

    def test_playwright_bootstrap_removes_bad_cache(self):
        tool, spec, target = toolchain.playwright_selected()
        archive = self.root / ".tools/downloads" / spec["downloadName"]
        archive.parent.mkdir(parents=True)
        archive.write_bytes(b"untrusted archive bytes")
        with patch.object(toolchain, "ROOT", self.root), \
                patch.object(toolchain, "playwright_selected", return_value=(tool, spec, target)):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                toolchain.bootstrap(offline=True, tools=("playwright",))
        self.assertFalse(archive.exists(), "failed bootstrap must remove corrupt cache")
        self.assertFalse((self.root / ".tools" / spec["installPath"]).exists(), "nothing may be installed from an unverified archive")

    def test_playwright_manifest_layout(self):
        tool, spec, target = toolchain.playwright_selected()
        self.assertNotIn(toolchain.PLAYWRIGHT, toolchain.ALL_TOOLS, "the browser download must stay opt-in")
        self.assertTrue(spec["url"].startswith("https://cdn.playwright.dev/builds/cft/" + tool["browser"]["version"] + "/"))
        self.assertTrue(spec["installPath"].startswith(spec["browsersPath"] + "/"))
        for field, value in (("installPath", "../escape"), ("url", "http://cdn.playwright.dev/x.zip"),
                             ("browsersPath", "/home/user/.cache/ms-playwright"), ("sha256", "0" * 63),
                             ("executable", "chrome"), ("downloadName", "chrome-headless-shell-linux64.zip")):
            with self.subTest(field=field):
                broken = dict(spec, **{field: value})
                manifest = {"schemaVersion": 1, "tools": [dict(tool, platforms={target: broken})]}
                with patch.object(toolchain.Path, "read_text", return_value=__import__("json").dumps(manifest)):
                    with self.assertRaisesRegex(ValueError, "invalid Playwright"):
                        toolchain.playwright_selected()
        reserved = {"schemaVersion": 1, "tools": [dict(tool, status="reserved")]}
        with patch.object(toolchain.Path, "read_text", return_value=__import__("json").dumps(reserved)):
            with self.assertRaisesRegex(ValueError, "invalid Playwright"):
                toolchain.playwright_selected()

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
