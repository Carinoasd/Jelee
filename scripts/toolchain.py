#!/usr/bin/env python3
"""Project-local Linux Go bootstrap; only Python's standard library is required."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import subprocess
import sys
import tarfile
from datetime import datetime, timezone
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, build_opener

ROOT = Path(__file__).resolve().parent.parent


def local_path(root, path):
    root, path = Path(os.path.abspath(root)), Path(os.path.abspath(path))
    if path == root or root not in path.parents:
        raise ValueError("path must remain below project directory")
    current = path
    while current != root.parent:
        if current.is_symlink():
            raise ValueError("linked tool paths are forbidden: " + str(current))
        current = current.parent
    return path


def selected():
    manifest = json.loads((ROOT / "tools/manifest.json").read_text(encoding="utf-8"))
    if manifest["schemaVersion"] != 1:
        raise ValueError("unsupported manifest schema")
    tool, = [tool for tool in manifest["tools"] if tool["name"] == "go"]
    if not re.fullmatch(r"\d+\.\d+\.\d+", tool["version"]):
        raise ValueError("invalid version")
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None:
        raise ValueError("POSIX bootstrap supports Linux amd64/arm64; use bootstrap-tools.ps1 on Windows")
    target = "linux-" + arch
    spec = tool["platforms"][target]
    if (urlparse(spec["url"]).scheme != "https" or not re.fullmatch(r"[a-f0-9]{64}", spec["sha256"])
            or spec["archive"] != "tar.gz" or spec["installPath"] != "go/" + tool["version"] + "/" + target
            or spec["executable"] != "go/bin/go"):
        raise ValueError("invalid Go source, hash or installation layout")
    return tool, spec, target


def digest(path):
    result = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def assert_hash(path, expected):
    if digest(path) != expected:
        raise ValueError("SHA256 mismatch for " + Path(path).name)


def safe_extract(archive, destination, mode="r:gz"):
    destination = local_path(ROOT, destination)
    if any(destination.iterdir()):
        raise ValueError("extraction staging directory must be empty")
    # Archive links are forbidden and files use exclusive creation. Check a
    # directory when first creating it; repeated ancestor stats make /mnt/c
    # extraction needlessly slow. Only this installer populates staging.
    checked_dirs = {destination}

    def ensure_directory(directory):
        if directory in checked_dirs:
            return
        ensure_directory(directory.parent)
        try:
            directory.mkdir()
        except FileExistsError:
            if directory.is_symlink() or not directory.is_dir():
                raise ValueError("unsafe extraction directory")
        checked_dirs.add(directory)

    total = 0
    with tarfile.open(archive, mode) as tar:
        for count, member in enumerate(tar):
            name = member.name
            if (name.startswith("/") or "\\" in name or ":" in name
                    or ".." in name.split("/") or "." in name.split("/")
                    or not (member.isdir() or member.isfile())):
                raise ValueError("unsafe archive entry: " + name)
            total += member.size
            if total > 2 * 1024**3 or count >= 100000:
                raise ValueError("archive resource limit exceeded")
            target = destination / PurePosixPath(name)
            if member.isdir():
                ensure_directory(target)
            else:
                ensure_directory(target.parent)
                with tar.extractfile(member) as source, target.open("xb") as output:
                    shutil.copyfileobj(source, output)
                target.chmod(0o755 if member.mode & 0o111 else 0o644)


def assert_go_executables(archive, install):
    remaining = {"go/bin/go", "go/bin/gofmt"}
    with tarfile.open(archive, "r:gz") as tar:
        for member in tar:
            if member.name in remaining:
                expected = hashlib.sha256()
                with tar.extractfile(member) as stream:
                    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                        expected.update(chunk)
                assert_hash(local_path(ROOT, install / member.name), expected.hexdigest())
                remaining.remove(member.name)
    if remaining:
        raise ValueError("Go executables missing in verified archive")


class HTTPSRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urlparse(newurl).scheme != "https":
            raise ValueError("refusing non-HTTPS redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def remove_tree(path):
    path = local_path(ROOT, path)
    if path.exists():
        for base, dirs, files in os.walk(path, followlinks=False):
            for name in dirs + files:
                local_path(ROOT, Path(base) / name)
        shutil.rmtree(path)


def paths(spec):
    name = Path(urlparse(spec["url"]).path).name
    return (local_path(ROOT, ROOT / ".tools/downloads" / name),
            local_path(ROOT, ROOT / ".tools" / spec["installPath"]))


def bootstrap(offline=False):
    tool, spec, target = selected()
    archive, install = paths(spec)
    archive.parent.mkdir(parents=True, exist_ok=True)
    if not archive.exists():
        if offline:
            raise ValueError("offline cache missing: " + archive.name)
        url = spec["url"]
        mirror = os.environ.get("JELEE_TOOLS_MIRROR")
        if mirror:
            url = mirror.rstrip("/") + "/" + archive.name
        if urlparse(url).scheme != "https":
            raise ValueError("tool sources must use HTTPS")
        partial = local_path(ROOT, archive.with_suffix(archive.suffix + ".partial"))
        try:
            print("Downloading " + archive.name, flush=True)
            with build_opener(HTTPSRedirect()).open(url, timeout=60) as source, partial.open("wb") as output:
                shutil.copyfileobj(source, output)
            assert_hash(partial, spec["sha256"])
            partial.replace(archive)
        finally:
            partial.unlink(missing_ok=True)
    try:
        assert_hash(archive, spec["sha256"])
    except ValueError:
        archive.unlink()
        raise
    executable = local_path(ROOT, install / spec["executable"])
    if not executable.exists():
        stage = local_path(ROOT, install.with_name(install.name + ".staging"))
        remove_tree(stage)
        stage.mkdir(parents=True)
        try:
            safe_extract(archive, stage)
            if not (stage / spec["executable"]).is_file():
                raise ValueError("Go binary absent from archive")
            remove_tree(install)
            stage.replace(install)
        finally:
            remove_tree(stage)
    assert_go_executables(archive, install)
    bin_dir = local_path(ROOT, ROOT / ".bin")
    bin_dir.mkdir(exist_ok=True)
    wrapper = bin_dir / "go"
    wrapper.write_text('#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                       'exec python3 "$root/scripts/toolchain.py" go "$@"\n', encoding="utf-8")
    wrapper.chmod(0o755)
    record = {"schemaVersion": 1, "name": "go", "version": tool["version"], "platform": target,
              "archiveSHA256": spec["sha256"], "executableSHA256": digest(executable),
              "installedAt": datetime.now(timezone.utc).isoformat()}
    record_path = local_path(ROOT, ROOT / ".tools/.installed.json")
    installed = {"schemaVersion": 1, "platforms": {}}
    if record_path.exists():
        prior = json.loads(record_path.read_text(encoding="utf-8"))
        if "platforms" in prior:
            installed = prior
    installed["platforms"][target] = record
    record_path.write_text(json.dumps(installed, indent=2) + "\n", encoding="utf-8")
    run_go(["telemetry", "off"])
    verify()


def go_environment(spec):
    cache = local_path(ROOT, ROOT / ".tools/cache")
    env = dict(os.environ)
    env.update(GOTOOLCHAIN="local", GOENV="off", GOROOT=str(ROOT / ".tools" / spec["installPath"] / "go"))
    for key, name in {"GOCACHE": "go-build", "GOPATH": "gopath", "GOMODCACHE": "gomod",
                      "GOTMPDIR": "tmp", "TMPDIR": "tmp", "XDG_CONFIG_HOME": "config"}.items():
        path = local_path(ROOT, cache / name)
        path.mkdir(parents=True, exist_ok=True)
        env[key] = str(path)
    return env


def run_go(args, capture=False):
    _, spec, _ = selected()
    _, install = paths(spec)
    exe = local_path(ROOT, install / spec["executable"])
    if not exe.is_file():
        raise ValueError("run scripts/bootstrap-tools first")
    return subprocess.run([str(exe)] + args, env=go_environment(spec), check=True,
                          text=True, capture_output=capture)


def verify():
    tool, spec, target = selected()
    archive, install = paths(spec)
    assert_hash(archive, spec["sha256"])
    installed = json.loads((ROOT / ".tools/.installed.json").read_text(encoding="utf-8"))
    record = installed["platforms"][target]
    if (record["version"] != tool["version"] or record["platform"] != target
            or record["archiveSHA256"] != spec["sha256"]):
        raise ValueError("installed record does not match manifest; bootstrap again")
    assert_hash(install / spec["executable"], record["executableSHA256"])
    assert_go_executables(archive, install)
    actual = run_go(["version"], capture=True).stdout.strip()
    if actual != "go version go" + tool["version"] + " " + target.replace("-", "/"):
        raise ValueError("unexpected toolchain: " + actual)
    module = ROOT / "go.mod"
    if module.exists() and not re.search(r"(?m)^go " + re.escape(tool["version"]) + r"$", module.read_text()):
        raise ValueError("go.mod version differs from tools/manifest.json")
    print("Verified " + actual + "; archive SHA256 and installed binary match")


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "go":
        run_go(sys.argv[2:])
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["bootstrap", "verify", "clean"])
    parser.add_argument("--offline", action="store_true")
    args = parser.parse_args()
    if args.command == "bootstrap":
        bootstrap(args.offline)
    elif args.command == "verify":
        verify()
    else:
        for name in (".tools", ".bin", ".testfixtures", ".testdata"):
            remove_tree(ROOT / name)
        print("Removed local tools, caches and generated test data")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print("toolchain: " + str(error), file=sys.stderr)
        sys.exit(1)
