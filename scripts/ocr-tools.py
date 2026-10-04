#!/usr/bin/env python3
"""Optional pinned Tesseract OCR runtime for subtitle OCR (G15.6, G30.6, G51).

Never installed by default. Only the Debian packages pinned by version, URL,
size and SHA256 in tools/manifest.json ("ocrTools") are downloaded, over
credential-free HTTPS; only the listed files are taken from each package's
data archive (the control archive and maintainer scripts are never unpacked
or executed). The version check runs the installed, hash-verified executable
with fixed arguments only. Linux amd64 only: upstream Tesseract publishes no
portable Linux binary and no portable Windows archive.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import uuid
from datetime import datetime, timezone
from urllib.parse import urlparse
import urllib.request

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parent.parent
TOOL = "tesseract"
PLATFORM = "linux-amd64"
LANGUAGES = ("eng", "chi_tra", "chi_sim", "jpn")
SHA = re.compile(r"[0-9a-f]{64}\Z")
KINDS = ("elf", "executable", "notice", "data")
MAX_PACKAGE = 16 * 1024 * 1024
MAX_PACKAGES = 64

_spec = importlib.util.spec_from_file_location("runtime_tools", Path(__file__).with_name("runtime-tools.py"))
runtime_tools = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(runtime_tools)


class Rejected(ValueError):
    pass


def need(condition, code):
    if not condition:
        raise Rejected(code)


def target():
    need(platform.system() == "Linux" and platform.machine().lower() == "x86_64",
         "the OCR runtime supports Linux amd64 only")
    return PLATFORM


def relative(value):
    return (isinstance(value, str) and 0 < len(value) <= 512 and "\\" not in value and ":" not in value
            and not value.startswith("/") and "\x00" not in value
            and all(part not in ("", ".", "..") for part in value.split("/")))


def https(url):
    parsed = urlparse(url)
    return (parsed.scheme == "https" and bool(parsed.netloc) and not parsed.username and not parsed.password
            and not parsed.query and not parsed.fragment)


def load():
    manifest = json.loads((ROOT / "tools/manifest.json").read_text(encoding="utf-8"))
    need(manifest["schemaVersion"] == 1, "manifest_schema")
    section = manifest["ocrTools"]
    need(section["schemaVersion"] == 1 and section["optional"] is True and set(section["tools"]) == {TOOL},
         "manifest_policy")
    packages = section["packages"]
    need(0 < len(packages) <= MAX_PACKAGES, "manifest_packages")
    destinations = set()
    for package in packages:
        need(https(package["url"]) and SHA.fullmatch(package["sha256"]) is not None
             and type(package["sizeBytes"]) is int and 0 < package["sizeBytes"] <= MAX_PACKAGE
             and package["archive"] == "deb-ar" and package["architecture"] in ("amd64", "all")
             and 0 < len(package["files"]) <= 4, "manifest_package")
        for file in package["files"]:
            need(relative(file["source"]) and relative(file["destination"]) and file["kind"] in KINDS
                 and SHA.fullmatch(file["sha256"]) is not None and type(file["sizeBytes"]) is int
                 and 0 < file["sizeBytes"] and file["destination"] not in destinations, "manifest_package_file")
            destinations.add(file["destination"])
    entry = section["tools"][TOOL]
    spec = entry["platforms"][PLATFORM]
    need(spec["installPath"] == "ocr/" + TOOL + "/" + entry["debianVersion"] + "/" + PLATFORM, "manifest_install_path")
    executables = [f for p in packages for f in p["files"] if f["kind"] == "executable"]
    need(len(executables) == 1 and executables[0]["destination"] == spec["executable"]["path"], "manifest_executable")
    languages = {f["destination"] for p in packages for f in p["files"] if f["kind"] == "data"}
    need(languages == {spec["tessdataPath"] + "/" + code + ".traineddata" for code in LANGUAGES}
         and set(spec["languages"]) == set(LANGUAGES), "manifest_languages")
    return entry, spec, packages


def cache_path(url):
    name = PurePosixPath(urlparse(url).path).name
    need(relative(name) and "/" not in name, "manifest_url")
    return runtime_tools.local(ROOT, ROOT / ".tools/downloads" / name)


def download(item, offline):
    runtime_tools.safe_mkdir(runtime_tools.local(ROOT, ROOT / ".tools/downloads"))
    archive = cache_path(item["url"])
    if archive.exists():
        try:
            runtime_tools.file_identity(archive, item["sha256"], item["sizeBytes"], MAX_PACKAGE)
        except runtime_tools.Rejected:
            # Only this validated project-local cache file is removed.
            archive.unlink()
            raise Rejected("cached package differs from manifest; removed, bootstrap again") from None
        return archive
    need(not offline, "offline package is missing: " + archive.name)
    uri = item["url"]
    mirror = os.environ.get("JELEE_TOOLS_MIRROR")
    if mirror:
        uri = mirror.rstrip("/") + "/" + archive.name
    need(https(uri), "sources and mirrors must use credential-free HTTPS")
    partial = runtime_tools.local(ROOT, archive.with_name(archive.name + "." + uuid.uuid4().hex + ".partial"))
    try:
        print("Downloading " + archive.name, flush=True)
        opener = urllib.request.build_opener(runtime_tools.HTTPSRedirect())
        with opener.open(uri, timeout=60) as source, partial.open("xb") as output:
            total = 0
            while True:
                chunk = source.read(min(1024 * 1024, item["sizeBytes"] - total + 1))
                if not chunk:
                    break
                total += len(chunk)
                need(total <= item["sizeBytes"], "download exceeds manifest size")
                output.write(chunk)
        runtime_tools.file_identity(partial, item["sha256"], item["sizeBytes"], MAX_PACKAGE)
        partial.replace(archive)
    finally:
        partial.unlink(missing_ok=True)
    return archive


def read_packages(packages, offline):
    """Return {destination: bytes} for exactly the pinned package files."""
    selected = {}
    for package in packages:
        archive = download(package, offline)
        raw = runtime_tools.read_verified(archive, package["sha256"], package["sizeBytes"])
        member, compressed = runtime_tools.deb_data(raw)
        # The executable is an ELF file; the selection only knows elf/notice/data.
        specs = [dict(file, kind="elf" if file["kind"] == "executable" else file["kind"]) for file in package["files"]]
        files = runtime_tools.selected_files(runtime_tools.decompress(member, compressed), specs,
                                             kinds=("elf", "notice", "data"))
        for file in package["files"]:
            data = files[file["source"]]
            need(hashlib.sha256(data).hexdigest() == file["sha256"] and len(data) == file["sizeBytes"],
                 "package_file_identity:" + file["source"])
            selected[file["destination"]] = data
    return selected


def layout(packages, files):
    kinds = {f["destination"]: f["kind"] for p in packages for f in p["files"]}
    return {path: {"sha256": hashlib.sha256(data).hexdigest(), "executable": kinds[path] in ("elf", "executable")}
            for path, data in files.items()}


def paths(spec):
    install = runtime_tools.local(ROOT, ROOT / ".tools" / spec["installPath"])
    record = runtime_tools.local(ROOT, ROOT / ".tools/ocr-installed" / (TOOL + "-" + PLATFORM + ".json"))
    return install, record


def install(spec, packages, files):
    install_dir, _ = paths(spec)
    runtime_tools.safe_mkdir(install_dir.parent)
    need(not install_dir.exists() and not install_dir.is_symlink(), "installation_exists")
    staging = Path(tempfile.mkdtemp(prefix="." + TOOL + "-stage-", dir=install_dir.parent))
    try:
        expected = layout(packages, files)
        for path, data in files.items():
            output = staging / PurePosixPath(path)
            output.parent.mkdir(parents=True, exist_ok=True)
            with output.open("xb") as stream:
                stream.write(data)
            output.chmod(0o555 if expected[path]["executable"] else 0o444)
        staging.rename(install_dir)
        staging = None
    finally:
        if staging is not None:
            shutil.rmtree(staging)


def check_install(packages, install_dir, files):
    expected = layout(packages, files)
    found = set()
    for parent, _, names in os.walk(install_dir, followlinks=False):
        for name in names:
            path = Path(parent) / name
            need(not path.is_symlink() and path.is_file(), "installed_nonregular")
            found.add(path.relative_to(install_dir).as_posix())
    need(found == set(expected), "installed inventory differs from manifest; remove the installation and bootstrap again")
    for path, item in expected.items():
        try:
            runtime_tools.file_identity(install_dir / PurePosixPath(path), item["sha256"], len(files[path]))
        except runtime_tools.Rejected:
            raise Rejected("installed file differs from manifest: " + path + "; remove the installation and bootstrap again") from None


def tool_environment(install_dir):
    # Developer runs use this host's glibc (libresolv included: it must match
    # the host libc); the production sandbox uses the pinned mediaRuntime.
    env = {key: value for key, value in os.environ.items() if not key.startswith(("LD_", "TESS", "OMP_"))}
    env.update({"LANG": "C", "LC_ALL": "C", "TZ": "UTC", "OMP_THREAD_LIMIT": "1",
                "LD_LIBRARY_PATH": str(install_dir / "lib")})
    return env


def check_version(spec, install_dir):
    executable = install_dir / PurePosixPath(spec["executable"]["path"])
    with subprocess.Popen([str(executable)] + list(spec["versionArguments"]), env=tool_environment(install_dir),
                          stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as process:
        try:
            output = process.stdout.read(65537)
            need(len(output) <= 65536 and process.wait(timeout=20) == 0, "version check failed for tesseract")
        finally:
            if process.poll() is None:
                process.kill()
    lines = output.decode("utf-8", "replace").replace("\r", "").split("\n")
    need(spec["versionLine"] in lines, "installed tesseract version differs from manifest")


def verify(entry, spec, packages):
    files = read_packages(packages, True)
    install_dir, record = paths(spec)
    need(record.is_file(), "tesseract is not installed; run scripts/ocr-tools.py bootstrap")
    installed = json.loads(record.read_text(encoding="utf-8"))
    need(installed.get("schemaVersion") == 1 and installed.get("version") == entry["debianVersion"]
         and installed.get("files") == layout(packages, files),
         "installation record differs from manifest; remove it and bootstrap again")
    check_install(packages, install_dir, files)
    check_version(spec, install_dir)
    print("Verified tesseract " + entry["debianVersion"] + " " + PLATFORM + " with " + ", ".join(LANGUAGES)
          + "; package, executable, library, language data and notice SHA256 match")


def bootstrap(entry, spec, packages, offline):
    files = read_packages(packages, offline)
    install_dir, record = paths(spec)
    if not install_dir.exists():
        install(spec, packages, files)
    # Never silently bless or repair changed installed bytes.
    check_install(packages, install_dir, files)
    check_version(spec, install_dir)
    runtime_tools.safe_mkdir(record.parent)
    data = {"schemaVersion": 1, "tool": TOOL, "platform": PLATFORM, "version": entry["debianVersion"],
            "files": layout(packages, files), "installedAt": datetime.now(timezone.utc).isoformat()}
    partial = record.with_name(record.name + "." + uuid.uuid4().hex + ".partial")
    try:
        partial.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
        partial.replace(record)
    finally:
        partial.unlink(missing_ok=True)
    bin_dir = runtime_tools.safe_mkdir(runtime_tools.local(ROOT, ROOT / ".bin"))
    wrapper = runtime_tools.local(ROOT, bin_dir / TOOL)
    wrapper.write_text('#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                       'exec python3 "$root/scripts/ocr-tools.py" run "$@"\n', encoding="utf-8")
    wrapper.chmod(0o755)
    verify(entry, spec, packages)


def run_tool(args):
    target()
    entry, spec, packages = load()
    files = read_packages(packages, True)
    install_dir, _ = paths(spec)
    check_install(packages, install_dir, files)
    executable = str(install_dir / PurePosixPath(spec["executable"]["path"]))
    # No shell and no PATH fallback. A developer wrapper, not the production
    # sandbox; --tessdata-dir must be passed by the developer.
    os.execve(executable, [executable] + args, tool_environment(install_dir))


def lock():
    import fcntl
    path = runtime_tools.local(ROOT, ROOT / ".tools/.ocr-install.lock")
    runtime_tools.safe_mkdir(path.parent)
    handle = path.open("a")
    try:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        handle.close()
        raise Rejected("another OCR bootstrap is running") from None
    return handle


def main():
    if len(sys.argv) >= 2 and sys.argv[1] == "run":
        run_tool(sys.argv[2:])
        return
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=["bootstrap", "verify"])
    parser.add_argument("--offline", action="store_true")
    parser.add_argument("--if-installed", action="store_true",
                        help="verify only when installed and otherwise report it as skipped")
    args = parser.parse_args()
    if args.if_installed and platform.system() != "Linux":
        print("Skipped optional tesseract: Linux amd64 only")
        return
    target()
    entry, spec, packages = load()
    with lock():
        if args.command == "bootstrap":
            bootstrap(entry, spec, packages, args.offline)
        elif args.if_installed and not paths(spec)[1].is_file():
            print("Skipped optional tesseract: not installed (scripts/ocr-tools.py bootstrap)")
        else:
            verify(entry, spec, packages)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as error:
        print("ocr-tools: " + str(error), file=sys.stderr)
        sys.exit(1)
