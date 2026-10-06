import copy
import gzip
import importlib.util
import io
import json
import lzma
import os
from pathlib import Path
import struct
import socket
import tarfile
import tempfile
import unittest
import threading
import time
from unittest import mock

spec = importlib.util.spec_from_file_location("runtime_tools", Path(__file__).with_name("runtime-tools.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

ELF = b"\x7fELF\x02\x01" + bytes(12) + b"\x3e\x00" + bytes(44)
FILE = {"source": "usr/lib/x.so", "destination": "lib/x.so", "kind": "elf", "sha256": None}


def tar(entries):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        for name, value, kind in entries:
            info = tarfile.TarInfo(name)
            info.type = kind
            info.mode = 0o6755
            if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE):
                info.linkname = value
                archive.addfile(info)
            elif kind == tarfile.REGTYPE:
                info.size = len(value)
                archive.addfile(info, io.BytesIO(value))
            else:
                archive.addfile(info)
    return buffer.getvalue()


def ar(members):
    value = bytearray(b"!<arch>\n")
    for name, data in members:
        value.extend((name + "/").ljust(16).encode())
        value.extend(b"0           0     0     100644  ")
        value.extend(str(len(data)).ljust(10).encode() + b"`\n")
        value.extend(data)
        if len(data) % 2:
            value.extend(b"\n")
    return bytes(value)


def deb(raw, compression="xz"):
    compressed = lzma.compress(raw) if compression == "xz" else gzip.compress(raw)
    return ar([("debian-binary", b"2.0\n"), ("control.tar.gz", b"not parsed; malicious maintainer scripts"),
               ("data.tar." + compression, compressed)])


def fixture(directory):
    cache = directory / ".tools" / "downloads"
    cache.mkdir(parents=True)
    archive = deb(tar([("./", b"", tarfile.DIRTYPE), (FILE["source"], ELF, tarfile.REGTYPE),
                       ("usr/share/doc/example", "../../../../outside", tarfile.SYMTYPE)]))
    (cache / "example.deb").write_bytes(archive)
    manifest = {"mediaRuntime": {"schemaVersion": 1, "platform": "linux-amd64", "readyToExecute": False,
        "installPath": "media-runtime/linux-amd64/example-pin", "licenseTexts": [],
        "packages": [{"name": "example", "url": "https://example.test/example.deb",
                      "sha256": m.digest(archive), "sizeBytes": len(archive), "files": [copy.deepcopy(FILE)]}]}}
    return cache, manifest


class ArchiveTests(unittest.TestCase):
    def rejected(self, fn, *args):
        with self.assertRaises(m.Rejected):
            fn(*args)

    def test_select_only_regular_files_and_ignore_control(self):
        for compression in ("xz", "gz"):
            raw = tar([("./", b"", tarfile.DIRTYPE), (FILE["source"], ELF, tarfile.REGTYPE),
                       ("usr/ignored", b"secret", tarfile.REGTYPE),
                       ("usr/link", "/outside", tarfile.SYMTYPE)])
            name, data = m.deb_data(deb(raw, compression))
            self.assertEqual(m.selected_files(m.decompress(name, data), [FILE]), {FILE["source"]: ELF})

    def test_ar_rejects_header_magic_padding_and_truncation(self):
        valid = deb(tar([(FILE["source"], ELF, tarfile.REGTYPE)]))
        for broken in (b"bad" + valid[3:], valid[:-1], valid + b"x", valid[:66] + b"!!" + valid[68:],
                       valid[:56] + b"-1        " + valid[66:]):
            with self.subTest(prefix=broken[:8]):
                self.rejected(m.deb_data, broken)
        members = [("debian-binary", b"2.0\n"), ("control.tar.gz", b"x"), ("data.tar.gz", b"x")]
        padded = ar(members)
        self.rejected(m.deb_data, padded[:-1] + b"x")

    def test_ar_rejects_unknown_duplicate_or_reordered_members(self):
        candidates = [
            [("debian-binary", b"3.0\n"), ("control.tar.gz", b""), ("data.tar.gz", b"")],
            [("debian-binary", b"2.0\n"), ("data.tar.gz", b""), ("control.tar.gz", b"")],
            [("debian-binary", b"2.0\n"), ("control.tar.gz", b""), ("control.tar.gz", b"")],
            [("debian-binary", b"2.0\n"), ("control.tar.gz", b""), ("data.tar.zst", b"")],
        ]
        for candidate in candidates:
            self.rejected(m.deb_data, ar(candidate))

    def test_decompression_rejects_bombs_trailing_and_truncated_streams(self):
        for ext, compressor in (("xz", lzma.compress), ("gz", gzip.compress)):
            compressed = compressor(b"x" * 65536)
            self.rejected(m.decompress, "data.tar." + ext, compressed, 1024)
            self.rejected(m.decompress, "data.tar." + ext, compressed + b"trailing")
            self.rejected(m.decompress, "data.tar." + ext, compressed + compressed)
            self.rejected(m.decompress, "data.tar." + ext, compressed[:-1])
            self.assertEqual(m.decompress("data.tar." + ext, compressed, 65536), b"x" * 65536)

    def test_xz_rejects_excessive_decoder_memory(self):
        compressed = lzma.compress(b"x", filters=[{"id": lzma.FILTER_LZMA2, "dict_size": 128 * 1024 * 1024}])
        self.rejected(m.decompress, "data.tar.xz", compressed)

    def test_tar_rejects_unsafe_paths_even_when_unselected(self):
        for name in ("../outside", "/absolute", "a/../../escape", "C:/drive", "back\\slash", "a//b", "a/./b"):
            raw = tar([(FILE["source"], ELF, tarfile.REGTYPE), (name, b"bad", tarfile.REGTYPE)])
            self.rejected(m.selected_files, raw, [FILE])

    def test_selected_link_device_fifo_directory_rejected(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.CHRTYPE, tarfile.FIFOTYPE, tarfile.DIRTYPE):
            raw = tar([(FILE["source"], "target" if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else b"", kind)])
            self.rejected(m.selected_files, raw, [FILE])

    def test_tar_missing_duplicate_and_bad_checksum_rejected(self):
        one = (FILE["source"], ELF, tarfile.REGTYPE)
        for raw in (tar([]), tar([one, one]), b"X" + tar([one])[1:], tar([one])[:1024]):
            self.rejected(m.selected_files, raw, [FILE])

    def test_tar_trailing_nonzero_rejected(self):
        raw = tar([(FILE["source"], ELF, tarfile.REGTYPE)])
        self.rejected(m.selected_files, raw + b"x", [FILE])

    def test_wrong_elf_architecture_rejected(self):
        for bad in (b"not elf", ELF[:18] + struct.pack("<H", 183) + ELF[20:], ELF[:5] + b"\x02" + ELF[6:]):
            self.rejected(m.selected_files, tar([(FILE["source"], bad, tarfile.REGTYPE)]), [FILE])

    def test_manifest_paths_and_duplicates_rejected(self):
        raw = tar([(FILE["source"], ELF, tarfile.REGTYPE)])
        for change in ({"destination": "../escape"}, {"source": "./usr/lib/x.so"}, {"kind": "script"}):
            spec = dict(FILE, **change)
            self.rejected(m.selected_files, raw, [spec])
        self.rejected(m.selected_files, raw, [FILE, FILE])

    def test_notice_and_file_limits(self):
        notice = {"source": "notice", "destination": "licenses/notice", "kind": "notice", "sha256": None}
        raw = tar([("notice", b"x" * (m.MAX_NOTICE + 1), tarfile.REGTYPE)])
        self.rejected(m.selected_files, raw, [notice])

    def test_extended_sparse_and_unknown_tar_types_are_refused(self):
        for kind in (tarfile.XHDTYPE, tarfile.GNUTYPE_LONGNAME, tarfile.GNUTYPE_SPARSE, b"9"):
            self.rejected(m.selected_files, tar([(FILE["source"], b"", kind)]), [FILE])

    def test_archive_size_cap(self):
        self.rejected(m.deb_data, b"!<arch>\n" + bytes(m.MAX_ARCHIVE))


class InstallTests(unittest.TestCase):
    def setUp(self):
        scratch = Path(__file__).parent.parent / ".testdata"
        scratch.mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(prefix="runtime-test-", dir=scratch)
        self.project = Path(self.temp.name)
        self.cache, self.manifest = fixture(self.project)

    def tearDown(self):
        self.temp.cleanup()

    def ready(self):
        self.manifest["mediaRuntime"]["readyToExecute"] = True
        self.manifest["mediaRuntime"]["packages"][0]["files"][0]["sha256"] = m.digest(ELF)

    def test_inspection_produces_hash_without_install_or_execution(self):
        files, report = m.prepare(self.manifest, self.cache)
        self.assertEqual(report[0]["sha256"], m.digest(ELF))
        self.assertEqual(files, {"lib/x.so": (ELF, "elf")})
        self.assertFalse((self.project / ".tools" / "media-runtime").exists())

    def test_install_requires_ready_and_all_individual_hashes(self):
        with self.assertRaisesRegex(m.Rejected, "manifest_not_ready"):
            m.prepare(self.manifest, self.cache, install=True)
        self.manifest["mediaRuntime"]["readyToExecute"] = True
        with self.assertRaisesRegex(m.Rejected, "selected_identity"):
            m.prepare(self.manifest, self.cache, install=True)

    def test_bad_archive_rejected_before_decode(self):
        (self.cache / "example.deb").write_bytes(b"bad archive")
        with self.assertRaisesRegex(m.Rejected, "cached_identity"):
            m.prepare(self.manifest, self.cache)

    def test_wrong_individual_hash_rejected(self):
        self.ready()
        self.manifest["mediaRuntime"]["packages"][0]["files"][0]["sha256"] = "0" * 64
        with self.assertRaisesRegex(m.Rejected, "selected_identity"):
            m.prepare(self.manifest, self.cache, install=True)

    @unittest.skipUnless(os.name == "posix", "draft Linux installer")
    def test_install_records_identity_clears_setuid_and_never_overwrites(self):
        self.ready()
        files, _ = m.prepare(self.manifest, self.cache, install=True)
        target = m.install_files(self.project, self.manifest, files)
        self.assertEqual((target / "lib/x.so").read_bytes(), ELF)
        self.assertEqual((target / "lib/x.so").stat().st_mode & 0o7777, 0o555)
        self.assertFalse(json.loads((target / "installed.json").read_text())["executed"])
        with self.assertRaisesRegex(m.Rejected, "installation_exists"):
            m.install_files(self.project, self.manifest, files)
        self.assertEqual(list((self.project / ".tools").glob(".runtime-stage-*")), [])

    @unittest.skipUnless(os.name == "posix", "symlink test")
    def test_symlink_cache_and_destination_rejected(self):
        path = self.cache / "example.deb"
        path.rename(self.cache / "original.deb")
        path.symlink_to("original.deb")
        with self.assertRaisesRegex(m.Rejected, "symlink_path"):
            m.prepare(self.manifest, self.cache)
        self.ready()
        (self.project / ".tools/media-runtime").symlink_to(self.cache, target_is_directory=True)
        with self.assertRaisesRegex(m.Rejected, "symlink_path"):
            m.install_files(self.project, self.manifest, {"lib/x.so": (ELF, "elf")})

    @unittest.skipUnless(os.name == "posix", "draft Linux installer")
    def test_concurrent_install_lock_and_staging_cleanup(self):
        self.ready()
        with m.installation_lock(self.project):
            with self.assertRaisesRegex(m.Rejected, "installation_locked"):
                with m.installation_lock(self.project):
                    self.fail("second lock acquired")
        # An OS lock is released on exit; the retained file is not a stale lock.
        with m.installation_lock(self.project):
            pass
        with self.assertRaisesRegex(m.Rejected, "invalid_path"):
            m.install_files(self.project, self.manifest, {"../outside": (ELF, "elf")})
        self.assertEqual(list((self.project / ".tools").glob(".runtime-stage-*")), [])

    def test_offline_missing_and_corrupt_cache_removed_only_known_archive(self):
        item = self.manifest["mediaRuntime"]["packages"][0]
        unrelated = self.cache / "unrelated"
        unrelated.write_bytes(b"keep")
        for bad in (b"short", b"x" * item["sizeBytes"]):
            (self.cache / "example.deb").write_bytes(bad)
            with self.assertRaisesRegex(m.Rejected, "cached_identity"):
                m.fetch(self.project, item, offline=True)
            self.assertFalse((self.cache / "example.deb").exists())
            self.assertEqual(unrelated.read_bytes(), b"keep")
        with self.assertRaisesRegex(m.Rejected, "offline_missing"):
            m.fetch(self.project, item, offline=True)

    def test_download_bounds_hash_failure_and_partial_cleanup(self):
        item = self.manifest["mediaRuntime"]["packages"][0]
        original = (self.cache / "example.deb").read_bytes()
        (self.cache / "example.deb").unlink()
        for body in (original + b"x", b"x" * len(original), original[:-1]):
            response = io.BytesIO(body)
            response.geturl = lambda: item["url"]
            with mock.patch.object(m.urllib.request, "build_opener") as opener:
                opener.return_value.open.return_value = response
                with self.assertRaises(m.Rejected):
                    m.fetch(self.project, item)
            self.assertFalse((self.cache / "example.deb").exists())
            self.assertEqual(list(self.cache.glob("*.partial")), [])

    def test_download_success_and_https_downgrade_rejected(self):
        item = self.manifest["mediaRuntime"]["packages"][0]
        original = (self.cache / "example.deb").read_bytes()
        (self.cache / "example.deb").unlink()
        response = io.BytesIO(original)
        response.geturl = lambda: item["url"]
        with mock.patch.object(m.urllib.request, "build_opener") as opener:
            opener.return_value.open.return_value = response
            m.fetch(self.project, item)
        self.assertEqual((self.cache / "example.deb").read_bytes(), original)
        for url in ("http://example.test/file", "https://user:pass@example.test/file"):
            with self.assertRaises(m.Rejected):
                m.HTTPSRedirect().redirect_request(None, None, 302, "redirect", {}, url)

    def test_unreachable_source_falls_back_to_pinned_mirror_with_same_hash(self):
        item = copy.deepcopy(self.manifest["mediaRuntime"]["packages"][0])
        item["mirrorURLs"] = ["https://mirror.example.test/pool/example.deb"]
        original = (self.cache / "example.deb").read_bytes()
        (self.cache / "example.deb").unlink()
        opened = []

        def open_uri(uri, timeout):
            opened.append(uri)
            if uri == item["url"]:
                raise m.urllib.error.URLError("timed out")
            response = io.BytesIO(original)
            response.geturl = lambda: uri
            return response
        with mock.patch.object(m.urllib.request, "build_opener") as opener:
            opener.return_value.open.side_effect = open_uri
            m.fetch(self.project, item)
        self.assertEqual(opened, [item["url"], item["mirrorURLs"][0]])
        self.assertEqual((self.cache / "example.deb").read_bytes(), original)
        # A mirror serving different bytes is rejected like the origin would be.
        (self.cache / "example.deb").unlink()

        def tampered(uri, timeout):
            if uri == item["url"]:
                raise m.urllib.error.URLError("timed out")
            response = io.BytesIO(b"x" * len(original))
            response.geturl = lambda: uri
            return response
        with mock.patch.object(m.urllib.request, "build_opener") as opener:
            opener.return_value.open.side_effect = tampered
            with self.assertRaises(m.Rejected):
                m.fetch(self.project, item)
        self.assertFalse((self.cache / "example.deb").exists())
        self.assertEqual(list(self.cache.glob("*.partial")), [])

    def test_mirrors_validated_and_excluded_from_runtime_identity(self):
        manifest = json.loads((Path(__file__).resolve().parent.parent / "tools/manifest.json").read_text(encoding="utf-8"))
        m.validate_manifest(manifest, require_ready=True)
        item = manifest["mediaRuntime"]["licenseTexts"][0]
        self.assertTrue(item["mirrorURLs"])
        before = m.runtime_identity(manifest)
        item["mirrorURLs"] = []
        self.assertEqual(m.runtime_identity(manifest), before)
        name = item["url"].rsplit("/", 1)[1]
        for bad in (["http://mirror.example.test/" + name], ["https://mirror.example.test/other.txt"],
                    [item["url"]], ["https://a.example.test/" + name] * 2, "https://a.example.test/" + name,
                    ["https://%s.example.test/%s" % (host, name) for host in "abcd"]):
            item["mirrorURLs"] = bad
            with self.assertRaisesRegex(m.Rejected, "manifest_mirror|manifest_url"):
                m.validate_manifest(manifest)

    def test_offline_verification_never_repairs_modified_installed_files(self):
        self.ready()
        files, _ = m.prepare(self.manifest, self.cache, True)
        target = m.install_files(self.project, self.manifest, files)
        m.verify_install(self.project, self.manifest, files)
        installed = target / "lib/x.so"
        installed.chmod(0o755)
        installed.write_bytes(b"tampered")
        installed.chmod(0o555)
        with self.assertRaisesRegex(m.Rejected, "cached_identity"):
            m.verify_install(self.project, self.manifest, files)
        self.assertEqual(installed.read_bytes(), b"tampered")

    def test_changed_https_origin_reuses_only_identical_verified_cache(self):
        item = copy.deepcopy(self.manifest["mediaRuntime"]["packages"][0])
        expected = (self.cache / "example.deb").read_bytes()
        item["url"] = "https://replacement.example.test/archives/example.deb"
        with mock.patch.object(m.urllib.request, "build_opener", side_effect=AssertionError("offline cache used network")):
            actual = m.fetch(self.project, item, offline=True)
        self.assertEqual(actual.read_bytes(), expected)
        item["sha256"] = "0" * 64
        with self.assertRaisesRegex(m.Rejected, "cached_identity"):
            m.fetch(self.project, item, offline=True)
        self.assertFalse(actual.exists())

    def test_url_only_manifest_change_refuses_old_install_record_without_repair(self):
        self.ready()
        files, _ = m.prepare(self.manifest, self.cache, True)
        target = m.install_files(self.project, self.manifest, files)
        record = (target / "installed.json").read_bytes()
        changed = copy.deepcopy(self.manifest)
        changed["mediaRuntime"]["packages"][0]["url"] = "https://replacement.example.test/example.deb"
        same_files, _ = m.prepare(changed, self.cache, True)
        self.assertEqual(files, same_files)
        with self.assertRaisesRegex(m.Rejected, "installed_record"):
            m.verify_install(self.project, changed, same_files)
        self.assertEqual((target / "installed.json").read_bytes(), record)
        self.assertEqual((target / "lib/x.so").read_bytes(), ELF)
        m.verify_install(self.project, self.manifest, files)

    def test_verification_rejects_changed_record_extra_files_and_links(self):
        self.ready()
        files, _ = m.prepare(self.manifest, self.cache, True)
        target = m.install_files(self.project, self.manifest, files)
        (target / "extra").write_bytes(b"unexpected")
        with self.assertRaisesRegex(m.Rejected, "installed_inventory"):
            m.verify_install(self.project, self.manifest, files)
        (target / "extra").unlink()
        library = target / "lib/x.so"
        library.unlink()
        library.symlink_to(self.cache / "example.deb")
        with self.assertRaisesRegex(m.Rejected, "symlink_path"):
            m.verify_install(self.project, self.manifest, files)
        library.unlink()
        library.write_bytes(ELF)
        library.chmod(0o555)
        (target / "installed.json").write_text('{}')
        with self.assertRaisesRegex(m.Rejected, "installed_record"):
            m.verify_install(self.project, self.manifest, files)

    def test_symlink_download_cache_never_deleted(self):
        item = self.manifest["mediaRuntime"]["packages"][0]
        archive = self.cache / "example.deb"
        original = self.cache / "original.deb"
        archive.rename(original)
        archive.symlink_to(original.name)
        with self.assertRaisesRegex(m.Rejected, "symlink_path"):
            m.fetch(self.project, item, True)
        self.assertTrue(archive.is_symlink())
        self.assertTrue(original.exists())

    def test_manifest_http_credentials_query_fragment_rejected(self):
        for url in ("http://example.test/file", "https://a:b@example.test/file", "https://example.test/file?q=1", "https://example.test/file#part"):
            with self.assertRaises(m.Rejected):
                m.cache_name(url)


class ManifestTests(unittest.TestCase):
    def setUp(self):
        self.manifest = json.loads((m.ROOT / "tools/manifest.json").read_text())

    def test_shipped_manifest_fixed_eight_file_closure(self):
        m.validate_manifest(self.manifest, True)

    def test_unknown_extra_or_missing_runtime_library_refused(self):
        runtime = self.manifest["mediaRuntime"]
        original = copy.deepcopy(runtime["packages"][0]["files"])
        for change in (original[:-1], original + [copy.deepcopy(original[0])]):
            runtime["packages"][0]["files"] = change
            with self.assertRaises(m.Rejected):
                m.validate_manifest(self.manifest, True)
        runtime["packages"][0]["files"] = original
        runtime["packages"][0]["files"][0]["destination"] = "lib/other.so"
        with self.assertRaisesRegex(m.Rejected, "manifest_closure"):
            m.validate_manifest(self.manifest, True)

    def test_ready_without_hash_and_unsafe_install_root_refused(self):
        runtime = self.manifest["mediaRuntime"]
        runtime["packages"][0]["files"][0]["sha256"] = None
        with self.assertRaisesRegex(m.Rejected, "manifest_file_hash"):
            m.validate_manifest(self.manifest, True)
        runtime["installPath"] = "../../outside"
        with self.assertRaisesRegex(m.Rejected, "manifest_install_path"):
            m.validate_manifest(self.manifest)

    def test_no_floating_package_or_cache_collision(self):
        runtime = self.manifest["mediaRuntime"]
        runtime["packages"][0]["sha256"] = ""
        with self.assertRaisesRegex(m.Rejected, "manifest_identity"):
            m.validate_manifest(self.manifest)
        runtime["packages"][0]["sha256"] = "a" * 64
        runtime["licenseTexts"][0]["url"] = runtime["packages"][0]["url"]
        with self.assertRaisesRegex(m.Rejected, "manifest_cache_collision"):
            m.validate_manifest(self.manifest)


class DeadlineTests(unittest.TestCase):
    def test_slow_status_header_and_body_have_absolute_deadline(self):
        for mode in ("status", "header", "body"):
            with self.subTest(mode=mode):
                reader, writer = socket.socketpair()
                stop = threading.Event()
                first = {"status": b"", "header": b"HTTP/1.1 200 OK\r\nX-Slow: ",
                         "body": b"HTTP/1.1 200 OK\r\nContent-Length: 65536\r\n\r\n"}[mode]
                writer.sendall(first)
                def trickle():
                    while not stop.wait(0.01):
                        try:
                            writer.sendall(b"x")
                        except OSError:
                            break
                thread = threading.Thread(target=trickle)
                thread.start()
                started = time.monotonic()
                response = m.DeadlineResponse(reader, deadline=started + 0.1)
                try:
                    with self.assertRaises(TimeoutError):
                        response.begin()
                        while True:
                            response.read1(65536)
                    self.assertLess(time.monotonic() - started, 1)
                finally:
                    stop.set()
                    response.close()
                    reader.close()
                    writer.close()
                    thread.join(1)
                    self.assertFalse(thread.is_alive())

    def test_expired_deadline_does_not_begin_connection_or_socket_read(self):
        with self.assertRaises(TimeoutError):
            m.DeadlineHTTPSConnection("example.test", deadline=time.monotonic()-1)
        raw, sock = mock.Mock(), mock.Mock()
        reader = m.DeadlineReader(raw, sock, time.monotonic()-1)
        with self.assertRaises(TimeoutError):
            reader.readinto(bytearray(1))
        raw.readinto.assert_not_called()
        reader.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
