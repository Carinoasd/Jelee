#!/usr/bin/env python3
"""Project-local Linux Go, Node and golangci-lint bootstrap; only Python's standard library is required."""
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


def manifest_tool(name):
    manifest = json.loads((ROOT / "tools/manifest.json").read_text(encoding="utf-8"))
    if manifest["schemaVersion"] != 1:
        raise ValueError("unsupported manifest schema")
    tool, = [tool for tool in manifest["tools"] if tool["name"] == name]
    if not re.fullmatch(r"\d+\.\d+\.\d+", tool["version"]):
        raise ValueError("invalid version")
    return tool


def linux_target():
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None:
        raise ValueError("POSIX bootstrap supports Linux amd64/arm64; use bootstrap-tools.ps1 on Windows")
    return "linux-" + arch


def selected():
    tool = manifest_tool("go")
    target = linux_target()
    spec = tool["platforms"][target]
    if (urlparse(spec["url"]).scheme != "https" or not re.fullmatch(r"[a-f0-9]{64}", spec["sha256"])
            or spec["archive"] != "tar.gz" or spec["installPath"] != "go/" + tool["version"] + "/" + target
            or spec["executable"] != "go/bin/go"):
        raise ValueError("invalid Go source, hash or installation layout")
    return tool, spec, target


def node_selected():
    tool = manifest_tool("node")
    target = linux_target()
    spec = tool["platforms"][target]
    root = "node-v" + tool["version"] + "-" + target.replace("amd64", "x64")
    links = {root + "/bin/npm": "../lib/node_modules/npm/bin/npm-cli.js",
             root + "/bin/npx": "../lib/node_modules/npm/bin/npx-cli.js",
             root + "/bin/corepack": "../lib/node_modules/corepack/dist/corepack.js"}
    if (urlparse(spec["url"]).scheme != "https" or not re.fullmatch(r"[a-f0-9]{64}", spec["sha256"])
            or not spec["url"].endswith("/v" + tool["version"] + "/" + root + ".tar.xz")
            or spec["archive"] != "tar.xz" or spec["archiveRoot"] != root
            or spec["installPath"] != "node/" + tool["version"] + "/" + target
            or spec["executable"] != root + "/bin/node"
            or spec["npmCli"] != root + "/lib/node_modules/npm/bin/npm-cli.js"
            or spec["skippedLinks"] != links or tool.get("npmCache") != "npm-cache"):
        raise ValueError("invalid Node source, hash or installation layout")
    return tool, spec, target


GOLANGCI = "golangci-lint"
GOLANGCI_RELEASES = "https://github.com/golangci/golangci-lint/releases/download/"


def golangci_selected():
    tool = manifest_tool(GOLANGCI)
    target = linux_target()
    spec = tool["platforms"][target]
    root = "golangci-lint-" + tool["version"] + "-" + target
    if (urlparse(spec["url"]).scheme != "https" or not re.fullmatch(r"[a-f0-9]{64}", spec["sha256"])
            or spec["url"] != GOLANGCI_RELEASES + "v" + tool["version"] + "/" + root + ".tar.gz"
            or spec["archive"] != "tar.gz" or spec["archiveRoot"] != root
            or spec["installPath"] != GOLANGCI + "/" + tool["version"] + "/" + target
            or spec["executable"] != root + "/golangci-lint" or spec["licenseFile"] != root + "/LICENSE"
            or tool.get("cache") != "cache/golangci-lint"):
        raise ValueError("invalid golangci-lint source, hash or installation layout")
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


def safe_extract(archive, destination, mode="r:gz", skipped_links=None):
    """Extract regular files and directories only.

    skipped_links maps exact symbolic link names to their exact targets. Such
    entries are not created (wrappers call the real file instead); any other
    link or special entry still fails the whole extraction.
    """
    skipped_links = skipped_links or {}
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
            if member.issym() and skipped_links.get(name) == member.linkname:
                continue
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
    assert_archive_files(archive, install, {"go/bin/go", "go/bin/gofmt"}, "r:gz")


