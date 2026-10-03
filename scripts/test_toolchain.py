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
