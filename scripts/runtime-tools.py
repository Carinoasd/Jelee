#!/usr/bin/env python3
"""Optional pinned Linux amd64 runtime packages; never execute package contents."""
import argparse
import hashlib
import http.client
import io
import json
import lzma
import os
import platform
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import tempfile
import time
import urllib.parse
import urllib.request
import uuid
import zlib
from contextlib import contextmanager

ROOT = Path(__file__).resolve().parent.parent

MAX_ARCHIVE = 16 * 1024 * 1024
MAX_TAR = 64 * 1024 * 1024
MAX_FILE = 8 * 1024 * 1024
MAX_NOTICE = 1024 * 1024
MAX_SELECTED = 32 * 1024 * 1024
MAX_MEMBERS = 10000
MAX_SOURCE = 128 * 1024 * 1024
SHA = re.compile(r"[0-9a-f]{64}\Z")
LIBRARIES = {"lib64/ld-linux-x86-64.so.2"} | {
    "lib/x86_64-linux-gnu/" + name for name in
    ("libc.so.6", "libm.so.6", "libmvec.so.1", "libdl.so.2", "libpthread.so.0", "librt.so.1", "libgcc_s.so.1")}


class Rejected(ValueError):
    pass


def need(condition, code):
    if not condition:
        raise Rejected(code)


def canonical(name, allow_root=False):
    need(isinstance(name, str) and 0 < len(name.encode("utf-8")) <= 1024,
         "invalid_path")
    if name.startswith("./"):
        name = name[2:]
    if allow_root and name in ("", "."):
        return "."
    name = name.rstrip("/") if allow_root else name
    parts = name.split("/")
    need(name and not any(p in ("", ".", "..") for p in parts)
         and not any(c in name for c in "\\:\x00")
         and all(ord(c) >= 32 and ord(c) != 127 for c in name), "invalid_path")
    return name


def digest(value):
    return hashlib.sha256(value).hexdigest()


def deb_data(archive):
    need(len(archive) <= MAX_ARCHIVE and archive.startswith(b"!<arch>\n"), "ar_magic_or_limit")
    pos, members = 8, []
    while pos < len(archive):
        need(len(members) < 3 and pos + 60 <= len(archive), "ar_header")
        header = archive[pos:pos + 60]
        need(header[58:] == b"`\n", "ar_header")
        try:
            name = header[:16].decode("ascii").rstrip(" ")
            size_text = header[48:58].decode("ascii").strip(" ")
        except UnicodeError:
            raise Rejected("ar_header") from None
        name = name[:-1] if name.endswith("/") else name
        need(re.fullmatch(r"[0-9]+", size_text) is not None, "ar_size")
        size = int(size_text)
        start, end = pos + 60, pos + 60 + size
        need(end <= len(archive), "ar_truncated")
        members.append((name, archive[start:end]))
        pos = end
        if size % 2:
            need(pos < len(archive) and archive[pos:pos + 1] == b"\n", "ar_padding")
            pos += 1
    need(len(members) == 3 and members[0] == ("debian-binary", b"2.0\n"), "deb_version")
    need(members[1][0] in ("control.tar.xz", "control.tar.gz"), "deb_control")
    need(members[2][0] in ("data.tar.xz", "data.tar.gz"), "deb_data")
    # The control archive is intentionally never decompressed or executed.
    return members[2]


def decompress(name, data, limit=MAX_TAR):
    need(0 <= limit <= MAX_TAR and len(data) <= MAX_ARCHIVE, "compression_limit")
    is_xz = name.endswith(".xz")
    decoder = (lzma.LZMADecompressor(memlimit=64 * 1024 * 1024) if is_xz
               else zlib.decompressobj(31))
    output, pos, pending = bytearray(), 0, b""
    try:
        while not decoder.eof:
            if is_xz:
                if decoder.needs_input:
                    need(pos < len(data), "compressed_truncated")
                    chunk = data[pos:pos + 65536]
                    pos += len(chunk)
                else:
                    chunk = b""
            else:
                chunk = pending
                if not chunk:
                    need(pos < len(data), "compressed_truncated")
                    chunk = data[pos:pos + 65536]
                    pos += len(chunk)
            result = decoder.decompress(chunk, min(65536, limit - len(output) + 1))
            output.extend(result)
            need(len(output) <= limit, "expanded_limit")
            if not is_xz:
                pending = decoder.unconsumed_tail
        need(not decoder.unused_data and pos == len(data), "compressed_trailing")
    except (lzma.LZMAError, zlib.error):
        raise Rejected("compressed_invalid") from None
    return bytes(output)