def assert_archive_files(archive, install, names, mode):
    remaining = set(names)
    with tarfile.open(archive, mode) as tar:
        for member in tar:
            if member.name in remaining:
                expected = hashlib.sha256()
                with tar.extractfile(member) as stream:
                    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                        expected.update(chunk)
                assert_hash(local_path(ROOT, install / member.name), expected.hexdigest())
                remaining.remove(member.name)
    if remaining:
        raise ValueError("executables missing in verified archive: " + ", ".join(sorted(remaining)))


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


def fetch(spec, offline):
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
    return archive, install


ALL_TOOLS = ("go", "node", GOLANGCI)


def bootstrap(offline=False, tools=ALL_TOOLS):
    if "go" in tools:
        bootstrap_go(offline)
    if "node" in tools:
        bootstrap_node(offline)
    if GOLANGCI in tools:
        bootstrap_golangci(offline)


def bootstrap_go(offline=False):
    tool, spec, target = selected()
    archive, install = fetch(spec, offline)
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
    installed = read_installed()
    installed["platforms"][target] = record
    write_installed(installed)
    run_go(["telemetry", "off"])
    verify_go()


def read_installed():
    # Go records stay under "platforms" for the PowerShell scripts; other
    # tools are recorded per name under "tools".
    record_path = local_path(ROOT, ROOT / ".tools/.installed.json")
    installed = {"schemaVersion": 1, "platforms": {}}
    if record_path.exists():
        prior = json.loads(record_path.read_text(encoding="utf-8"))
        if "platforms" in prior:
            installed = prior
    installed.setdefault("tools", {})
    return installed


def write_installed(installed):
    record_path = local_path(ROOT, ROOT / ".tools/.installed.json")
    record_path.write_text(json.dumps(installed, indent=2) + "\n", encoding="utf-8")


NODE_WRAPPERS = {
    "node": 'exec "$node" "$@"\n',
    "npm": 'exec "$node" "$tool/{npm}" "$@"\n',
    "npx": 'exec "$node" "$tool/{npx}" "$@"\n',
}


def node_wrapper(spec, body):
    root = spec["archiveRoot"]
    return ('#!/bin/sh\n# Generated by scripts/toolchain.py; runs the manifest-pinned Node.\nset -eu\n'
            'root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
            'tool="$root/.tools/' + spec["installPath"] + '"\n'
            'node="$tool/' + spec["executable"] + '"\n'
            '[ -x "$node" ] || { echo "run scripts/bootstrap-tools first" >&2; exit 1; }\n'
            # Package scripts resolve node/npm through PATH; keep these wrappers first.
            'PATH="$root/.bin:$PATH"\n'
            # Project-local npm state: no user or global configuration, cache or prefix.
            'npm_config_cache="$root/.tools/npm-cache"\n'
            'npm_config_userconfig="$root/.tools/npm-userconfig"\n'
            'npm_config_globalconfig="$root/.tools/npm-globalconfig"\n'
            'npm_config_prefix="$root/.tools/npm-prefix"\n'
            'npm_config_update_notifier=false\nnpm_config_fund=false\n'
            'export PATH npm_config_cache npm_config_userconfig npm_config_globalconfig npm_config_prefix '
            'npm_config_update_notifier npm_config_fund\n'
            + body.format(npm=spec["npmCli"], npx=root + "/lib/node_modules/npm/bin/npx-cli.js"))


