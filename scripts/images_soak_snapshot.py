"""Committed source extraction and verification for independent soak workers."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tarfile
from urllib.parse import urlsplit

from test_probe_runtime import digest
from test_scan_memory import capture


class SnapshotFailure(RuntimeError):
    def __init__(self, code="soak_snapshot_invalid"):
        self.code = code
        super().__init__(code)


def relative(value):
    path = PurePosixPath(value)
    if not value or path.is_absolute() or ".." in path.parts or "\\" in value or str(path) != value or "\x00" in value:
        raise SnapshotFailure()
    return path


def extract(archive, destination):
    """No links, devices, overwrites, archive ownership, or executable hooks."""
    records, seen = {}, set()
    total = 0
    with tarfile.open(archive, "r|*") as source:
        for member in source:
            name = member.name.rstrip("/") if member.isdir() else member.name
            parts = relative(name)
            if name in seen or len(seen) >= 30000 or parts.parts[0] in {".git", ".tools", ".bin", ".testdata"}:
                raise SnapshotFailure()
            seen.add(name)
            target = destination.joinpath(*parts.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            if not member.isfile() or not 0 <= member.size <= 64 * 1024**2:
                raise SnapshotFailure()
            total += member.size
            if total > 512 * 1024**2:
                raise SnapshotFailure()
            target.parent.mkdir(parents=True, exist_ok=True)
            checksum = hashlib.sha256()
            with source.extractfile(member) as incoming, target.open("xb") as outgoing:
                size = 0
                while block := incoming.read(65536):
                    size += len(block)
                    if size > member.size:
                        raise SnapshotFailure()
                    checksum.update(block)
                    outgoing.write(block)
            if size != member.size:
                raise SnapshotFailure()
            executable = bool(member.mode & 0o111)
            target.chmod(0o555 if executable else 0o444)
            records[name] = {"sha256": checksum.hexdigest(), "bytes": size, "executable": executable}
    for directory, _dirs, _files in os.walk(destination, topdown=False):
        if Path(directory) != destination:
            Path(directory).chmod(0o555)
    return records


def verify(root, records):
    actual = set()
    for directory, dirs, files in os.walk(root, followlinks=False):
        base = Path(directory)
        if base == root:
            dirs[:] = [name for name in dirs if name not in {".tools", ".bin", ".testdata"}]
        for name in dirs:
            path = base / name
            if path.is_symlink() or path.stat().st_mode & 0o222:
                raise SnapshotFailure("soak_snapshot_changed")
        for name in files:
            path = base / name
            key = path.relative_to(root).as_posix()
            info = path.lstat()
            item = records.get(key)
            if (item is None or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or
                    info.st_mode & 0o222 or info.st_size != item["bytes"] or
                    bool(info.st_mode & 0o111) != item["executable"] or digest(path) != item["sha256"]):
                raise SnapshotFailure("soak_snapshot_changed")
            actual.add(key)
    if actual != set(records):
        raise SnapshotFailure("soak_snapshot_changed")


def copy_verified(source, destination, expected, executable=False):
    if source.is_symlink() or not source.is_file() or source.stat().st_size > 512 * 1024**2:
        raise SnapshotFailure("soak_dependency_invalid")
    # Resolve every ancestor before reading a dependency from the local cache.
    if any(parent.is_symlink() for parent in source.parents):
        raise SnapshotFailure("soak_dependency_invalid")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with source.open("rb") as incoming, destination.open("xb") as output:
        shutil.copyfileobj(incoming, output, 65536)
    if digest(destination) != expected:
        raise SnapshotFailure("soak_dependency_invalid")
    destination.chmod(0o555 if executable else 0o444)


def provision(workspace, root):
    """Only existing pinned bytes; SDK is freshly extracted by snapshot code."""
    manifest = json.loads((root / "tools/manifest.json").read_bytes())
    go, = [tool for tool in manifest["tools"] if tool["name"] == "go"]
    spec = go["platforms"]["linux-amd64"]
    archive = Path(urlsplit(spec["url"]).path).name
    copy_verified(workspace / ".tools/downloads" / archive, root / ".tools/downloads" / archive, spec["sha256"])
    media = manifest["mediaTools"]["platforms"]["linux-amd64"]
    items = [(media["executables"]["ffprobe"], True)] + [(item, False) for item in media["licenseFiles"]]
    for item, executable in items:
        name = Path(".tools") / str(relative(media["installPath"])) / str(relative(item["path"]))
        copy_verified(workspace / name, root / name, item["sha256"], executable)
    runtime = manifest["mediaRuntime"]
    for package in runtime["packages"]:
        for item in package["files"]:
            name = Path(".tools") / str(relative(runtime["installPath"])) / str(relative(item["destination"]))
            copy_verified(workspace / name, root / name, item["sha256"], item["destination"].startswith("lib64/"))
    for item in runtime["licenseTexts"]:
        name = Path(".tools") / str(relative(runtime["installPath"])) / str(relative(item["destination"]))
        copy_verified(workspace / name, root / name, item["sha256"])


def prepare(workspace, owned):
    root = owned / "source"
    root.mkdir(mode=0o700)
    revision = capture(["git", "-C", str(workspace), "rev-parse", "HEAD", "HEAD^{tree}"], limit=4096)
    values = revision.stdout.decode("ascii").splitlines()
    if revision.returncode or len(values) != 2 or any(not re.fullmatch(r"[a-f0-9]{40}", v) for v in values):
        raise SnapshotFailure()
    # Size-check the committed tree before git materializes the tar archive.
    listing = capture(["git", "-C", str(workspace), "ls-tree", "-rlz", values[0]], limit=8 * 1024**2)
    entries = listing.stdout.rstrip(b"\0").split(b"\0")
    if listing.returncode or len(entries) > 30000:
        raise SnapshotFailure()
    total = 0
    for entry in entries:
        fields = entry.split(b"\t", 1)[0].split()
        if len(fields) != 4 or fields[0] not in (b"100644", b"100755") or fields[1] != b"blob":
            raise SnapshotFailure()
        total += int(fields[3])
    if total > 512 * 1024**2:
        raise SnapshotFailure()
    archive = owned / "source.tar"
    result = capture(["git", "-C", str(workspace), "archive", "--format=tar", "--output=" + str(archive), values[0]], timeout=120)
    if result.returncode or archive.stat().st_size > 600 * 1024**2:
        raise SnapshotFailure()
    records = extract(archive, root)
    archive.unlink()
    provision(workspace, root)
    verify(root, records)
    return {"version": 1, "commit": values[0], "tree": values[1], "files": records}


def remove_snapshot(owned):
    if owned.parent != Path("/var/tmp").resolve() or not re.fullmatch(r"jelee-soak-snapshot-[a-f0-9]{32}", owned.name) or owned.is_symlink():
        raise SnapshotFailure("soak_snapshot_cleanup_invalid")
    for directory, dirs, _files in os.walk(owned, followlinks=False):
        path = Path(directory)
        if path.is_symlink():
            raise SnapshotFailure("soak_snapshot_cleanup_invalid")
        path.chmod(0o700)
        dirs[:] = [name for name in dirs if not (path / name).is_symlink()]
    shutil.rmtree(owned)
