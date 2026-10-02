#!/usr/bin/env python3
"""Owned native image acceptance. Smoke never substitutes for 100000 images."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import sys
import tempfile
import threading
import time
import uuid
from urllib.parse import unquote, urlsplit

sys.dont_write_bytecode = True
from test_probe_runtime import digest
from test_probe_worker import source_digest as shared_source_digest
from test_scan_memory import (capture, SafeLog, ScanFailure, strict_json, storage_record,
                              mount_filesystem, check_postgres_storage)
from runtime_memory_acceptance import (memory_flags, inspect_owned, container_state,
                                       retain_worker_failure_log)

ROOT = Path(__file__).resolve().parent.parent
BUDGET = "tools/image-memory-budget.json"
MAX_REPORT_BYTES = 2 * 1024 * 1024
MAX_SOURCE_BYTES = 16 * 1024 * 1024
SMOKE_ITEMS, FINAL_ITEMS = 1000, 100000
SCRATCH = "/image-work:rw,noexec,nosuid,nodev,size=64m,mode=0700,uid=65532,gid=65532"
NEGATIVE_CASES = {"unsupported": "jpg", "corrupt": "jpg", "oversized-source": "jpg", "oversized-dimensions": "png"}


class ImageFailure(RuntimeError):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def source_digest():
    names = ("scripts/test_image_memory.py", "scripts/test_image_memory_controller.py",
             "scripts/image_memory_acceptance.py", "scripts/test_image_memory_acceptance.py",
             "scripts/runtime_memory_contracts.py", "Makefile", ".dockerignore", "LICENSE", "docs/LICENSE-COMPLIANCE.md",
             "internal/adapter/images/LICENSE.x-image",
             "scripts/test_scan_memory.py",
             "scripts/scan_memory_acceptance.py", "scripts/container_memory.py",
             "scripts/runtime_memory_acceptance.py", BUDGET)
    record = {name: digest(ROOT / name) for name in names}
    for path in sorted((ROOT / "tools/image-fixture").glob("*.go")):
        record[str(path.relative_to(ROOT))] = digest(path)
    record["sharedSources"] = shared_source_digest()
    return hashlib.sha256(json.dumps(record, sort_keys=True).encode()).hexdigest()


def load_budget():
    from image_memory_acceptance import validate_image_budget
    try:
        with (ROOT / BUDGET).open("rb") as stream:
            body = stream.read(65537)
        budget = strict_json(body, 65536)
        validate_image_budget(budget)
        return budget, hashlib.sha256(body).hexdigest()
    except (OSError, ValueError, ScanFailure):
        raise ImageFailure("image_memory_budget_invalid") from None


def check_fixture_storage(directory, count):
    with Path("/proc/self/mountinfo").open("rb") as stream:
        mounts = stream.read(1024 * 1024 + 1)
    result = capture(["stat", "-f", "-c", "%a %S %d", "--", str(directory)], limit=4096)
    if result.returncode:
        raise ImageFailure("image_memory_storage_invalid")
    # Two files per source, ten sources per directory, root and reserve.
    return storage_record(mount_filesystem(mounts, str(directory)), result.stdout,
                          2 * count + (count + 9) // 10 + 1 + 13 + 10000)


def fixture_kind(index, count):
    if index < 64:
        return "png16"
    return "png" if index < max(100, count // 20) else "jpeg"


def sample_kind(relative, count):
    match = re.fullmatch(r"dir-([0-9]{5})/clip-([0-9]{6})(\.mkv|-poster\.(jpg|png))", relative)
    if match is None:
        raise ImageFailure("image_memory_manifest_invalid")
    directory, index = int(match[1]), int(match[2])
    if index >= count or directory != index // 10:
        raise ImageFailure("image_memory_manifest_invalid")
    if match[3] == ".mkv":
        return "media"
    kind = fixture_kind(index, count)
    if match[4] != ("jpg" if kind == "jpeg" else "png"):
        raise ImageFailure("image_memory_manifest_invalid")
    return kind


def validate_manifest(manifest, count):
    """Validate bounded metadata before interpreting any relative sample path."""
    try:
        boundary = max(100, count // 20)
        counts = {"jpeg": count - boundary, "png": boundary - 64, "png16": 64}
        if (count not in (SMOKE_ITEMS, FINAL_ITEMS) or type(manifest) is not dict or
                set(manifest) != {"version", "count", "directories", "fileCount", "totalBytes", "counts", "templates", "samples", "negativeFiles", "negativeBytes", "negativeSamples"} or
                any(type(manifest[key]) is not int for key in ("version", "count", "directories", "fileCount", "negativeFiles")) or
                manifest["version"] != 1 or manifest["count"] != count or
                manifest["directories"] != count // 10 or manifest["fileCount"] != 2 * count or
                manifest["counts"] != counts or any(type(value) is not int for value in manifest["counts"].values())):
            raise ValueError("counts")
        templates = manifest["templates"]
        if set(templates) != {"jpeg", "png", "png16", "media"}:
            raise ValueError("templates")
        dimensions = {"jpeg": (640, 960), "png": (256, 384), "png16": (4096, 2560), "media": (0, 0)}
        formats = {"jpeg": "jpeg", "png": "png", "png16": "png", "media": "anchor"}
        for kind, entry in templates.items():
            if (set(entry) != {"sha256", "bytes", "width", "height", "format"} or
                    not isinstance(entry["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"]) or
                    type(entry["bytes"]) is not int or not 0 < entry["bytes"] <= MAX_SOURCE_BYTES or
                    type(entry["width"]) is not int or type(entry["height"]) is not int or
                    (entry["width"], entry["height"]) != dimensions[kind] or entry["format"] != formats[kind]):
                raise ValueError("template")
        total = count * templates["media"]["bytes"] + sum(counts[k] * templates[k]["bytes"] for k in counts)
        if type(manifest["totalBytes"]) is not int or manifest["totalBytes"] != total:
            raise ValueError("total")
        samples = manifest["samples"]
        if type(samples) is not list or not 4 <= len(samples) <= 128:
            raise ValueError("samples")
        seen, kinds = set(), set()
        for sample in samples:
            if type(sample) is not dict or set(sample) != {"relativePath", "sha256", "bytes"}:
                raise ValueError("sample")
            relative = sample["relativePath"]
            if not isinstance(relative, str) or relative in seen:
                raise ValueError("sample path")
            kind = sample_kind(relative, count)
            if type(sample["bytes"]) is not int or sample["sha256"] != templates[kind]["sha256"] or sample["bytes"] != templates[kind]["bytes"]:
                raise ValueError("sample identity")
            seen.add(relative)
            kinds.add(kind)
        if kinds != set(templates):
            raise ValueError("sample coverage")
        expected = {"negative/" + kind + "/" + leaf for kind, extension in NEGATIVE_CASES.items()
                    for leaf in ("clip.mkv", "clip-poster." + extension)}
        negatives = manifest["negativeSamples"]
        if type(negatives) is not list or len(negatives) != 8 or manifest["negativeFiles"] != 8:
            raise ValueError("negative count")
        seen = set()
        total = 0
        for sample in negatives:
            if type(sample) is not dict or set(sample) != {"relativePath", "sha256", "bytes"}:
                raise ValueError("negative sample")
            relative = sample["relativePath"]
            if (not isinstance(relative, str) or relative not in expected or relative in seen or
                    type(sample["bytes"]) is not int or not 0 < sample["bytes"] <= MAX_SOURCE_BYTES + 1 or
                    not isinstance(sample["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", sample["sha256"])):
                raise ValueError("negative identity")
            if relative.endswith("/clip.mkv") and (sample["bytes"] != templates["media"]["bytes"] or sample["sha256"] != templates["media"]["sha256"]):
                raise ValueError("negative anchor")
            if relative == "negative/oversized-source/clip-poster.jpg":
                if sample["bytes"] != MAX_SOURCE_BYTES + 1:
                    raise ValueError("source limit fixture")
            elif sample["bytes"] > MAX_SOURCE_BYTES:
                raise ValueError("unexpected oversized fixture")
            seen.add(relative)
            total += sample["bytes"]
        if seen != expected or type(manifest["negativeBytes"]) is not int or manifest["negativeBytes"] != total:
            raise ValueError("negative total")
        return manifest
    except (KeyError, TypeError, ValueError):
        raise ImageFailure("image_memory_manifest_invalid") from None


def fixture_sample(directory, manifest):
    """Read only the validated fixed sample, preserving stat and content evidence."""
    result = []
    root_info = directory.lstat()
    if not stat.S_ISDIR(root_info.st_mode) or root_info.st_mode & 0o222:
        raise ImageFailure("image_memory_fixture_changed")
    for sample in manifest["samples"] + manifest["negativeSamples"]:
        relative = sample["relativePath"]
        if relative.startswith("negative/"):
            limit = MAX_SOURCE_BYTES + 1 if relative == "negative/oversized-source/clip-poster.jpg" else MAX_SOURCE_BYTES
        else:
            sample_kind(relative, manifest["count"])
            limit = MAX_SOURCE_BYTES
        path = directory / relative
        for parent in path.parents:
            parent_info = parent.lstat()
            if not stat.S_ISDIR(parent_info.st_mode) or parent_info.st_mode & 0o222:
                raise ImageFailure("image_memory_fixture_changed")
            if parent == directory:
                break
        before = path.lstat()
        if (not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or
                before.st_mode & 0o222 or before.st_size != sample["bytes"]):
            raise ImageFailure("image_memory_fixture_changed")
        checksum = hashlib.sha256()
        size = 0
        identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_mode, value.st_nlink)
        with os.fdopen(os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)), "rb") as stream:
            if identity(os.fstat(stream.fileno())) != identity(before):
                raise ImageFailure("image_memory_fixture_changed")
            while True:
                block = stream.read(65536)
                if not block:
                    break
                size += len(block)
                if size > limit:
                    raise ImageFailure("image_memory_fixture_changed")
                checksum.update(block)
        after = path.lstat()
        if identity(before) != identity(after) or size != sample["bytes"] or checksum.hexdigest() != sample["sha256"]:
            raise ImageFailure("image_memory_fixture_changed")
        result.append((relative, *identity(after), checksum.hexdigest()))
    return result


def image_identity(body):
    if not isinstance(body, bytes) or not re.fullmatch(rb"sha256:[0-9a-f]{64}\n?", body):
        raise ImageFailure("image_memory_image_identity")
    return body.decode("ascii").strip()


def parse_events(transcript):
    ready, report = False, None
    for line in transcript.splitlines():
        if not line.startswith(b"{"):
            continue
        value = strict_json(line)
        if type(value) is not dict:
            raise ImageFailure("image_memory_events_invalid")
        if "imagesMemoryReadyForSIGTERM" in value:
            if set(value) != {"imagesMemoryReadyForSIGTERM"} or value["imagesMemoryReadyForSIGTERM"] is not True or ready or report is not None:
                raise ImageFailure("image_memory_events_invalid")
            ready = True
        elif "imagesMemoryAcceptance" in value:
            if set(value) != {"imagesMemoryAcceptance"} or report is not None or type(value["imagesMemoryAcceptance"]) is not dict:
                raise ImageFailure("image_memory_events_invalid")
            report = value["imagesMemoryAcceptance"]
    return ready, report


def write_json(path, value):
    body = (json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")
    if len(body) > MAX_REPORT_BYTES:
        raise ImageFailure("image_memory_report_limit")
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as stream:
        stream.write(body)


def remove_owned(kind, name):
    query = (["docker", "container", "ls", "--all", "--filter", "name=^/" + name + "$", "--format", "{{.Names}}"]
             if kind == "container" else
             ["docker", "image", "ls", "--filter", "reference=" + name, "--format", "{{.Repository}}:{{.Tag}}"])

    def present():
        value = capture(query, timeout=15, limit=4096)
        if value.returncode or value.stdout.strip() not in (b"", name.encode("ascii")):
            raise ImageFailure("image_memory_cleanup_unverified")
        return bool(value.stdout.strip())

    if present():
        value = capture(["docker", kind, "rm", "--force", name], timeout=30, limit=4096)
        if value.returncode or present():
            raise ImageFailure("image_memory_cleanup_unverified")


def remove_native(native):
    if (native.parent != Path("/var/tmp").resolve() or not native.name.startswith("jelee-image-memory-") or
            native.is_symlink() or native.resolve() != native):
        raise ImageFailure("image_memory_cleanup_unverified")
    # The generator seals source directories 0555. Only this invocation's
    # unique tree is reopened for cleanup; links are never traversed.
    for directory, children, _files in os.walk(native, followlinks=False):
        path = Path(directory)
        if path.is_symlink():
            raise ImageFailure("image_memory_cleanup_unverified")
        path.chmod(0o700)
        children[:] = [name for name in children if not (path / name).is_symlink()]
    shutil.rmtree(native)


def run_case(*, smoke=False):
    from image_memory_acceptance import validate_image_memory
    identity = uuid.uuid4().hex
    count = SMOKE_ITEMS if smoke else FINAL_ITEMS
    evidence = ROOT / ".testdata" / ("image-memory-" + identity)
    evidence.parent.mkdir(exist_ok=True)
    evidence.mkdir(mode=0o700)
    report = {"version": 1, "scope": "smoke-1000" if smoke else "images-100000", "result": "failed",
              "finalAcceptance": False, "fixtureItems": count, "stage": "preflight"}
    native = log = None
    latest, dsn = b"", ""
    base = "jelee/jelee:image-memory-" + identity
    image, container = base + "-test", "jelee-image-memory-" + identity
    cleanup_container, schema = container + "-cleanup", "jelee_probe_worker_" + identity
    attempted = set()
    cleanup_ready = cleanup_failed = False
    try:
        if sys.platform != "linux":
            raise ImageFailure("image_memory_linux_required")
        dsn = os.environ.get("JELEE_TEST_DATABASE_URL", "")
        parsed = urlsplit(dsn)
        if parsed.scheme not in ("postgres", "postgresql") or parsed.path != "/jelee_test" or not parsed.hostname or any(c in dsn for c in "\r\n\x00"):
            raise ImageFailure("image_memory_database_invalid")
        budget, budget_hash = load_budget()
        before_source = source_digest()
        report.update(sourceDigest=before_source, budgetSha256=budget_hash)
        native_parent = Path("/var/tmp").resolve()
        report["fixtureStorage"] = check_fixture_storage(native_parent, count)
        report["postgresStorage"] = check_postgres_storage(dsn, os.environ.get("JELEE_IMAGE_MEMORY_PG_CONTAINER", ""))
        native = Path(tempfile.mkdtemp(prefix="jelee-image-memory-", dir=native_parent)).resolve()
        log = SafeLog(evidence / "acceptance.txt", (dsn, parsed.password, unquote(parsed.password or ""), ROOT, native, Path.home()))

        def run(argv, timeout=300, check=True, **kwargs):
            result = capture(argv, timeout=timeout, env=kwargs.get("env"))
            log.write(result.stdout.decode("utf-8", errors="replace"))
            log.flush()
            if check and result.returncode:
                raise ImageFailure("image_memory_command_failed")
            return result

        report["stage"] = "build"
        attempted.add(("image", base))
        run(["docker", "build", "--network", "host", "-t", base, "."], timeout=900)
        report["productionImage"] = image_identity(run(["docker", "image", "inspect", base, "--format", "{{.Id}}"], timeout=15).stdout)
        build = native / "build"
        build.mkdir(mode=0o700)
        env = dict(os.environ, CGO_ENABLED="0")
        for key in ("JELEE_TEST_DATABASE_URL", "JELEE_DATABASE_URL"):
            env.pop(key, None)
        sdk = run([str(ROOT / ".bin/go"), "version"], env=env, timeout=30).stdout
        if not re.fullmatch(rb"go version go[0-9]+\.[0-9]+\.[0-9]+ linux/amd64\n?", sdk):
            raise ImageFailure("image_memory_sdk_identity")
        report["sdk"] = {"goVersion": sdk.decode("ascii").strip(), "manifestSha256": digest(ROOT / "tools/manifest.json")}
        run([str(ROOT / ".bin/go"), "test", "-trimpath", "-tags", "jelee_probe_tests", "-c", "-o", str(build / "worker.test"), "./internal/platform/runtime"], env=env, timeout=900)
        report["testBinarySha256"] = digest(build / "worker.test")
        (build / "Dockerfile").write_text("FROM " + base + "\nCOPY --chmod=0555 worker.test /worker.test\n", encoding="utf-8")
        attempted.add(("image", image))
        run(["docker", "build", "--network", "none", "-t", image, str(build)], timeout=300)
        report["testImage"] = image_identity(run(["docker", "image", "inspect", image, "--format", "{{.Id}}"], timeout=15).stdout)
        generator = native / "image-fixture"
        run([str(ROOT / ".bin/go"), "build", "-trimpath", "-o", str(generator), "./tools/image-fixture"], env=env, timeout=300)
        report["fixtureGeneratorSha256"] = digest(generator)
        report["stage"] = "fixture"
        inputs = native / "media"
        inputs.mkdir(mode=0o700)
        generated = run([str(generator), "--root", str(inputs), "--count", str(count)], timeout=900)
        manifest = validate_manifest(strict_json(generated.stdout), count)
        write_json(evidence / "fixture-manifest.json", manifest)
        report.update(fixtureManifestSha256=digest(evidence / "fixture-manifest.json"), fixtureCounts=manifest["counts"], fixtureBytes=manifest["totalBytes"])
        originals = fixture_sample(inputs, manifest)
        secret = native / "database.env"
        with os.fdopen(os.open(secret, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8") as stream:
            stream.write("JELEE_TEST_DATABASE_URL=" + dsn + "\nJELEE_PROBE_TEST_SCHEMA=" + schema +
                         "\nJELEE_IMAGES_MEMORY_ACCEPTANCE=true\nJELEE_IMAGES_MEMORY_ITEMS=" + str(count) + "\n")
        cleanup_ready = True
        report["stage"] = "workload"
        attempted.add(("container", container))
        run(["docker", "run", "-d", "--name", container, "--user", "65532:65532", "--read-only", "--network", "host",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges", *memory_flags(100), "--env", "GOMAXPROCS=2",
             "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--tmpfs", SCRATCH,
             "--env-file", str(secret), "--volume", str(inputs) + ":/media:ro", "--entrypoint", "/worker.test", image,
             "-test.v", "-test.run", "^TestImagesMemoryAcceptance$", "-test.timeout", "1h"], timeout=30)
        deadline = time.monotonic() + 3600
        signalled, prior_ready, prior_final = False, False, None
        while time.monotonic() < deadline:
            inspected = inspect_owned(container)
            value = capture(["docker", "logs", container], timeout=15)
            if value.returncode:
                raise ImageFailure("image_memory_log_unavailable")
            latest = value.stdout
            ready, final = parse_events(latest)
            if prior_ready and not ready or prior_final is not None and prior_final != final:
                raise ImageFailure("image_memory_events_invalid")
            prior_ready, prior_final = ready, final
            if ready and not signalled:
                run(["docker", "kill", "--signal", "SIGTERM", container], timeout=15)
                signalled = True
            if not inspected["State"]["Running"]:
                log.write(latest.decode("utf-8", errors="replace")); log.flush()
                report["containerExit"] = container_state(container)
                state = inspected["State"]
                if (not signalled or final is None or b"--- SKIP:" in latest or b"\nPASS\n" not in latest or
                        state["ExitCode"] != 0 or state["OOMKilled"] is not False):
                    raise ImageFailure("image_memory_acceptance_incomplete")
                report["stage"] = "validation"
                write_json(evidence / "acceptance-report.private.json", final)
                report["rawAcceptanceSha256"] = digest(evidence / "acceptance-report.private.json")
                report["validation"] = validate_image_memory(final, inspected, budget, expected_items=count)
                break
            time.sleep(1)
        else:
            raise ImageFailure("image_memory_workload_timeout")
        if fixture_sample(inputs, manifest) != originals:
            raise ImageFailure("image_memory_fixture_changed")
        if source_digest() != before_source:
            raise ImageFailure("image_memory_source_changed")
        report.update(result="passed", stage="complete", sourceUnchanged=True,
                      originalSamplesUnchanged=True, finalAcceptance=not smoke)
    except (Exception, KeyboardInterrupt) as error:
        code = error.code if isinstance(error, (ImageFailure, ScanFailure)) else "image_memory_acceptance_failed"
        report.update(result="failed", finalAcceptance=False, failureCode=code.replace("scan_memory_", "image_memory_"))
    finally:
        handlers = {}
        if threading.current_thread() is threading.main_thread():
            handlers = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGTERM, signal.SIGINT)}
        if log is not None and report["result"] != "passed" and ("container", container) in attempted:
            report["workerStateBeforeCleanup"] = container_state(container)
            try:
                report["workerFailureLog"] = retain_worker_failure_log(run, log, container, latest.decode("utf-8", errors="replace"), dsn)
            except (ImageFailure, ScanFailure, OSError):
                report["workerFailureLog"] = {"retentionFailed": True}
        if ("container", container) in attempted:
            try:
                remove_owned("container", container)
            except Exception:
                cleanup_failed = True
        if cleanup_ready:
            attempted.add(("container", cleanup_container))
            try:
                result = capture(["docker", "run", "--name", cleanup_container, "--network", "host", "--read-only", "--cap-drop", "ALL",
                                  "--security-opt", "no-new-privileges", "--memory", "256m", "--pids-limit", "32", "--env-file", str(secret),
                                  "--entrypoint", "/worker.test", image, "--cleanup-probe-worker"], timeout=30, limit=4096)
                cleanup_failed |= result.returncode != 0
            except Exception:
                cleanup_failed = True
        for kind, name in (("container", cleanup_container), ("image", image), ("image", base)):
            if (kind, name) in attempted:
                try:
                    remove_owned(kind, name)
                except Exception:
                    cleanup_failed = True
        if log is not None:
            try:
                log.close()
                report["transcriptSha256"] = digest(evidence / "acceptance.txt")
            except OSError:
                cleanup_failed = True
        if native is not None:
            try:
                remove_native(native)
            except Exception:
                cleanup_failed = True
        report["testArtifactsCleaned"] = not cleanup_failed
        if cleanup_failed:
            report.update(result="failed", finalAcceptance=False, cleanupFailure=True)
        try:
            write_json(evidence / "summary.json", report)
        finally:
            for sig, handler in handlers.items():
                signal.signal(sig, handler)
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run real image processing acceptance; smoke is not final scale evidence.")
    parser.add_argument("--smoke", action="store_true", help="process 1000 sources; never claim final 100000 acceptance")
    args = parser.parse_args(argv)
    interrupted_once = False

    def interrupted(_signum, _frame):
        nonlocal interrupted_once
        if not interrupted_once:
            interrupted_once = True
            raise ImageFailure("image_memory_controller_interrupted")

    previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        result = run_case(smoke=args.smoke)
    except (OSError, ValueError, ImageFailure, ScanFailure):
        print('{"result":"failed","errorCode":"image_memory_controller_failed"}')
        return 1
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    print(json.dumps({key: result[key] for key in ("scope", "result", "fixtureItems", "finalAcceptance", "testArtifactsCleaned")}))
    return 0 if result["result"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