def tar_string(value):
    first, _, rest = value.partition(b"\x00")
    need(not rest.strip(b"\x00"), "tar_string")
    try:
        return first.decode("utf-8", "strict")
    except UnicodeError:
        raise Rejected("tar_encoding") from None


def octal(value):
    value = value.strip(b" \x00")
    need(re.fullmatch(b"[0-7]+", value) is not None, "tar_number")
    return int(value, 8)


def selected_files(raw, specs, kinds=("elf", "notice")):
    """Return {source: bytes} for exactly the listed regular members.

    "data" (opt-in through kinds, used for OCR language models) is bounded
    like an ELF file but has no ELF header check."""
    need(len(raw) <= MAX_TAR, "expanded_limit")
    wanted, destinations = {}, set()
    for spec in specs:
        src, dst = canonical(spec["source"]), canonical(spec["destination"])
        need(src == spec["source"] and dst == spec["destination"], "manifest_path")
        need(src not in wanted and dst not in destinations, "manifest_duplicate")
        need(spec["kind"] in kinds and spec["kind"] in ("elf", "notice", "data"), "manifest_kind")
        wanted[src] = spec
        destinations.add(dst)
    need(0 < len(wanted) <= 32, "manifest_files")
    pos, seen, selected, total = 0, set(), {}, 0
    while True:
        need(pos + 512 <= len(raw), "tar_truncated")
        header = raw[pos:pos + 512]
        if header == bytes(512):
            need(pos + 1024 <= len(raw) and not raw[pos:].strip(b"\x00"), "tar_terminator")
            break
        need(len(seen) < MAX_MEMBERS, "tar_member_limit")
        need(sum(header[:148]) + 8 * 32 + sum(header[156:]) == octal(header[148:156]),
             "tar_checksum")
        need(header[257:263] in (b"ustar\x00", b"ustar ", bytes(6)), "tar_format")
        name = tar_string(header[:100])
        if header[257:263] == b"ustar\x00":
            prefix = tar_string(header[345:500])
            if prefix:
                name = prefix + "/" + name
        kind = header[156:157]
        need(kind in (b"\x00", b"0", b"1", b"2", b"5"), "tar_type")
        name = canonical(name, allow_root=kind == b"5")
        need(name not in seen, "tar_duplicate")
        seen.add(name)
        size = octal(header[124:136])
        start, end = pos + 512, pos + 512 + size
        pos = start + ((size + 511) // 512) * 512
        need(pos <= len(raw), "tar_truncated")
        need(kind in (b"\x00", b"0") or size == 0, "tar_special_payload")
        if name in wanted:
            need(kind in (b"\x00", b"0"), "selected_not_regular")
            spec = wanted[name]
            bound = MAX_FILE if spec["kind"] in ("elf", "data") else MAX_NOTICE
            total += size
            need(0 < size <= bound and total <= MAX_SELECTED, "selected_size")
            data = raw[start:end]
            if spec["kind"] == "elf":
                need(len(data) >= 64 and data[:6] == b"\x7fELF\x02\x01"
                     and data[18:20] == b"\x3e\x00", "selected_not_amd64_elf")
            selected[name] = data
        # Unselected symlinks/hardlinks are ignored, never followed/materialized.
    need(set(selected) == set(wanted), "selected_missing")
    return selected


def safe_existing(path, directory=False):
    path = Path(os.path.abspath(path))
    for part in reversed((path,) + tuple(path.parents)):
        info = part.lstat()
        need(not stat.S_ISLNK(info.st_mode), "symlink_path")
        need(stat.S_ISDIR(info.st_mode) if part != path or directory
             else stat.S_ISREG(info.st_mode), "nonregular_path")
    return path


def safe_mkdir(path):
    path = Path(os.path.abspath(path))
    for part in reversed((path,) + tuple(path.parents)):
        if not part.exists() and not part.is_symlink():
            part.mkdir(mode=0o755)
        safe_existing(part, directory=True)
    return path


def local(project, path):
    project = safe_existing(project, directory=True)
    path = Path(os.path.abspath(path))
    need(project in path.parents, "outside_project")
    current = path
    while not current.exists() and not current.is_symlink():
        current = current.parent
    safe_existing(current, directory=current != path or current.is_dir())
    return path


@contextmanager
def installation_lock(project):
    import fcntl
    tools = safe_mkdir(local(project, project / ".tools"))
    path = local(project, tools / ".media-runtime-install.lock")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        need(stat.S_ISREG(os.fstat(fd).st_mode), "nonregular_lock")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Rejected("installation_locked") from None
        yield
    finally:
        os.close(fd)


def file_identity(path, sha, size, limit=MAX_SOURCE):
    need(isinstance(sha, str) and SHA.fullmatch(sha) is not None
         and type(size) is int and 0 < size <= limit, "manifest_identity")
    path = safe_existing(path)
    checksum, total = hashlib.sha256(), 0
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        need(stat.S_ISREG(os.fstat(fd).st_mode), "nonregular_path")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            while True:
                block = stream.read(min(65536, size - total + 1))
                if not block:
                    break
                total += len(block)
                need(total <= size, "cached_identity")
                checksum.update(block)
    finally:
        os.close(fd)
    need(total == size and checksum.hexdigest() == sha, "cached_identity")


def read_verified(path, sha, size, limit=MAX_ARCHIVE):
    need(isinstance(sha, str) and SHA.fullmatch(sha) is not None
         and type(size) is int and 0 < size <= limit, "manifest_identity")
    path = safe_existing(path)
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags)
    try:
        need(stat.S_ISREG(os.fstat(fd).st_mode), "nonregular_path")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            data = stream.read(size + 1)
    finally:
        os.close(fd)
    need(len(data) == size and digest(data) == sha, "cached_identity")
    return data


