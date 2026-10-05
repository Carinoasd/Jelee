#!/usr/bin/env python3
"""Required Linux amd64 sandbox proof; use only project-pinned tools and fixtures.

Prerequisites: bootstrap/verify Go, media tools and runtime, make fixtures, Docker.
Only this invocation's temporary directory, container, and image are removed.
The sanitized command transcript is retained in .testdata/sandbox-native.txt.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import signal
import stat
import subprocess
import sys
import tempfile
import uuid

sys.dont_write_bytecode = True
import toolchain

ROOT = Path(__file__).resolve().parent.parent
PACKAGE = "github.com/MoYuanCN/Jelee/internal/platform/sandbox"
SELECTED = "Test(Native|Real|Pinned|Protected|New|Descriptor|Launcher|Policy|Syscall|Cover|Tool|OCR)"
# Developer proofs that hash this host's glibc and a source checkout's tools
# only after an explicit JELEE_*_HOST_RUNTIME opt-in. The proof image has
# neither, so they are outside this suite rather than counted as skips; the
# zero-skip rule still holds for every selected test.
HOST_RUNTIME_ONLY = "^Test(RealMatroskaToolsInSandboxWithExplicitHostRuntime|RealTesseractInSandboxWithExplicitHostRuntime)$"


def open_regular(path, maximum):
    # Nonblocking and no-follow make a replaced leaf FIFO/symlink fail closed.
    descriptor = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_size > maximum:
            raise ValueError("input is not a bounded regular file")
        return os.fdopen(descriptor, "rb")
    except BaseException:
        os.close(descriptor)
        raise


def bounded_chunks(source, maximum):
    before, total = os.fstat(source.fileno()), 0
    while True:
        chunk = source.read(min(65536, maximum - total + 1))
        if not chunk:
            break
        total += len(chunk)
        if total > maximum:
            raise ValueError("input exceeded the streaming size limit")
        yield chunk
    after = os.fstat(source.fileno())
    if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns) or total != before.st_size:
        raise ValueError("input changed during streaming")


def digest(path, maximum=512 << 20):
    result = hashlib.sha256()
    with open_regular(path, maximum) as source:
        for chunk in bounded_chunks(source, maximum):
            result.update(chunk)
    return result.hexdigest()


def local(path):
    return toolchain.local_path(ROOT, path)


def relative(value):
    if not isinstance(value, str) or not value or "\\" in value or ":" in value:
        raise ValueError("invalid project-relative manifest path")
    if any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise ValueError("invalid project-relative manifest path")
    if any(part in ("", ".", "..") for part in value.split("/")):
        raise ValueError("invalid project-relative manifest path")
    return value


def checked_file(path, expected, maximum=512 << 20):
    path = local(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > maximum:
        raise ValueError("pinned input is not a bounded regular file")
    if not isinstance(expected, str) or not re.fullmatch("[0-9a-f]{64}", expected):
        raise ValueError("invalid manifest digest")
    if digest(path, maximum) != expected:
        raise ValueError("pinned input checksum mismatch")
    return path


def verified_copy(source, destination, expected, maximum=512 << 20):
    source = checked_file(source, expected, maximum)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with open_regular(source, maximum) as reader, destination.open("xb") as writer:
        for chunk in bounded_chunks(reader, maximum):
            writer.write(chunk)
    if digest(destination, maximum) != expected:
        raise ValueError("staged input checksum mismatch")


def select_fixtures(requested, media):
    parent = local(ROOT / ".testfixtures")
    if requested:
        source = local(ROOT / requested)
        if source.parent != parent or not source.name.startswith("media-linux-amd64-"):
            raise ValueError("fixtures must be a generated Linux directory in .testfixtures")
    else:
        choices = [local(path) for path in parent.glob("media-linux-amd64-*")
                   if path.is_dir() and (path / "fixtures.json").is_file()]
        if not choices:
            raise ValueError("generate Linux fixtures with make fixtures first")
        source = max(choices, key=lambda path: (path / "fixtures.json").stat().st_mtime_ns)
    record_path = local(source / "fixtures.json")
    if record_path.stat().st_size > 64 << 10:
        raise ValueError("fixture record is too large")
    record = json.loads(record_path.read_text(encoding="utf-8"))
    if (record.get("schemaVersion") != 1 or record.get("platform") != "linux-amd64"
            or record.get("toolVersion") != media["vendorVersion"]
            or record.get("toolSHA256") != media["executables"]["ffmpeg"]["sha256"]):
        raise ValueError("fixtures were not generated with the current pinned Linux tool")
    files = record.get("files")
    if not isinstance(files, list) or not 2 <= len(files) <= 64:
        raise ValueError("invalid fixture inventory")
    hashes, total = {}, 0
    for item in files:
        name = relative(item["name"])
        if "/" in name or name in hashes or name == "fixtures.json":
            raise ValueError("invalid or duplicate fixture filename")
        path = checked_file(source / name, item["sha256"], 4 << 20)
        size = path.stat().st_size
        if type(item["bytes"]) is not int or size != item["bytes"]:
            raise ValueError("fixture size differs from its record")
        total += size
        hashes[name] = item["sha256"]
    if total > 16 << 20 or not {"multi.mkv", "video-180p.mp4"} <= hashes.keys():
        raise ValueError("fixture set is incomplete or oversized")
    if {path.name for path in source.iterdir()} != set(hashes) | {"fixtures.json"}:
        raise ValueError("fixture directory differs from its recorded inventory")
    hashes["fixtures.json"] = digest(record_path)
    return source, hashes


class Transcript:
    def __init__(self, stream):
        self.stream = stream

    def write(self, value):
        self.stream.write(str(value).replace(str(ROOT), "$PROJECT") + "\n")
        self.stream.flush()

    def run(self, argv, *, env=None, input=None, timeout=300, check=True):
        self.write("COMMAND " + json.dumps([str(value) for value in argv], ensure_ascii=False))
        result = subprocess.run(argv, cwd=ROOT, env=env, input=input, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                timeout=timeout)
        self.write(result.stdout)
        self.write("EXIT " + str(result.returncode))
        if check and result.returncode:
            raise RuntimeError("native sandbox command failed; see .testdata/sandbox-native.txt")
        return result


def source_hashes():
    sources = {ROOT / "go.mod", ROOT / "go.sum", ROOT / "tools/manifest.json",
               ROOT / "scripts/test_sandbox_native.py", ROOT / "scripts/toolchain.py"}
    for directory in ("internal/platform/sandbox", "internal/platform/process"):
        sources.update((ROOT / directory).rglob("*.go"))
        sources.update((ROOT / directory).rglob("*.s"))
    return {str(path.relative_to(ROOT)): digest(path) for path in sorted(sources)}


def execute(temporary, transcript, requested):
    manifest = json.loads((ROOT / "tools/manifest.json").read_text(encoding="utf-8"))
    media, runtime = manifest["mediaTools"]["platforms"]["linux-amd64"], manifest["mediaRuntime"]
    if runtime.get("readyToExecute") is not True:
        raise ValueError("runtime pins are not ready")
    base_spec = next(item for item in manifest["containerBuildDependencies"]
                     if item["name"] == "golang-build-image")
    base = base_spec["image"]
    if not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", base) or not base.endswith(base_spec["sha256"]):
        raise ValueError("test base image must have an exact manifest digest")
    fixtures, originals = select_fixtures(requested, media)
    before = source_hashes()
    transcript.write("SOURCE SHA256 " + json.dumps(before, sort_keys=True))
    transcript.write("FIXTURES " + str(fixtures.relative_to(ROOT)))
    transcript.write("FIXTURE SHA256 " + json.dumps(originals, sort_keys=True))
    transcript.run([sys.executable, "-B", "scripts/toolchain.py", "verify"])
    _, spec, target = toolchain.selected()
    if target != "linux-amd64":
        raise ValueError("native sandbox proof requires Linux amd64")
    _, install = toolchain.paths(spec)
    go = local(install / spec["executable"])
    env = toolchain.go_environment(spec)
    tmp = temporary / "tmp"
    tmp.mkdir()
    env.update(CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOAMD64="v1",
               GOFLAGS="", GOWORK="off", GOEXPERIMENT="", GOTMPDIR=str(tmp), TMPDIR=str(tmp))
    env.pop("GOCOVERDIR", None)
    stage = temporary / "image"
    stage.mkdir()
    builds = [
        ("helper", ["build", "-trimpath"], "./internal/platform/sandbox/testdata/helper"),
        ("thread", ["build", "-trimpath"], "./internal/platform/sandbox/testdata/threadlimit"),
        ("coverage-helper", ["build", "-cover", "-covermode=atomic", "-coverpkg=" + PACKAGE + "," + PACKAGE + "/testdata/helper"], "./internal/platform/sandbox/testdata/helper"),
        ("sandbox.test", ["test", "-cover", "-covermode=atomic", "-c"], "./internal/platform/sandbox"),
        ("process.test", ["test", "-cover", "-covermode=atomic", "-c"], "./internal/platform/process"),
        ("covdata", ["build"], "cmd/covdata"),
    ]
    for name, arguments, package in builds:
        transcript.run([str(go)] + arguments + ["-o", str(stage / name), package], env=env)
        transcript.write("BUILT SHA256 " + name + " " + digest(stage / name))
    libraries = []
    runtime_base = ROOT / ".tools" / relative(runtime["installPath"])
    for package in runtime["packages"]:
        for item in package.get("files", []):
            destination = relative(item["destination"])
            verified_copy(runtime_base / destination, stage / "runtime" / destination, item["sha256"])
            if item["kind"] == "elf":
                libraries.append({"Path": "/" + destination, "SHA256": item["sha256"]})
    for item in runtime["licenseTexts"]:
        destination = relative(item["destination"])
        verified_copy(runtime_base / destination, stage / "runtime" / destination, item["sha256"])
    tool, tool_base = media["executables"]["ffprobe"], ROOT / ".tools" / relative(media["installPath"])
    verified_copy(tool_base / relative(tool["path"]), stage / "real-ffprobe", tool["sha256"])
    for index, item in enumerate(media["licenseFiles"]):
        verified_copy(tool_base / relative(item["path"]), stage / "runtime" / "licenses" / ("ffprobe-" + str(index) + ".txt"), item["sha256"])
    inputs = []
    for name in ("multi.mkv", "video-180p.mp4"):
        verified_copy(fixtures / name, stage / "fixtures" / name, originals[name], 4 << 20)
        inputs.append("/project/fixtures/" + name)
    profile = {"Profile": {"FFprobePath": "/project/real-tool/ffprobe"}, "Policy": {
        "FFprobeSHA256": tool["sha256"], "Libraries": libraries, "RequireProtectedFiles": True}, "Inputs": inputs}
    (stage / "real-profile.json").write_text(json.dumps(profile), encoding="utf-8")
    transcript.write("VERIFIED PROFILE " + json.dumps(profile, sort_keys=True))
    # The container's only writable path (TMPDIR) is a noexec tmpfs, so every
    # fixture that must actually execute is shipped in the image.
    dockerfile = f"""FROM {base}
