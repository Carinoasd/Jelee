#!/usr/bin/env python3
"""Optional pinned Linux media tools; no system installation or runtime integration."""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import selectors
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from urllib.parse import urlparse
from urllib.request import build_opener

sys.dont_write_bytecode = True
import toolchain

ROOT = Path(__file__).resolve().parent.parent
HASH = re.compile(r"[a-f0-9]{64}")
VERSION = re.compile(r"[A-Za-z0-9._+-]{1,100}")


def local(path):
    return toolchain.local_path(ROOT, path)


def relative(value):
    return (isinstance(value, str) and value != "" and "\\" not in value and ":" not in value
            and not value.startswith("/") and all(p not in ("", ".", "..") for p in value.split("/")))


def selected():
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise ValueError("media bootstrap supports Linux amd64 only; use bootstrap-media-tools.ps1 on Windows amd64")
    target = "linux-amd64"
    manifest = json.loads(local(ROOT / "tools/manifest.json").read_text(encoding="utf-8"))
    media = manifest["mediaTools"]
    if manifest["schemaVersion"] != 1 or media["schemaVersion"] != 1 or media["optional"] is not True:
        raise ValueError("invalid optional media manifest")
    spec = media["platforms"][target]
    validate_spec(spec, target)
    return spec, target


def validate_spec(spec, target):
    uri = urlparse(spec["url"])
    if (uri.scheme != "https" or not uri.netloc or uri.username or uri.password or uri.query or uri.fragment
            or not HASH.fullmatch(spec["sha256"]) or spec["archive"] != "tar.xz"
            or type(spec["sizeBytes"]) is not int or not 0 < spec["sizeBytes"] <= 256 * 1024**2
            or not VERSION.fullmatch(spec["vendorVersion"]) or not VERSION.fullmatch(spec["archiveRoot"])
            or not re.fullmatch(r"media/" + re.escape(target) + r"/[A-Za-z0-9._+-]+", spec["installPath"])):
        raise ValueError("invalid media archive, version or installation layout")
    if set(spec["executables"]) != {"ffmpeg", "ffprobe"}:
        raise ValueError("media manifest must declare ffmpeg and ffprobe")
    for name, item in spec["executables"].items():
        if (item["path"] != spec["archiveRoot"] + "/bin/" + name or not HASH.fullmatch(item["sha256"])
                or item["productionAllowed"] is not (name == "ffprobe")):
            raise ValueError("invalid executable hash, layout or production policy")
    licenses = spec["licenseFiles"]
    if not isinstance(licenses, list) or not 1 <= len(licenses) <= 16:
        raise ValueError("license files must be declared")
    for item in licenses:
        if (not relative(item["path"]) or not item["path"].startswith(spec["archiveRoot"] + "/")
                or not HASH.fullmatch(item["sha256"])):
            raise ValueError("invalid license hash or layout")


def paths(spec, target):
    archive = local(ROOT / ".tools/downloads" / PurePosixPath(urlparse(spec["url"]).path).name)
    install = local(ROOT / ".tools" / spec["installPath"])
    record = local(ROOT / ".tools/media-installed" / (target + ".json"))
    return archive, install, record


def environment():
    env = dict(os.environ)
    cache = local(ROOT / ".tools/cache/media")
    for key, name in {"TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp", "XDG_CONFIG_HOME": "config"}.items():
        directory = local(cache / name)
        directory.mkdir(parents=True, exist_ok=True)
        env[key] = str(directory)
    for key in list(env):
        if key == "FFREPORT" or key.startswith("LD_"):
            env.pop(key)
    return env


def check_files(spec, install, names=None):
    selected_names = names if names is not None else spec["executables"]
    for name in selected_names:
        entry = spec["executables"][name]
        executable = local(install / entry["path"])
        toolchain.assert_hash(executable, entry["sha256"])
        if not os.access(executable, os.X_OK):
            raise ValueError("media executable is not executable")
    for entry in spec["licenseFiles"]:
        toolchain.assert_hash(local(install / entry["path"]), entry["sha256"])


def check_version(spec, install, name):
    output = version_output([str(local(install / spec["executables"][name]["path"])), "-version"])
    first = output.splitlines()[0] if output else ""
    expected = name + " version " + spec["vendorVersion"] + " "
    if not first.startswith(expected):
        raise ValueError("media executable version differs from manifest")
    return first