def cache_name(url):
    parsed = urllib.parse.urlsplit(url)
    need(parsed.scheme == "https" and parsed.hostname and not parsed.username
         and not parsed.password and not parsed.query and not parsed.fragment, "manifest_url")
    name = PurePosixPath(parsed.path).name
    need(canonical(name) == name and "/" not in name, "manifest_url")
    return name


def validate_manifest(manifest, require_ready=False):
    runtime = manifest["mediaRuntime"]
    need(runtime["schemaVersion"] == 1 and runtime["platform"] == "linux-amd64"
         and runtime.get("optional") is True and runtime.get("experimentalOnly") is True,
         "manifest_policy")
    need(re.fullmatch(r"media-runtime/linux-amd64/[A-Za-z0-9._+-]{1,128}", runtime["installPath"])
         is not None, "manifest_install_path")
    if require_ready:
        need(runtime.get("readyToExecute") is True, "manifest_not_ready")
    packages, licenses, sources = runtime["packages"], runtime["licenseTexts"], runtime["sourceArtifacts"]
    need(len(packages) == 3 and {p["name"] for p in packages} == {"libc6", "libgcc-s1", "gcc-14-base"}
         and len(licenses) == 4 and len(sources) == 6, "manifest_packages")
    seen, libraries, notices, names = set(), set(), set(), set()
    for item in packages + licenses + sources:
        name = cache_name(item["url"])
        need(name not in names, "manifest_cache_collision")
        names.add(name)
        limit = MAX_SOURCE if item in sources else MAX_ARCHIVE if item in packages else MAX_NOTICE
        need(type(item["sizeBytes"]) is int and 0 < item["sizeBytes"] <= limit
             and isinstance(item["sha256"], str) and SHA.fullmatch(item["sha256"]) is not None,
             "manifest_identity")
    for package in packages:
        need(package["architecture"] == "amd64" and package["archive"] == "deb-ar"
             and 0 < len(package["files"]) <= 8, "manifest_package")
        for file in package["files"]:
            src, dst = canonical(file["source"]), canonical(file["destination"])
            need(src == file["source"] and dst == file["destination"] and dst not in seen,
                 "manifest_path")
            seen.add(dst)
            need(file["kind"] in ("elf", "notice"), "manifest_kind")
            (libraries if file["kind"] == "elf" else notices).add(dst)
            value = file["sha256"]
            need(value is None and not require_ready or isinstance(value, str) and SHA.fullmatch(value) is not None,
                 "manifest_file_hash")
    need(libraries == LIBRARIES and notices == {
        "licenses/runtime/libc6/copyright", "licenses/runtime/gcc-14-base/copyright"}, "manifest_closure")
    for license_text in licenses:
        dst = canonical(license_text["destination"])
        need(dst == license_text["destination"] and dst not in seen, "manifest_duplicate")
        seen.add(dst)
        notices.add(dst)
    need(notices == {"licenses/runtime/" + name for name in
         ("libc6/copyright", "gcc-14-base/copyright", "LGPL-2.1.txt", "GPL-2.0.txt", "GPL-3.0.txt", "GCC-exception-3.1.txt")},
         "manifest_notices")


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        cache_name(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class DeadlineReader(io.RawIOBase):
    """Apply the absolute deadline to every socket receive, including headers."""
    def __init__(self, raw, sock, deadline):
        self.raw, self.sock, self.deadline = raw, sock, deadline

    def readable(self):
        return True

    def readinto(self, buffer):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("download_timeout")
        self.sock.settimeout(min(30, remaining))
        count = self.raw.readinto(buffer)
        if time.monotonic() >= self.deadline:
            raise TimeoutError("download_timeout")
        return count

    def close(self):
        self.raw.close()
        super().close()


class DeadlineSocket:
    def __init__(self, sock, deadline):
        self.sock, self.deadline = sock, deadline

    def makefile(self, *args, **kwargs):
        raw = self.sock.makefile("rb", buffering=0)
        return io.BufferedReader(DeadlineReader(raw, self.sock, self.deadline), buffer_size=65536)


class DeadlineResponse(http.client.HTTPResponse):
    def __init__(self, sock, *args, deadline, **kwargs):
        super().__init__(DeadlineSocket(sock, deadline), *args, **kwargs)


class DeadlineHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, *args, deadline, **kwargs):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("download_timeout")
        kwargs["timeout"] = min(kwargs.get("timeout", 30), remaining)
        super().__init__(*args, **kwargs)
        self.response_class = lambda *a, **k: DeadlineResponse(*a, deadline=deadline, **k)