def bootstrap_node(offline=False):
    tool, spec, target = node_selected()
    archive, install = fetch(spec, offline)
    executable = local_path(ROOT, install / spec["executable"])
    if not executable.exists():
        stage = local_path(ROOT, install.with_name(install.name + ".staging"))
        remove_tree(stage)
        stage.mkdir(parents=True)
        try:
            safe_extract(archive, stage, "r:xz", spec["skippedLinks"])
            for name in (spec["executable"], spec["npmCli"]):
                if not (stage / name).is_file():
                    raise ValueError("Node file absent from archive: " + name)
            remove_tree(install)
            stage.replace(install)
        finally:
            remove_tree(stage)
    assert_archive_files(archive, install, {spec["executable"], spec["npmCli"]}, "r:xz")
    bin_dir = local_path(ROOT, ROOT / ".bin")
    bin_dir.mkdir(exist_ok=True)
    for name, body in NODE_WRAPPERS.items():
        wrapper = bin_dir / name
        wrapper.write_text(node_wrapper(spec, body), encoding="utf-8")
        wrapper.chmod(0o755)
    local_path(ROOT, ROOT / ".tools" / tool["npmCache"]).mkdir(exist_ok=True)
    installed = read_installed()
    installed["tools"].setdefault("node", {})[target] = {
        "schemaVersion": 1, "name": "node", "version": tool["version"], "platform": target,
        "archiveSHA256": spec["sha256"], "executableSHA256": digest(executable),
        "installedAt": datetime.now(timezone.utc).isoformat()}
    write_installed(installed)
    verify_node()