COPY --chmod=0555 sandbox.test /project/sandbox.test
COPY --chmod=0555 process.test /project/process.test
COPY --chmod=0555 covdata /project/covdata
COPY --chmod=0555 helper /project/ffprobe
COPY --chmod=0555 thread /project/thread-fixture/ffprobe
COPY --chmod=0555 coverage-helper /project/coverage-helper/ffprobe
COPY --chmod=0555 helper /project/protected/ffprobe
COPY --chmod=0555 thread /project/protected/other
COPY --chmod=0555 helper /project/unsafe-parent/ffprobe
COPY --chmod=0555 helper /project/tool-fixture/mkvextract
COPY --chmod=0555 runtime/ /
COPY --chmod=0555 real-ffprobe /project/real-tool/ffprobe
COPY --chmod=0555 fixtures/ /project/fixtures/
COPY --chmod=0444 real-profile.json /project/real-profile.json
RUN chmod 0777 /project/unsafe-parent
"""
    transcript.write("TEST IMAGE RECIPE\n" + dockerfile)
    identity = uuid.uuid4().hex
    image, container = "jelee/sandbox-native-test:" + identity, "jelee-sandbox-native-" + identity
    cleanup_failed = False
    try:
        transcript.run(["docker", "build", "--pull=false", "--network", "none", "--tag", image, "--file", "-", str(stage)], input=dockerfile)
        command = f"""set -eu