class DeadlineHTTPSHandler(urllib.request.HTTPSHandler):
    def __init__(self, deadline):
        super().__init__()
        self.deadline = deadline

    def https_open(self, request):
        def connection(*args, **kwargs):
            return DeadlineHTTPSConnection(*args, deadline=self.deadline, **kwargs)
        return self.do_open(connection, request, context=self._context)


def fetch(project, item, offline=False, limit=MAX_ARCHIVE):
    cache = safe_mkdir(local(project, project / ".tools/downloads"))
    archive = local(project, cache / cache_name(item["url"]))
    need(type(item["sizeBytes"]) is int and 0 < item["sizeBytes"] <= limit
         and isinstance(item["sha256"], str) and SHA.fullmatch(item["sha256"]) is not None,
         "manifest_identity")
    if archive.exists():
        try:
            file_identity(archive, item["sha256"], item["sizeBytes"], limit)
        except Rejected as error:
            if str(error) == "cached_identity":
                # G51.4: remove only this validated project-local bad cache file.
                local(project, archive).unlink()
            raise
        return archive
    need(not offline, "offline_missing")
    uri = item["url"]
    mirror = os.environ.get("JELEE_TOOLS_MIRROR")
    if mirror:
        uri = mirror.rstrip("/") + "/" + archive.name
    cache_name(uri)
    partial = local(project, archive.with_name(archive.name + "." + uuid.uuid4().hex + ".partial"))
    try:
        total, deadline = 0, time.monotonic() + 300
        opener = urllib.request.build_opener(HTTPSRedirect(), DeadlineHTTPSHandler(deadline))
        with opener.open(uri, timeout=30) as source, partial.open("xb") as output:
            cache_name(source.geturl())
            while True:
                need(time.monotonic() < deadline, "download_timeout")
                block = source.read1(min(65536, item["sizeBytes"] - total + 1))
                if not block:
                    break
                total += len(block)
                need(total <= item["sizeBytes"], "download_size")
                output.write(block)
        file_identity(partial, item["sha256"], item["sizeBytes"], limit)
        partial.replace(archive)
    finally:
        partial.unlink(missing_ok=True)
    return archive


