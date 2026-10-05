#!/usr/bin/env python3
"""Optional pinned mkvtoolnix and MediaInfo CLI tools (E4, G30.6, G51).

Never installed by default. Only HTTPS downloads pinned by SHA256 in
tools/manifest.json ("matroskaTools") are accepted; only the listed files are
taken from each archive (zip members, AppImage SquashFS files, Debian package
files) and nothing inside an archive is executed during installation. The
version check runs the installed, hash-verified executables with fixed
arguments only.
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
import zipfile
from datetime import datetime, timezone
from urllib.parse import urlparse
import urllib.request

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent))
import squashfs_reader  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
TOOLS = ("mkvtoolnix", "mediainfo")
EXECUTABLES = {"mkvtoolnix": {"mkvmerge", "mkvextract", "mkvpropedit"}, "mediainfo": {"mediainfo"}}
PRODUCTION = {"mkvmerge", "mkvextract", "mediainfo"}
SHA = re.compile(r"[0-9a-f]{64}\Z")
MAX_ARCHIVE = 256 * 1024 * 1024
MAX_FILE = 64 * 1024 * 1024
MAX_NOTICE = 1024 * 1024
MAX_SELECTED = 192 * 1024 * 1024

_spec = importlib.util.spec_from_file_location("runtime_tools", Path(__file__).with_name("runtime-tools.py"))
runtime_tools = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(runtime_tools)


class Rejected(ValueError):
    pass


def need(condition, code):
    if not condition:
        raise Rejected(code)


def target():
    system, machine = platform.system(), platform.machine().lower()
    if system == "Linux" and machine == "x86_64":
        return "linux-amd64"
    if system == "Windows" and machine in ("amd64", "x86_64"):
        return "windows-amd64"
    raise Rejected("matroska tools support Linux amd64 and Windows amd64 only")


def relative(value):
    return (isinstance(value, str) and 0 < len(value) <= 512 and "\\" not in value and ":" not in value
            and not value.startswith("/") and "\x00" not in value
            and all(part not in ("", ".", "..") for part in value.split("/")))


def https(url):
    parsed = urlparse(url)
    return (parsed.scheme == "https" and bool(parsed.netloc) and not parsed.username and not parsed.password
            and not parsed.query and not parsed.fragment)


def identity(item, limit):
    need(https(item["url"]) and isinstance(item["sha256"], str) and SHA.fullmatch(item["sha256"]) is not None
         and type(item["sizeBytes"]) is int and 0 < item["sizeBytes"] <= limit, "manifest_identity")


def pinned(item, limit=MAX_FILE):
    need(relative(item["path"]) and isinstance(item["sha256"], str) and SHA.fullmatch(item["sha256"]) is not None
         and type(item["sizeBytes"]) is int and 0 < item["sizeBytes"] <= limit, "manifest_file")


def load():
    manifest = json.loads((ROOT / "tools/manifest.json").read_text(encoding="utf-8"))
    need(manifest["schemaVersion"] == 1, "manifest_schema")
    section = manifest["matroskaTools"]
    need(section["schemaVersion"] == 1 and section["optional"] is True and set(section["tools"]) == set(TOOLS),
         "manifest_policy")
    packages = {}
    for package in section["runtimePackages"]:
        identity(package, 16 * 1024 * 1024)
        need(package["archive"] == "deb-ar" and package["architecture"] == "amd64" and 0 < len(package["files"]) <= 4,
             "manifest_package")
        for file in package["files"]:
            need(relative(file["source"]) and relative(file["destination"]) and file["kind"] in ("elf", "notice")
                 and SHA.fullmatch(file["sha256"]) is not None, "manifest_package_file")
        packages[package["name"]] = package
    return section, packages


def spec_for(section, tool, platform_name):
    entry = section["tools"][tool]
    need(re.fullmatch(r"[0-9]+\.[0-9]+(\.[0-9]+)?", entry["version"]) is not None, "manifest_version")
    spec = entry["platforms"][platform_name]
    identity(spec, MAX_ARCHIVE)
    need(spec["archive"] in ("zip", "appimage-squashfs"), "manifest_archive")
    need(spec["installPath"] == "matroska/" + tool + "/" + entry["version"] + "/" + platform_name, "manifest_install_path")
    need(set(spec["executables"]) == EXECUTABLES[tool], "manifest_executables")
    for name, item in spec["executables"].items():
        pinned(item)
        need(item["productionAllowed"] is (name in PRODUCTION), "manifest_production_policy")
    for item in spec["libraries"]:
        pinned(item)
    for item in spec.get("licenseFiles", []):
        pinned(item, MAX_NOTICE)
    for item in spec.get("licenseTexts", []):
        identity(item, MAX_NOTICE)
        need(relative(item["destination"]), "manifest_license_text")
    need(spec.get("licenseFiles") or spec.get("licenseTexts"), "manifest_license_missing")
    if spec["archive"] == "appimage-squashfs":
        need(type(spec["squashfsOffset"]) is int and 0 < spec["squashfsOffset"] < 1024 * 1024, "manifest_offset")
    return entry, spec


def cache_path(url, name=None):
    name = name or PurePosixPath(urlparse(url).path).name
    need(relative(name) and "/" not in name, "manifest_url")
    return runtime_tools.local(ROOT, ROOT / ".tools/downloads" / name)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def download(item, offline, limit=MAX_ARCHIVE):
    runtime_tools.safe_mkdir(runtime_tools.local(ROOT, ROOT / ".tools/downloads"))
    archive = cache_path(item["url"], item.get("cacheName"))
    if archive.exists():
        try:
            runtime_tools.file_identity(archive, item["sha256"], item["sizeBytes"], limit)
        except runtime_tools.Rejected:
            # Only this validated project-local cache file is removed.
            archive.unlink()
            raise Rejected("cached archive differs from manifest; removed, bootstrap again") from None
        return archive
    need(not offline, "offline archive is missing: " + archive.name)
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
        runtime_tools.file_identity(partial, item["sha256"], item["sizeBytes"], limit)
        partial.replace(archive)
    finally:
        partial.unlink(missing_ok=True)
    return archive


def wanted(spec):
    files = {}
    for name, item in spec["executables"].items():
        files[item["path"]] = (item, "executable")
    for item in spec["libraries"]:
        files[item["path"]] = (item, "library")
    for item in spec.get("licenseFiles", []):
        files[item["path"]] = (item, "license")
    return files


def read_archive(spec, archive):
    """Return {path: bytes} for exactly the pinned archive members."""
    files = wanted(spec)
    selected = {}
    if spec["archive"] == "zip":
        with zipfile.ZipFile(archive) as bundle:
            need(len(bundle.infolist()) <= 20000, "archive_member_limit")
            members = {}
            for info in bundle.infolist():
                need(info.filename not in members, "archive_duplicate_member")
                members[info.filename] = info
            for path, (item, _) in files.items():
                info = members.get(path)
                need(info is not None and not info.is_dir() and info.file_size == item["sizeBytes"], "archive_member:" + path)
                with bundle.open(info) as stream:
                    data = stream.read(item["sizeBytes"] + 1)
                selected[path] = data
    else:
        data = archive.read_bytes()
        image = squashfs_reader.SquashFS(data, spec["squashfsOffset"])
        for path in files:
            selected[path] = image.read(path)
    total = 0
    for path, (item, _) in files.items():
        data = selected[path]
        total += len(data)
        need(len(data) == item["sizeBytes"] and digest(data) == item["sha256"], "archive_file_identity:" + path)
    need(total <= MAX_SELECTED, "selected_size")
    return selected


def read_packages(spec, packages, offline):
    """Return {runtime/<destination>: bytes} from the pinned Debian packages."""
    selected = {}
    for name in spec.get("runtimePackages", []):
        package = packages[name]
        archive = download(package, offline, 16 * 1024 * 1024)
        raw = runtime_tools.read_verified(archive, package["sha256"], package["sizeBytes"])
        member, compressed = runtime_tools.deb_data(raw)
        files = runtime_tools.selected_files(runtime_tools.decompress(member, compressed), package["files"])
        for file in package["files"]:
            data = files[file["source"]]
            need(digest(data) == file["sha256"] and len(data) == file["sizeBytes"], "package_file_identity")
            selected["runtime/" + file["destination"]] = data
    return selected


def read_license_texts(spec, offline):
    selected = {}
    for item in spec.get("licenseTexts", []):
        archive = download(item, offline, MAX_NOTICE)
        selected[item["destination"]] = runtime_tools.read_verified(archive, item["sha256"], item["sizeBytes"], MAX_NOTICE)
    return selected


def paths(tool, platform_name, spec):
    install = runtime_tools.local(ROOT, ROOT / ".tools" / spec["installPath"])
    record = runtime_tools.local(ROOT, ROOT / ".tools/matroska-installed" / (tool + "-" + platform_name + ".json"))
    return install, record


def expected_files(spec, files):
    executables = {item["path"] for item in spec["executables"].values()}
    return {path: {"sha256": digest(data), "executable": path in executables or path.endswith(".so") or ".so." in path}
            for path, data in files.items()}


def install(tool, platform_name, spec, files):
    install_dir, _ = paths(tool, platform_name, spec)
    runtime_tools.safe_mkdir(install_dir.parent)
    need(not install_dir.exists() and not install_dir.is_symlink(), "installation_exists")
    staging = Path(tempfile.mkdtemp(prefix="." + tool + "-stage-", dir=install_dir.parent))
    try:
        layout = expected_files(spec, files)
        for path, data in files.items():
            output = staging / PurePosixPath(path)
            output.parent.mkdir(parents=True, exist_ok=True)
            with output.open("xb") as stream:
                stream.write(data)
            output.chmod(0o555 if layout[path]["executable"] else 0o444)
        staging.rename(install_dir)
        staging = None
    finally:
        if staging is not None:
            shutil.rmtree(staging)


def check_install(spec, install_dir, files):
    layout = expected_files(spec, files)
    found = set()
    for parent, _, names in os.walk(install_dir, followlinks=False):
        for name in names:
            path = Path(parent) / name
            need(not path.is_symlink() and path.is_file(), "installed_nonregular")
            found.add(path.relative_to(install_dir).as_posix())
    need(found == set(layout), "installed inventory differs from manifest; remove the installation and bootstrap again")
    for path, item in layout.items():
        try:
            runtime_tools.file_identity(install_dir / PurePosixPath(path), item["sha256"], len(files[path]), MAX_ARCHIVE)
        except runtime_tools.Rejected:
            raise Rejected("installed file differs from manifest: " + path + "; remove the installation and bootstrap again") from None


def tool_environment(spec, install_dir):
    env = {key: value for key, value in os.environ.items() if not key.startswith("LD_")}
    env.update({"LANG": "C", "LC_ALL": "C", "TZ": "UTC"})
    if os.name == "posix":
        directories = sorted({str(install_dir / PurePosixPath(item["path"]).parent) for item in spec["libraries"]})
        runtime_dir = install_dir / "runtime/lib/x86_64-linux-gnu"
        if runtime_dir.is_dir():
            directories.append(str(runtime_dir))
        if directories:
            env["LD_LIBRARY_PATH"] = ":".join(directories)
    return env


def runtime_loader():
    # The pinned Debian 13 glibc from make bootstrap-runtime. The bundled
    # libstdc++ needs glibc >= 2.38, so a host loader on Debian 12 or Ubuntu
    # 22.04 cannot start the tools; the image uses this same glibc.
    source = ROOT / "tools/manifest.json"
    if os.name != "posix" or not source.is_file():
        return None
    manifest = json.loads(source.read_text(encoding="utf-8"))
    base = ROOT / ".tools" / PurePosixPath(manifest["mediaRuntime"]["installPath"])
    loader = base / "lib64/ld-linux-x86-64.so.2"
    if not (base / "installed.json").is_file() or not loader.is_file():
        return None
    return loader, base / "lib/x86_64-linux-gnu"


def tool_command(spec, install_dir, executable, args):
    env = tool_environment(spec, install_dir)
    runtime = runtime_loader()
    if runtime is None:
        return [str(executable)] + list(args), env
    loader, libc_dir = runtime
    directories = env.pop("LD_LIBRARY_PATH", "").split(":") + [str(libc_dir)]
    return [str(loader), "--library-path", ":".join(d for d in directories if d), "--argv0", str(executable),
            str(executable)] + list(args), env


def check_version(spec, install_dir, name):
    executable = install_dir / PurePosixPath(spec["executables"][name]["path"])
    command, env = tool_command(spec, install_dir, executable, spec["versionArguments"])
    with subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE) as process:
        try:
            output = process.stdout.read(65537)
            status = process.wait(timeout=20)
            detail = process.stderr.read(4096).decode("utf-8", "replace").strip()
            hint = "" if runtime_loader() else "; run make bootstrap-runtime first (host glibc may be older than 2.38)"
            need(len(output) <= 65536 and status == 0,
                 "version check failed for " + name + (": " + detail if detail else "") + hint)
        finally:
            if process.poll() is None:
                process.kill()
    lines = output.decode("utf-8", "replace").replace("\r", "").split("\n")
    need(spec["versionLine"].replace("{name}", name) in lines, "installed " + name + " version differs from manifest")


def collect(tool, platform_name, section, packages, offline):
    entry, spec = spec_for(section, tool, platform_name)
    archive = download(spec, offline)
    files = read_archive(spec, archive)
    files.update(read_license_texts(spec, offline))
    if platform_name == "linux-amd64":
        files.update(read_packages(spec, packages, offline))
    return entry, spec, files


def verify_tool(tool, platform_name, section, packages):
    entry, spec, files = collect(tool, platform_name, section, packages, True)
    install_dir, record = paths(tool, platform_name, spec)
    need(record.is_file(), tool + " is not installed; run scripts/matroska-tools.py bootstrap --tool " + tool)
    installed = json.loads(record.read_text(encoding="utf-8"))
    need(installed.get("schemaVersion") == 1 and installed.get("version") == entry["version"]
         and installed.get("archiveSHA256") == spec["sha256"] and installed.get("files") == expected_files(spec, files),
         "installation record differs from manifest; remove it and bootstrap again")
    check_install(spec, install_dir, files)
    for name in sorted(spec["executables"]):
        check_version(spec, install_dir, name)
    print("Verified " + tool + " " + entry["version"] + " " + platform_name + "; archive, executable, library and license SHA256 match")


def bootstrap_tool(tool, platform_name, section, packages, offline):
    entry, spec, files = collect(tool, platform_name, section, packages, offline)
    install_dir, record = paths(tool, platform_name, spec)
    if not install_dir.exists():
        install(tool, platform_name, spec, files)
    # Never silently bless or repair changed installed bytes.
    check_install(spec, install_dir, files)
    for name in sorted(spec["executables"]):
        check_version(spec, install_dir, name)
    runtime_tools.safe_mkdir(record.parent)
    data = {"schemaVersion": 1, "tool": tool, "platform": platform_name, "version": entry["version"],
            "archiveSHA256": spec["sha256"], "files": expected_files(spec, files),
            "installedAt": datetime.now(timezone.utc).isoformat()}
    partial = record.with_name(record.name + "." + uuid.uuid4().hex + ".partial")
    try:
        partial.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
        partial.replace(record)
    finally:
        partial.unlink(missing_ok=True)
    if os.name == "posix":
        bin_dir = runtime_tools.safe_mkdir(runtime_tools.local(ROOT, ROOT / ".bin"))
        for name in sorted(spec["executables"]):
            wrapper = runtime_tools.local(ROOT, bin_dir / name)
            wrapper.write_text('#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                               'exec python3 "$root/scripts/matroska-tools.py" run ' + name + ' "$@"\n', encoding="utf-8")
            wrapper.chmod(0o755)
    verify_tool(tool, platform_name, section, packages)


def installed(tool, platform_name, section):
    _, spec = spec_for(section, tool, platform_name)
    return paths(tool, platform_name, spec)[1].is_file()


def run_tool(name, args):
    tool = next((t for t, names in EXECUTABLES.items() if name in names), None)
    need(tool is not None, "unknown matroska tool")
    platform_name = target()
    section, packages = load()
    entry, spec, files = collect(tool, platform_name, section, packages, True)
    install_dir, _ = paths(tool, platform_name, spec)
    check_install(spec, install_dir, files)
    executable = str(install_dir / PurePosixPath(spec["executables"][name]["path"]))
    # No shell and no PATH fallback. A developer wrapper, not the production
    # sandbox; the media file is whatever the developer passes.
    command, env = tool_command(spec, install_dir, executable, args)
    if os.name == "posix":
        os.execve(command[0], command, env)
    sys.exit(subprocess.run(command, env=env, check=False).returncode)


def lock():
    path = runtime_tools.local(ROOT, ROOT / ".tools/.matroska-install.lock")
    runtime_tools.safe_mkdir(path.parent)
    handle = path.open("a")
    try:
        if os.name == "posix":
            import fcntl
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        else:
            import msvcrt
            msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
    except OSError:
        handle.close()
        raise Rejected("another matroska bootstrap is running") from None
    return handle


def main():
    if len(sys.argv) >= 3 and sys.argv[1] == "run":
        run_tool(sys.argv[2], sys.argv[3:])
        return
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=["bootstrap", "verify"])
    parser.add_argument("--tool", action="append", choices=TOOLS, help="repeatable; default: both")
    parser.add_argument("--offline", action="store_true")
    parser.add_argument("--if-installed", action="store_true",
                        help="verify only tools that are installed and report the others as skipped")
    args = parser.parse_args()
    platform_name = target()
    section, packages = load()
    selected = tuple(args.tool or TOOLS)
    with lock():
        for tool in selected:
            if args.command == "bootstrap":
                bootstrap_tool(tool, platform_name, section, packages, args.offline)
            elif args.if_installed and not installed(tool, platform_name, section):
                print("Skipped optional " + tool + ": not installed (scripts/matroska-tools.py bootstrap --tool " + tool + ")")
            else:
                verify_tool(tool, platform_name, section, packages)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        print("matroska-tools: " + str(error), file=sys.stderr)
        sys.exit(1)
