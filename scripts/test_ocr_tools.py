#!/usr/bin/env python3
"""Offline tests of the optional Tesseract bootstrap for subtitle OCR (G15.6).

No network access and no pinned package is needed: synthetic Debian packages
stand in for the archive, and the version check is replaced.
"""
import copy
import hashlib
import importlib.util
import io
import json
import lzma
from pathlib import Path
import shutil
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True

_spec = importlib.util.spec_from_file_location("ocr_tools", Path(__file__).with_name("ocr-tools.py"))
ocr = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ocr)

ELF = b"\x7fELF\x02\x01\x01" + bytes(11) + b"\x3e\x00" + bytes(60)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def tar_xz(files):
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        for name, data in files.items():
            info = tarfile.TarInfo("./" + name)
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
    return lzma.compress(raw.getvalue())


def deb(files):
    def member(name, data):
        header = name.ljust(16).encode() + b"0".ljust(12) + b"0".ljust(6) + b"0".ljust(6) + b"100644".ljust(8) + str(len(data)).ljust(10).encode() + b"`\n"
        return header + data + (b"\n" if len(data) % 2 else b"")
    return b"!<arch>\n" + member("debian-binary", b"2.0\n") + member("control.tar.xz", tar_xz({"control": b"Package: x\n"})) + member("data.tar.xz", tar_xz(files))


class OCRToolsTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        (self.root / "tools").mkdir()
        (self.root / ".tools/downloads").mkdir(parents=True)
        self.executable = ELF + b"tesseract"
        self.library = ELF + b"libtesseract"
        self.models = {code: ("model " + code).encode() * 10 for code in ocr.LANGUAGES}
        self.notice = b"Apache-2.0 notice\n"
        packages = [self.package("tesseract-ocr", {
            "usr/bin/tesseract": ("bin/tesseract", "executable", self.executable),
            "usr/share/doc/tesseract-ocr/copyright": ("licenses/tesseract-ocr/copyright", "notice", self.notice)}),
            self.package("libtesseract5", {"usr/lib/x86_64-linux-gnu/libtesseract.so.5.0.5": ("lib/libtesseract.so.5", "elf", self.library)})]
        languages = {}
        for code, data in self.models.items():
            name = "tesseract-ocr-" + code.replace("_", "-")
            packages.append(self.package(name, {"usr/share/tesseract-ocr/5/tessdata/" + code + ".traineddata": ("tessdata/" + code + ".traineddata", "data", data)}))
            languages[code] = {"package": name}
        self.manifest = {"schemaVersion": 1, "ocrTools": {"schemaVersion": 1, "optional": True, "packages": packages, "tools": {"tesseract": {
            "version": "5.5.0", "debianVersion": "5.5.0-1+b1", "platforms": {"linux-amd64": {
                "installPath": "ocr/tesseract/5.5.0-1+b1/linux-amd64", "versionArguments": ["--version"], "versionLine": "tesseract 5.5.0",
                "executable": {"path": "bin/tesseract", "containerPath": "/usr/lib/jelee/tesseract/tesseract", "closure": []},
                "tessdataPath": "tessdata", "languages": languages}}}}}}
        self.write_manifest()
        self.patches = [mock.patch.object(ocr, "ROOT", self.root), mock.patch.object(ocr, "check_version", lambda spec, install: None)]
        for patch in self.patches:
            patch.start()

    def tearDown(self):
        for patch in self.patches:
            patch.stop()
        shutil.rmtree(self.root, ignore_errors=True)

    def package(self, name, files):
        data = deb({source: content for source, (_, _, content) in files.items()})
        filename = name + "_1_amd64.deb"
        (self.root / ".tools/downloads" / filename).write_bytes(data)
        return {"name": name, "version": "1", "architecture": "amd64", "url": "https://deb.debian.org/debian/pool/main/x/" + filename,
                "sha256": sha(data), "sizeBytes": len(data), "archive": "deb-ar",
                "files": [{"source": source, "destination": destination, "kind": kind, "sha256": sha(content), "sizeBytes": len(content)}
                          for source, (destination, kind, content) in files.items()]}

    def write_manifest(self):
        (self.root / "tools/manifest.json").write_text(json.dumps(self.manifest), encoding="utf-8")

    def install_dir(self):
        return self.root / ".tools/ocr/tesseract/5.5.0-1+b1/linux-amd64"

    def bootstrap(self):
        entry, spec, packages = ocr.load()
        ocr.bootstrap(entry, spec, packages, True)

    def test_offline_bootstrap_installs_exactly_the_pinned_files(self):
        self.bootstrap()
        install = self.install_dir()
        self.assertEqual((install / "bin/tesseract").read_bytes(), self.executable)
        self.assertEqual((install / "tessdata/chi_tra.traineddata").read_bytes(), self.models["chi_tra"])
        found = sorted(path.relative_to(install).as_posix() for path in install.rglob("*") if path.is_file())
        self.assertEqual(found, sorted(["bin/tesseract", "lib/libtesseract.so.5", "licenses/tesseract-ocr/copyright"]
                                       + ["tessdata/" + code + ".traineddata" for code in ocr.LANGUAGES]))
        self.assertEqual((install / "tessdata/eng.traineddata").stat().st_mode & 0o777, 0o444)
        self.assertEqual((install / "lib/libtesseract.so.5").stat().st_mode & 0o777, 0o555)
        record = json.loads((self.root / ".tools/ocr-installed/tesseract-linux-amd64.json").read_text())
        self.assertEqual(record["version"], "5.5.0-1+b1")
        self.assertTrue((self.root / ".bin/tesseract").is_file())
        # Verifying again succeeds; a second bootstrap reuses the install.
        entry, spec, packages = ocr.load()
        ocr.verify(entry, spec, packages)
        self.bootstrap()

    def test_changed_or_extra_installed_files_are_refused(self):
        self.bootstrap()
        entry, spec, packages = ocr.load()
        target = self.install_dir() / "tessdata/jpn.traineddata"
        target.chmod(0o644)
        target.write_bytes(b"tampered model")
        with self.assertRaisesRegex(ocr.Rejected, "installed file differs"):
            ocr.verify(entry, spec, packages)
        target.write_bytes(self.models["jpn"])
        (self.install_dir() / "tessdata/fra.traineddata").write_bytes(b"extra")
        with self.assertRaisesRegex(ocr.Rejected, "inventory differs"):
            ocr.verify(entry, spec, packages)

    def test_tampered_cache_is_removed(self):
        cached = self.root / ".tools/downloads/libtesseract5_1_amd64.deb"
        cached.write_bytes(b"not the pinned package")
        with self.assertRaisesRegex(ocr.Rejected, "removed"):
            self.bootstrap()
        self.assertFalse(cached.exists())
        with self.assertRaisesRegex(ocr.Rejected, "offline package is missing"):
            self.bootstrap()

    def test_manifest_policy_is_enforced(self):
        cases = {
            "not optional": lambda m: m["ocrTools"].update(optional=False),
            "http": lambda m: m["ocrTools"]["packages"][0].update(url="http://deb.debian.org/x.deb"),
            "escape": lambda m: m["ocrTools"]["packages"][0]["files"][0].update(destination="../bin/tesseract"),
            "kind": lambda m: m["ocrTools"]["packages"][0]["files"][0].update(kind="script"),
            "install path": lambda m: m["ocrTools"]["tools"]["tesseract"]["platforms"]["linux-amd64"].update(installPath="ocr/other"),
            "language": lambda m: m["ocrTools"]["tools"]["tesseract"]["platforms"]["linux-amd64"]["languages"].pop("jpn"),
            "duplicate": lambda m: m["ocrTools"]["packages"][1]["files"][0].update(destination="bin/tesseract"),
        }
        original = copy.deepcopy(self.manifest)
        for name, change in cases.items():
            with self.subTest(name):
                self.manifest = copy.deepcopy(original)
                change(self.manifest)
                self.write_manifest()
                with self.assertRaises((ocr.Rejected, KeyError)):
                    ocr.load()

    def test_data_kind_needs_an_explicit_opt_in(self):
        raw = lzma.decompress(tar_xz({"model": b"data"}))
        spec = [{"source": "model", "destination": "model", "kind": "data"}]
        with self.assertRaises(ocr.runtime_tools.Rejected):
            ocr.runtime_tools.selected_files(raw, spec)
        self.assertEqual(ocr.runtime_tools.selected_files(raw, spec, kinds=("data",)), {"model": b"data"})
        elf = [{"source": "model", "destination": "model", "kind": "elf"}]
        with self.assertRaises(ocr.runtime_tools.Rejected):
            ocr.runtime_tools.selected_files(raw, elf)

    def test_developer_environment_drops_loader_and_tesseract_overrides(self):
        with mock.patch.dict("os.environ", {"LD_PRELOAD": "x", "TESSDATA_PREFIX": "/tmp", "OMP_NUM_THREADS": "8", "KEEP": "1"}):
            env = ocr.tool_environment(Path("/opt/install"))
        self.assertNotIn("LD_PRELOAD", env)
        self.assertNotIn("TESSDATA_PREFIX", env)
        self.assertEqual(env["OMP_THREAD_LIMIT"], "1")
        self.assertEqual(env["LD_LIBRARY_PATH"], "/opt/install/lib")
        self.assertEqual(env["KEEP"], "1")


if __name__ == "__main__":
    unittest.main()