def version_output(command, timeout=10, limit=65536):
    # Only the fixed -version diagnostic uses this helper. Media processing needs
    # a separate production runner with process-tree and input isolation.
    with subprocess.Popen(command, env=environment(), stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
        try:
            outputs = {process.stdout: bytearray(), process.stderr: bytearray()}
            deadline = time.monotonic() + timeout
            with selectors.DefaultSelector() as selector:
                for stream in outputs:
                    os.set_blocking(stream.fileno(), False)
                    selector.register(stream, selectors.EVENT_READ)
                while selector.get_map():
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise ValueError("media version process timed out")
                    for key, _ in selector.select(remaining):
                        stream = key.fileobj
                        chunk = os.read(stream.fileno(), min(4096, limit - len(outputs[stream]) + 1))
                        if not chunk:
                            selector.unregister(stream)
                        elif len(outputs[stream]) + len(chunk) > limit:
                            raise ValueError("media version output exceeds limit")
                        else:
                            outputs[stream].extend(chunk)
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ValueError("media version process timed out")
                if process.wait(timeout=remaining) != 0:
                    raise ValueError("media version process failed")
            return outputs[process.stdout].decode("utf-8", errors="strict")
        finally:
            if process.poll() is None:
                process.kill()
            process.wait()


def check_record(spec, target, record):
    installed = json.loads(local(record).read_text(encoding="utf-8"))
    if (installed["schemaVersion"] != 1 or installed["platform"] != target
            or installed["vendorVersion"] != spec["vendorVersion"] or installed["archiveSHA256"] != spec["sha256"]
            or installed["executables"] != spec["executables"] or installed["licenseFiles"] != spec["licenseFiles"]):
        raise ValueError("media installation record differs from manifest; bootstrap again")


def verify():
    spec, target = selected()
    archive, install, record = paths(spec, target)
    toolchain.assert_hash(archive, spec["sha256"])
    if archive.stat().st_size != spec["sizeBytes"]:
        raise ValueError("media archive size differs from manifest")
    check_record(spec, target, record)
    check_files(spec, install)
    for name in spec["executables"]:
        check_version(spec, install, name)
    print("Verified media tools " + target + " " + spec["vendorVersion"] + "; archive, executable and license SHA256 match")


def download(spec, archive, offline):
    if archive.exists():
        try:
            toolchain.assert_hash(archive, spec["sha256"])
            if archive.stat().st_size != spec["sizeBytes"]:
                raise ValueError("media archive size differs from manifest")
        except ValueError:
            # Only this known project-local archive is removed; installed files
            # remain untouched and are never automatically repaired.
            local(archive).unlink(missing_ok=True)
            raise
        return
    if offline:
        raise ValueError("offline media archive is missing")
    uri = spec["url"]
    mirror = os.environ.get("JELEE_TOOLS_MIRROR")
    if mirror:
        uri = mirror.rstrip("/") + "/" + archive.name
    parsed = urlparse(uri)
    if parsed.scheme != "https" or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("media sources and mirrors must use credential-free HTTPS")
    partial = local(archive.with_name(archive.name + "." + uuid.uuid4().hex + ".partial"))
    try:
        print("Downloading " + archive.name, flush=True)
        with build_opener(toolchain.HTTPSRedirect()).open(uri, timeout=60) as source, partial.open("xb") as output:
            total = 0
            while True:
                chunk = source.read(min(1024 * 1024, spec["sizeBytes"] - total + 1))
                if not chunk:
                    break
                total += len(chunk)
                if total > spec["sizeBytes"]:
                    raise ValueError("media download exceeds manifest size")
                output.write(chunk)
        if total != spec["sizeBytes"]:
            raise ValueError("media download size differs from manifest")
        toolchain.assert_hash(partial, spec["sha256"])
        partial.replace(archive)
    finally:
        partial.unlink(missing_ok=True)


def bootstrap(offline=False):
    import fcntl
    spec, target = selected()
    archive, install, record = paths(spec, target)
    for directory in (archive.parent, install.parent, record.parent):
        directory.mkdir(parents=True, exist_ok=True)
    lock_path = local(record.with_suffix(".lock"))
    with lock_path.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("another media bootstrap is running") from None
        download(spec, archive, offline)
        if not install.exists():
            stage = local(install.with_name(install.name + ".staging-" + uuid.uuid4().hex))
            stage.mkdir()
            try:
                toolchain.safe_extract(archive, stage, mode="r:xz")
                check_files(spec, stage)
                for name in spec["executables"]:
                    check_version(spec, stage, name)
                stage.replace(install)
            finally:
                if stage.exists():
                    toolchain.remove_tree(stage)
        # Never silently bless or repair changed installed bytes.
        check_files(spec, install)
        for name in spec["executables"]:
            check_version(spec, install, name)
        installed = {"schemaVersion": 1, "platform": target, "vendorVersion": spec["vendorVersion"],
                     "archiveSHA256": spec["sha256"], "executables": spec["executables"],
                     "licenseFiles": spec["licenseFiles"], "installedAt": datetime.now(timezone.utc).isoformat()}
        partial_record = local(record.with_name(record.name + "." + uuid.uuid4().hex + ".partial"))
        try:
            partial_record.write_text(json.dumps(installed, indent=2) + "\n", encoding="utf-8")
            partial_record.replace(record)
        finally:
            partial_record.unlink(missing_ok=True)
        bin_dir = local(ROOT / ".bin")
        bin_dir.mkdir(exist_ok=True)
        for name in spec["executables"]:
            wrapper = local(bin_dir / name)
            wrapper.write_text('#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                               'exec python3 "$root/scripts/media-tools.py" run ' + name + ' "$@"\n', encoding="utf-8")
            wrapper.chmod(0o755)
    verify()


def run_tool(name, args):
    if name not in ("ffmpeg", "ffprobe"):
        raise ValueError("unknown media tool")
    spec, target = selected()
    _, install, record = paths(spec, target)
    check_record(spec, target, record)
    check_files(spec, install, [name])
    check_version(spec, install, name)
    executable = str(local(install / spec["executables"][name]["path"]))
    # No shell and no PATH fallback. These are explicit developer wrappers,
    # not the production process runner or a media sandbox.
    os.execve(executable, [executable] + args, environment())


def main():
    if len(sys.argv) >= 3 and sys.argv[1] == "run":
        run_tool(sys.argv[2], sys.argv[3:])
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["bootstrap", "verify"])
    parser.add_argument("--offline", action="store_true")
    args = parser.parse_args()
    if args.command == "bootstrap":
        bootstrap(args.offline)
    else:
        verify()


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as error:
        print("media-tools: " + str(error), file=sys.stderr)
        sys.exit(1)