def prepare(manifest, cache, install=False):
    runtime = manifest["mediaRuntime"]
    need(runtime["schemaVersion"] == 1 and runtime["platform"] == "linux-amd64", "manifest_platform")
    if install:
        need(runtime.get("readyToExecute") is True, "manifest_not_ready")
    need(0 < len(runtime["packages"]) <= 3 and len(runtime["licenseTexts"]) <= 8,
         "manifest_packages")
    cache = safe_existing(cache, directory=True)
    files, report = {}, []
    for package in runtime["packages"]:
        archive = read_verified(cache / cache_name(package["url"]), package["sha256"], package["sizeBytes"])
        name, compressed = deb_data(archive)
        selected = selected_files(decompress(name, compressed), package["files"])
        for spec in package["files"]:
            value, dst = selected[spec["source"]], spec["destination"]
            expected, actual = spec["sha256"], digest(selected[spec["source"]])
            need(expected is None and not install or isinstance(expected, str)
                 and SHA.fullmatch(expected) is not None and expected == actual, "selected_identity")
            need(dst not in files, "manifest_duplicate")
            files[dst] = (value, spec["kind"])
            need(sum(len(item[0]) for item in files.values()) <= MAX_SELECTED, "selected_size")
            report.append({"package": package["name"], "source": spec["source"],
                           "destination": dst, "sha256": actual, "sizeBytes": len(value)})
    for license_text in runtime["licenseTexts"]:
        dst = canonical(license_text["destination"])
        need(dst == license_text["destination"] and dst not in files, "manifest_duplicate")
        value = read_verified(cache / cache_name(license_text["url"]), license_text["sha256"],
                              license_text["sizeBytes"], MAX_NOTICE)
        files[dst] = (value, "notice")
    need(len(files) <= 64 and sum(len(item[0]) for item in files.values()) <= MAX_SELECTED,
         "selected_size")
    return files, report