GOLANGCI_WRAPPER = ('#!/bin/sh\n# Generated by scripts/toolchain.py; runs the manifest-pinned golangci-lint.\nset -eu\n'
                    'root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                    'exec python3 "$root/scripts/toolchain.py" golangci-lint "$@"\n')


def bootstrap_golangci(offline=False):
    tool, spec, target = golangci_selected()
    archive, install = fetch(spec, offline)
    executable = local_path(ROOT, install / spec["executable"])
    files = {spec["executable"], spec["licenseFile"]}
    if not executable.exists():
        stage = local_path(ROOT, install.with_name(install.name + ".staging"))
        remove_tree(stage)
        stage.mkdir(parents=True)
        try:
            safe_extract(archive, stage)
            for name in files:
                if not (stage / name).is_file():
                    raise ValueError("golangci-lint file absent from archive: " + name)
            remove_tree(install)
            stage.replace(install)
        finally:
            remove_tree(stage)
    assert_archive_files(archive, install, files, "r:gz")
    bin_dir = local_path(ROOT, ROOT / ".bin")
    bin_dir.mkdir(exist_ok=True)
    wrapper = bin_dir / GOLANGCI
    wrapper.write_text(GOLANGCI_WRAPPER, encoding="utf-8")
    wrapper.chmod(0o755)
    installed = read_installed()
    installed["tools"].setdefault(GOLANGCI, {})[target] = {
        "schemaVersion": 1, "name": GOLANGCI, "version": tool["version"], "platform": target,
        "archiveSHA256": spec["sha256"], "executableSHA256": digest(executable),
        "installedAt": datetime.now(timezone.utc).isoformat()}
    write_installed(installed)
    verify_golangci()


def golangci_environment():
    """Pinned Go environment plus a project-local golangci-lint cache.

    golangci-lint runs `go list` and the type checker through the go command
    on PATH, so the pinned GOROOT/bin goes first. Nothing is written to the
    user's home directory or global configuration.
    """
    _, go_spec, _ = selected()
    tool, _, _ = golangci_selected()
    env = go_environment(go_spec)
    go_bin = ROOT / ".tools" / go_spec["installPath"] / "go" / "bin"
    if not (go_bin / "go").is_file():
        raise ValueError("run scripts/bootstrap-tools first")
    cache = local_path(ROOT, ROOT / ".tools" / tool["cache"])
    cache.mkdir(parents=True, exist_ok=True)
    env.update(PATH=str(go_bin) + os.pathsep + env.get("PATH", ""), GOLANGCI_LINT_CACHE=str(cache))
    return env


def run_golangci(args):
    _, spec, _ = golangci_selected()
    _, install = paths(spec)
    exe = local_path(ROOT, install / spec["executable"])
    if not exe.is_file():
        raise ValueError("run scripts/bootstrap-tools first")
    return subprocess.run([str(exe)] + args, env=golangci_environment()).returncode


def verify_golangci():
    tool, spec, target = golangci_selected()
    archive, install = paths(spec)
    assert_hash(archive, spec["sha256"])
    try:
        record = read_installed()["tools"][GOLANGCI][target]
    except (KeyError, OSError, ValueError):
        raise ValueError("golangci-lint is not installed; run scripts/bootstrap-tools") from None
    if (record["version"] != tool["version"] or record["platform"] != target
            or record["archiveSHA256"] != spec["sha256"]):
        raise ValueError("installed golangci-lint record does not match manifest; bootstrap again")
    executable = local_path(ROOT, install / spec["executable"])
    assert_hash(executable, record["executableSHA256"])
    assert_archive_files(archive, install, {spec["executable"], spec["licenseFile"]}, "r:gz")
    wrapper = local_path(ROOT, ROOT / ".bin" / GOLANGCI)
    if not wrapper.is_file() or wrapper.read_text(encoding="utf-8") != GOLANGCI_WRAPPER:
        raise ValueError("stale .bin/golangci-lint wrapper; bootstrap again")
    actual = subprocess.run([str(executable), "version"], check=True, text=True,
                            capture_output=True, env=golangci_environment()).stdout.strip()
    if not actual.startswith("golangci-lint has version " + tool["version"] + " "):
        raise ValueError("unexpected golangci-lint version: " + actual)
    print("Verified golangci-lint " + tool["version"] + "; archive SHA256 and installed binary match")


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


def verify(tools=ALL_TOOLS):
    if "go" in tools:
        verify_go()
    if "node" in tools:
        verify_node()
    if GOLANGCI in tools:
        verify_golangci()


def verify_node():
    tool, spec, target = node_selected()
    archive, install = paths(spec)
    assert_hash(archive, spec["sha256"])
    try:
        record = read_installed()["tools"]["node"][target]
    except (KeyError, OSError, ValueError):
        raise ValueError("Node is not installed; run scripts/bootstrap-tools") from None
    if (record["version"] != tool["version"] or record["platform"] != target
            or record["archiveSHA256"] != spec["sha256"]):
        raise ValueError("installed Node record does not match manifest; bootstrap again")
    executable = local_path(ROOT, install / spec["executable"])
    assert_hash(executable, record["executableSHA256"])
    assert_archive_files(archive, install, {spec["executable"], spec["npmCli"]}, "r:xz")
    for name, body in NODE_WRAPPERS.items():
        wrapper = local_path(ROOT, ROOT / ".bin" / name)
        if not wrapper.is_file() or wrapper.read_text(encoding="utf-8") != node_wrapper(spec, body):
            raise ValueError("stale .bin/" + name + " wrapper; bootstrap again")
    actual = subprocess.run([str(executable), "--version"], check=True, text=True,
                            capture_output=True).stdout.strip()
    if actual != "v" + tool["version"]:
        raise ValueError("unexpected Node version: " + actual)
    npm = subprocess.run([str(ROOT / ".bin/npm"), "--version"], check=True, text=True,
                         capture_output=True).stdout.strip()
    expected_npm = tool.get("bundledPackageManager", "").removeprefix("npm ")
    if npm != expected_npm:
        raise ValueError("unexpected npm version: " + npm)
    print("Verified Node " + actual + " with npm " + npm + "; archive SHA256 and installed binary match")


def verify_go():
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
    if len(sys.argv) > 1 and sys.argv[1] == GOLANGCI:
        sys.exit(run_golangci(sys.argv[2:]))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["bootstrap", "verify", "clean"])
    parser.add_argument("--offline", action="store_true")
    parser.add_argument("--tool", action="append", choices=list(ALL_TOOLS),
                        help="limit bootstrap or verify to this tool (repeatable; default: all)")
    args = parser.parse_args()
    tools = tuple(args.tool or ALL_TOOLS)
    if args.command == "bootstrap":
        bootstrap(args.offline, tools)
    elif args.command == "verify":
        verify(tools)
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