mkdir -p /project/.testdata/parent /project/.testdata/child /project/.testdata/merged
/project/sandbox.test -test.v -test.timeout=3m '-test.run={SELECTED}' '-test.skip={HOST_RUNTIME_ONLY}' -test.gocoverdir=/project/.testdata/parent
echo COVERAGE_PARENT
/project/covdata percent -i=/project/.testdata/parent
echo COVERAGE_CHILD
/project/covdata percent -i=/project/.testdata/child
/project/covdata merge -i=/project/.testdata/parent,/project/.testdata/child -o=/project/.testdata/merged
echo COVERAGE_MERGED
/project/covdata percent -i=/project/.testdata/merged
/project/covdata func -i=/project/.testdata/merged
/project/process.test -test.v -test.timeout=3m
"""
        arguments = ["docker", "run", "--rm", "--name", container, "--network", "none", "--user", "65532:65532", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--security-opt", "seccomp=unconfined", "--pids-limit", "256", "--memory", "768m", "--cpus", "2", "--tmpfs", "/project/.testdata:rw,nosuid,nodev,noexec,size=268435456,mode=0700,uid=65532,gid=65532", "--workdir", "/project"]
        for key, value in {"TMPDIR": "/project/.testdata", "JELEE_REQUIRE_SANDBOX_TEST": "true", "JELEE_SANDBOX_TEST_HELPER": "/project/ffprobe", "JELEE_SANDBOX_EXTRACT_FIXTURE": "/project/tool-fixture/mkvextract", "JELEE_SANDBOX_THREAD_FIXTURE": "/project/thread-fixture/ffprobe", "JELEE_SANDBOX_PROTECTED_TEST_DIR": "/project/protected", "JELEE_SANDBOX_COVERAGE_HELPER": "/project/coverage-helper/ffprobe", "JELEE_SANDBOX_COVERAGE_DIR": "/project/.testdata/child", "JELEE_SANDBOX_REAL_PROFILE": "/project/real-profile.json", "GOCACHE": "/project/.testdata/gocache", "GOMODCACHE": "/project/.testdata/gomodcache", "GOTOOLCHAIN": "local", "GOENV": "off"}.items():
            arguments += ["--env", key + "=" + value]
        result = transcript.run(arguments + ["--entrypoint", "/bin/sh", image, "-c", command], timeout=390)
        if "--- SKIP:" in result.stdout or result.stdout.count("\nPASS\n") != 2:
            raise RuntimeError("required native suites failed or skipped")
        merged = result.stdout.split("COVERAGE_MERGED\n", 1)[1]
        matched = re.search(re.escape(PACKAGE) + r"\s+coverage:\s+([0-9.]+)%", merged)
        if not matched or float(matched.group(1)) < 85:
            raise RuntimeError("merged production sandbox coverage is below 85 percent")
        for name, expected in originals.items():
            checked_file(fixtures / name, expected, 4 << 20)
        if before != source_hashes():
            raise RuntimeError("source changed during native proof; rerun against a stable tree")
        transcript.write("PASS: zero skips; original fixtures unchanged; sandbox merged coverage " + matched.group(1) + "%")
    finally:
        # The UUID names are created only by this invocation. Never prune shared
        # Docker state or remove the earlier fixed-name development test image.
        for kind, name in (("container", container), ("image", image)):
            exists = transcript.run(["docker", kind, "inspect", "--format", "{{.Id}}", name], check=False)
            if exists.returncode == 0:
                removed = transcript.run(["docker", kind, "rm", "--force", name], check=False)
                cleanup_failed = cleanup_failed or removed.returncode != 0
        if cleanup_failed:
            raise RuntimeError("owned test container or image cleanup failed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixtures", help="generated Linux fixture directory, relative to the project")
    arguments = parser.parse_args()
    if sys.platform != "linux" or platform.machine().lower() not in ("x86_64", "amd64"):
        parser.error("native sandbox proof requires Linux amd64")
    parent = local(ROOT / ".testdata")
    parent.mkdir(exist_ok=True)
    destination = local(parent / "sandbox-native.txt")
    with tempfile.TemporaryDirectory(prefix="sandbox-native-", dir=parent) as directory:
        temporary = local(Path(directory))
        transcript_path = temporary / "transcript.txt"
        try:
            with transcript_path.open("x", encoding="utf-8") as output:
                transcript = Transcript(output)
                transcript.write("Jelee native Linux sandbox proof: CGO_ENABLED=0; no race-detector claim.")
                transcript.write("Actual child counter bytes are retained unchanged and merged by the pinned Go SDK.")
                try:
                    execute(temporary, transcript, arguments.fixtures)
                except BaseException as error:
                    transcript.write("FAILED " + type(error).__name__ + ": " + str(error))
                    raise
        finally:
            # Publish the complete transcript atomically, including failures.
            # Concurrent runs own separate workspaces; the latest log wins.
            if transcript_path.exists():
                os.replace(transcript_path, destination)
    print("PASS native sandbox proof; owned temporary artifacts cleaned; .testdata/sandbox-native.txt")


def terminate(signum, _frame):
    # A normal CI cancellation still unwinds the owned-resource cleanup.
    raise SystemExit(128 + signum)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, terminate)
    try:
        main()
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError, KeyError, TypeError) as error:
        print("native sandbox proof failed: " + str(error), file=sys.stderr)
        sys.exit(1)