def install_files(project, manifest, files):
    need(os.name == "posix", "linux_only")
    runtime = manifest["mediaRuntime"]
    need(runtime.get("readyToExecute") is True, "manifest_not_ready")
    relative = canonical(runtime["installPath"])
    need(relative == runtime["installPath"] and relative.startswith("media-runtime/linux-amd64/"),
         "manifest_install_path")
    project = safe_existing(project, directory=True)
    tools = safe_mkdir(local(project, project / ".tools"))
    target = local(project, tools / relative)
    safe_mkdir(target.parent)
    need(not target.exists() and not target.is_symlink(), "installation_exists")
    staging = None
    try:
        staging = Path(tempfile.mkdtemp(prefix=".runtime-stage-", dir=tools))
        for destination, (value, kind) in files.items():
            need(canonical(destination) == destination, "manifest_path")
            output = staging / destination
            output.parent.mkdir(parents=True, exist_ok=True)
            with output.open("xb") as stream:
                stream.write(value)
            output.chmod(0o555 if kind == "elf" else 0o444)
        record = {"schemaVersion": 1, "runtimeManifestSha256": runtime_identity(manifest),
                  "files": {name: digest(value[0]) for name, value in files.items()},
                  "executed": False}
        (staging / "installed.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
        # Locked publishers never overwrite an existing installation.
        need(not target.exists() and not target.is_symlink(), "installation_exists")
        staging.rename(target)
        staging = None
    finally:
        if staging is not None:
            shutil.rmtree(staging)
    return target


def runtime_identity(manifest):
    return digest(json.dumps(manifest["mediaRuntime"], sort_keys=True, separators=(",", ":")).encode())


def verify_install(project, manifest, files):
    target = local(project, project / ".tools" / canonical(manifest["mediaRuntime"]["installPath"]))
    safe_existing(target, directory=True)
    record_path = local(project, target / "installed.json")
    record = json.loads(read_verified_manifest(record_path))
    expected = {name: digest(value[0]) for name, value in files.items()}
    need(record == {"schemaVersion": 1, "runtimeManifestSha256": runtime_identity(manifest),
                    "files": expected, "executed": False}, "installed_record")
    actual = set()
    for parent, directories, names in os.walk(target, followlinks=False):
        for directory in directories:
            safe_existing(Path(parent) / directory, directory=True)
        for name in names:
            path = local(project, Path(parent) / name)
            safe_existing(path)
            actual.add(path.relative_to(target).as_posix())
    need(actual == set(files) | {"installed.json"}, "installed_inventory")
    for name, (value, kind) in files.items():
        path = local(project, target / name)
        file_identity(path, digest(value), len(value), MAX_FILE)
        # WSL DrvFS may synthesize execute bits for read-only text files. They
        # are never executed here; the container build sets final notice 0444.
        allowed_modes = (0o555,) if kind == "elf" else (0o444, 0o555)
        need(path.stat().st_mode & 0o7777 in allowed_modes, "installed_mode")
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("inspect", "bootstrap", "verify", "sources"))
    parser.add_argument("--offline", action="store_true")
    args = parser.parse_args()
    try:
        need(platform.system() == "Linux" and platform.machine() == "x86_64", "linux_amd64_only")
        manifest = json.loads(read_verified_manifest(local(ROOT, ROOT / "tools/manifest.json")))
        runtime = manifest["mediaRuntime"]
        validate_manifest(manifest, args.command in ("bootstrap", "verify"))
        with installation_lock(ROOT):
            if args.command == "sources":
                need(0 < len(runtime["sourceArtifacts"]) <= 16, "manifest_sources")
                for source in runtime["sourceArtifacts"]:
                    fetch(ROOT, source, args.offline, MAX_SOURCE)
                print("Verified pinned Debian runtime source archives; signatures and BtbN static dependency source completeness remain separate.")
                return
            for item in runtime["packages"] + runtime["licenseTexts"]:
                fetch(ROOT, item, args.offline or args.command == "verify")
            files, report = prepare(manifest, ROOT / ".tools/downloads", args.command != "inspect")
            if args.command == "inspect":
                print(json.dumps({"command": args.command, "executed": False, "files": report}, indent=2))
                return
            target = local(ROOT, ROOT / ".tools" / canonical(runtime["installPath"]))
            if args.command == "bootstrap" and not target.exists():
                install_files(ROOT, manifest, files)
            verify_install(ROOT, manifest, files)
            print("Verified Linux amd64 runtime: 8 ELF files, package notices and licenses; no executable run.")
    except (Rejected, OSError, ValueError, KeyError, TypeError):
        parser.exit(1, "runtime-tools: rejected input or unavailable dependency; no executable was run\n")


def read_verified_manifest(path):
    path = safe_existing(path)
    with path.open("rb") as stream:
        data = stream.read(256 * 1024 + 1)
    need(len(data) <= 256 * 1024, "manifest_size")
    return data


if __name__ == "__main__":
    main()
