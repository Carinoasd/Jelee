#!/usr/bin/env python3
"""Actual Linux-only, disposable, isolated probe image acceptance."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import uuid

ROOT = Path(__file__).resolve().parent.parent
IMAGE = "jelee/jelee:probe-runtime-test"


def run(argv, expected=0, **kwargs):
    completed = subprocess.run(argv, cwd=ROOT, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, timeout=300, **kwargs)
    if completed.returncode != expected:
        raise RuntimeError("probe runtime command failed; consult ignored acceptance log")
    return completed.stdout


def digest(path):
    with path.open("rb") as source:
        return digest_stream(source)


def digest_stream(source):
    value = hashlib.sha256()
    for block in iter(lambda: source.read(64 << 10), b""):
        value.update(block)
    return value.hexdigest()


def inspect_image(image, pins):
    name = "jelee-probe-inspect-" + uuid.uuid4().hex[:12]
    run(["docker", "create", "--name", name, image])
    try:
        child = subprocess.Popen(["docker", "export", name], stdout=subprocess.PIPE)
        found, all_files = set(), []
        try:
            with tarfile.open(fileobj=child.stdout, mode="r|") as archive:
                for entry in archive:
                    if not entry.isfile():
                        continue
                    all_files.append(entry.name)
                    if entry.name not in pins:
                        continue
                    if entry.uid != 0 or entry.mode & 0o222:
                        raise RuntimeError("runtime image has writable or unprotected pinned file")
                    file = archive.extractfile(entry)
                    actual = digest_stream(file)
                    if actual != pins[entry.name]:
                        raise RuntimeError("runtime image digest differs from manifest")
                    found.add(entry.name)
            if child.wait(timeout=30) != 0:
                raise RuntimeError("runtime image export failed")
        finally:
            child.stdout.close()
            if child.poll() is None:
                child.kill(); child.wait()
        if found != set(pins) or any(Path(name).name in ("ffmpeg", "sh", "bash", "go") for name in all_files):
            raise RuntimeError("runtime image inventory is incomplete or contains forbidden tools")
        return len(found)
    finally:
        run(["docker", "rm", name])


def main():
    if os.name != "posix":
        raise RuntimeError("Linux runtime acceptance requires native Linux Docker")
    manifest = json.loads((ROOT / "tools/manifest.json").read_text())
    media = manifest["mediaTools"]["platforms"]["linux-amd64"]
    runtime = manifest["mediaRuntime"]
    if not runtime["readyToExecute"]:
        raise RuntimeError("runtime pins are not ready")
    source_roots = sorted((ROOT / ".testfixtures").glob("media-linux-amd64-*"))
    if not source_roots:
        raise RuntimeError("generate original fixtures first")
    source_root = max(source_roots, key=lambda path: (path / "fixtures.json").stat().st_mtime_ns)
    if source_root.is_symlink():
        raise RuntimeError("acceptance fixture root must not be a symlink")
    source_record = json.loads((source_root / "fixtures.json").read_text())
    if source_record.get("platform") != "linux-amd64" or source_record.get("toolVersion") != media["vendorVersion"] or source_record.get("toolSHA256") != media["executables"]["ffmpeg"]["sha256"] or len(source_record.get("files", [])) != 13:
        raise RuntimeError("original fixture generator identity differs from manifest")
    # The generator's record contains only its original tiny files.
    originals = {}
    for record in source_record["files"]:
        name = record["name"]
        file = source_root / name
        if not name or name in (".", "..") or Path(name).name != name or name in originals or file.is_symlink() or not file.is_file() or file.stat().st_size > 4 << 20 or file.stat().st_size != record["bytes"] or digest(file) != record["sha256"]:
            raise RuntimeError("original fixture does not match its verified record")
        originals[name] = record["sha256"]
    pins = {"usr/lib/jelee/ffprobe": media["executables"]["ffprobe"]["sha256"],
            "licenses/ffprobe/LICENSE.txt": media["licenseFiles"][0]["sha256"]}
    for package in runtime["packages"]:
        pins.update({file["destination"]: file["sha256"] for file in package["files"]})
    pins.update({file["destination"]: file["sha256"] for file in runtime["licenseTexts"]})
    log_path = ROOT / ".testdata/probe-runtime-build.txt"
    with log_path.open("wb") as log:
        built = subprocess.run(["docker", "build", "--network", "host", "--tag", IMAGE, "."],
                               cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, timeout=300)
    if built.returncode:
        raise RuntimeError("runtime image build failed (see ignored build log)")
    verified_files = inspect_image(IMAGE, pins)
    image_id = run(["docker", "image", "inspect", IMAGE, "--format", "{{.Id}}"]).decode().strip()
    security = ["--rm", "--read-only", "--network", "none", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges", "--memory", "512m", "--pids-limit", "128",
                "--tmpfs", "/tmp:rw,noexec,nosuid,size=32m,mode=1777"]
    diagnostics = run(["docker", "run"] + security + ["--env", "JELEE_DATABASE_URL=invalid-no-db-diagnostic",
                       "--entrypoint", "/jelee-cli", IMAGE, "doctor", "probe"])
    diagnostic = json.loads(diagnostics)
    if diagnostic["capability"] != "available" or diagnostic["reason"] != "isolated_helper_verified":
        raise RuntimeError("sandbox health check failed")
    with tempfile.TemporaryDirectory(prefix="probe-runtime-accept-", dir=ROOT / ".testdata") as temporary:
        temporary = Path(temporary)
        fixtures = temporary / "media"
        fixtures.mkdir()
        for name in ("video-180p.mp4", "video-360p.mp4", "multi.mkv", "corrupt.mkv"):
            shutil.copyfile(source_root / name, fixtures / name)
        (fixtures / "external.concat").write_text("ffconcat version 1.0\nfile 'file:/etc/passwd'\n")
        (fixtures / "remote.m3u8").write_text("#EXTM3U\n#EXTINF:1,\nhttp://127.0.0.1:9/private\n#EXT-X-ENDLIST\n")
        before = {file.name: digest(file) for file in fixtures.iterdir()}
        env = dict(os.environ, CGO_ENABLED="0")
        run([str(ROOT / ".bin/go"), "build", "-tags", "jelee_probe_tests", "-trimpath",
             "-o", str(temporary / "probe-smoke"), "./tools/probe-smoke"], env=env)
        (temporary / "Dockerfile").write_text("FROM "+IMAGE+"\nCOPY --chmod=0555 probe-smoke /probe-smoke\n")
        test_image = "jelee/jelee:probe-runtime-smoke-" + uuid.uuid4().hex[:12]
        run(["docker", "build", "--tag", test_image, str(temporary)])
        try:
            smoke = json.loads(run(["docker", "run"] + security + ["--volume", str(fixtures)+":/media:ro",
                                     "--entrypoint", "/probe-smoke", test_image]))
            if smoke != {"cases": 6, "result": "isolated_normalization_passed"}:
                raise RuntimeError("isolated normalization failed")
            (temporary / "deny-landlock.json").write_text(json.dumps({"defaultAction": "SCMP_ACT_ALLOW", "syscalls": [{"names": ["landlock_create_ruleset"], "action": "SCMP_ACT_ERRNO", "errnoRet": 38}]}))
            denied = json.loads(run(["docker", "run"] + security + ["--security-opt", "seccomp="+str(temporary / "deny-landlock.json"), "--entrypoint", "/jelee-cli", IMAGE, "doctor", "probe"], expected=1))
            (temporary / "bad-lib").write_bytes(b"synthetic invalid runtime library\n")
            corrupt = json.loads(run(["docker", "run"] + security + ["--volume", str(temporary / "bad-lib")+":/lib/x86_64-linux-gnu/libc.so.6:ro", "--entrypoint", "/jelee-cli", IMAGE, "doctor", "probe"], expected=1))
            missing = json.loads(run(["docker", "run"] + security + ["--tmpfs", "/usr/lib/jelee:ro,size=1m", "--entrypoint", "/jelee-cli", IMAGE, "doctor", "probe"], expected=1))
            if any(item["capability"] != "disabled" for item in (denied, corrupt, missing)):
                raise RuntimeError("runtime capability did not fail closed")
            if before != {file.name: digest(file) for file in fixtures.iterdir()}:
                raise RuntimeError("runtime probe changed acceptance input bytes")
        finally:
            run(["docker", "image", "rm", test_image])
    if originals != {name: digest(source_root / name) for name in originals}:
        raise RuntimeError("runtime acceptance changed original generator files")
    print(json.dumps({"image": image_id, "pinnedFiles": verified_files, "cases": 6,
                      "healthyHelper": True, "missingToolDisabled": True,
                      "alteredLibraryDisabled": True, "landlockDeniedDisabled": True,
                      "originalHashesUnchanged": True, "testArtifactsCleaned": True}))


if __name__ == "__main__":
    main()
