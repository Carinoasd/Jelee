#!/usr/bin/env python3
"""Run real mixed image/scan soak in an owned container with streaming receipts.

The start_images_soak launcher supplies a frozen committed source directory.
"""
import hashlib
import os
from pathlib import Path
import re
import signal
import sys
import tempfile
import threading
import uuid
from urllib.parse import unquote, urlsplit

sys.dont_write_bytecode = True
from test_image_memory import (ROOT, SMOKE_ITEMS, SCRATCH, ImageFailure, source_digest,
    check_fixture_storage, image_identity, validate_manifest, fixture_sample,
    write_json, remove_owned, remove_native)
from test_probe_runtime import digest
from test_scan_memory import capture, SafeLog, ScanFailure, strict_json, check_postgres_storage
from runtime_memory_acceptance import memory_flags, inspect_owned, container_state
from images_soak_acceptance import validate_soak_budget, validate_soak_log
from images_soak_monitor import MonitorFailure, follow, atomic_status
from images_soak_snapshot import SnapshotFailure


def load_soak_budget():
    with (ROOT / "tools/image-soak-budget.json").open("rb") as stream:
        body = stream.read(65537)
    budget = validate_soak_budget(strict_json(body, 65536))
    return budget, hashlib.sha256(body).hexdigest()


def run_case(*, smoke=False, evidence_root=None, identity=None, snapshot=None):
    identity = identity or uuid.uuid4().hex
    if not re.fullmatch(r"[a-f0-9]{32}", identity):
        raise ImageFailure("soak_identity_invalid")
    count = SMOKE_ITEMS
    evidence = (evidence_root or ROOT / ".testdata") / ("image-soak-" + identity)
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.mkdir(mode=0o700)
    report = {"version": 1, "scope": "smoke" if smoke else "formal", "result": "failed",
              "finalAcceptance": False, "fixtureItems": count, "stage": "preflight"}
    native = log = None
    dsn = ""
    base = "jelee/jelee:image-soak-" + identity
    image, container = base + "-test", "jelee-image-memory-" + identity
    cleanup_container, schema = container + "-cleanup", "jelee_probe_worker_" + identity
    attempted = set()
    cleanup_ready = cleanup_failed = False
    try:
        if sys.platform != "linux":
            raise ImageFailure("image_memory_linux_required")
        if Path.cwd().resolve() != ROOT.resolve():
            raise ImageFailure("soak_working_directory_invalid")
        if snapshot is not None:
            from images_soak_snapshot import verify
            verify(ROOT, snapshot["files"])
            report.update(sourceCommit=snapshot["commit"], sourceTree=snapshot["tree"])
        dsn = os.environ.get("JELEE_TEST_DATABASE_URL", "")
        parsed = urlsplit(dsn)
        if parsed.scheme not in ("postgres", "postgresql") or parsed.path != "/jelee_test" or not parsed.hostname or any(c in dsn for c in "\r\n\x00"):
            raise ImageFailure("image_memory_database_invalid")
        budget, budget_hash = load_soak_budget()
        before_source = source_digest()
        atomic_status(evidence / "status.json", {"stage": "build", "controllerPid": os.getpid()})
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
        # BuildKit parses bare sha256:IDs as repository tags in FROM. This
        # invocation owns a unique local tag; verify it before and after build.
        if image_identity(run(["docker", "image", "inspect", base, "--format", "{{.Id}}"], timeout=15).stdout) != report["productionImage"]:
            raise ImageFailure("soak_base_image_changed")
        (build / "Dockerfile").write_text("FROM " + base + "\nCOPY --chmod=0555 worker.test /worker.test\n", encoding="utf-8")
        attempted.add(("image", image))
        run(["docker", "build", "--network", "none", "-t", image, str(build)], timeout=300)
        if image_identity(run(["docker", "image", "inspect", base, "--format", "{{.Id}}"], timeout=15).stdout) != report["productionImage"]:
            raise ImageFailure("soak_base_image_changed")
        report["testImage"] = image_identity(run(["docker", "image", "inspect", image, "--format", "{{.Id}}"], timeout=15).stdout)
        generator = native / "image-fixture"
        run([str(ROOT / ".bin/go"), "build", "-trimpath", "-o", str(generator), "./tools/image-fixture"], env=env, timeout=300)
        report["fixtureGeneratorSha256"] = digest(generator)
        report["stage"] = "fixture"
        atomic_status(evidence / "status.json", {"stage": "fixture", "controllerPid": os.getpid()})
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
                         "\nJELEE_IMAGES_SOAK_ACCEPTANCE=true\nJELEE_IMAGES_SOAK_MODE=" + report["scope"] +
                         "\nJELEE_IMAGES_SOAK_RUN_ID=" + identity + "\n")
        cleanup_ready = True
        report["stage"] = "workload"
        attempted.add(("container", container))
        run(["docker", "run", "-d", "--name", container, "--user", "65532:65532", "--read-only", "--network", "host",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges", *memory_flags(100), "--env", "GOMAXPROCS=2",
             "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--tmpfs", SCRATCH,
             "--env-file", str(secret), "--volume", str(inputs) + ":/media:ro", "--entrypoint", "/worker.test", report["testImage"],
             "-test.v", "-test.run", "^TestImagesSoakAcceptance$", "-test.timeout", "25h"], timeout=30)
        raw_path = evidence / "events.private.jsonl"
        def status(value):
            atomic_status(evidence / "status.json", dict(value, controllerPid=os.getpid(), runId=identity))
        receipt, inspected = follow(["docker", "logs", "--follow", container], raw_path,
                                    lambda: inspect_owned(container),
                                    lambda: run(["docker", "kill", "--signal", "SIGTERM", container], timeout=15), status)
        report["receipt"] = receipt
        report["containerExit"] = container_state(container)
        report["stage"] = "validation"
        with raw_path.open("rb") as stream:
            report["validation"] = validate_soak_log(stream, run_id=identity, scope=report["scope"],
                                                    fixture_bytes=manifest["totalBytes"] + manifest["negativeBytes"],
                                                    inspect=inspected)
        if report["validation"]["container"] is None:
            raise ImageFailure("soak_container_evidence_missing")
        if fixture_sample(inputs, manifest) != originals:
            raise ImageFailure("image_memory_fixture_changed")
        if source_digest() != before_source:
            raise ImageFailure("image_memory_source_changed")
        if snapshot is not None:
            verify(ROOT, snapshot["files"])
        report.update(result="passed", stage="complete", sourceUnchanged=True,
                      originalSamplesUnchanged=True, finalAcceptance=not smoke and snapshot is not None,
                      soakWorkloadPassed=True)
    except (Exception, KeyboardInterrupt) as error:
        code = error.code if isinstance(error, (ImageFailure, ScanFailure, MonitorFailure, SnapshotFailure)) else "image_memory_acceptance_failed"
        if isinstance(error, KeyboardInterrupt):
            code = "cancelled_by_user"
        report.update(result="failed", finalAcceptance=False, failureCode=code.replace("scan_memory_", "image_memory_"))
        if isinstance(error, MonitorFailure) and error.worker_error_code is not None:
            report["workerErrorCode"] = error.worker_error_code
    finally:
        handlers = {}
        if threading.current_thread() is threading.main_thread():
            handlers = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGTERM, signal.SIGINT)}
        if ("container", container) in attempted:
            report["workerStateBeforeCleanup"] = container_state(container)
            try:
                # Give interrupted work the same bounded shutdown before removal.
                if report["workerStateBeforeCleanup"].get("running"):
                    capture(["docker", "stop", "--time", "20", container], timeout=30, limit=4096)
                remove_owned("container", container)
            except Exception:
                cleanup_failed = True
        if cleanup_ready:
            attempted.add(("container", cleanup_container))
            try:
                result = capture(["docker", "run", "--name", cleanup_container, "--network", "host", "--read-only", "--cap-drop", "ALL",
                                  "--security-opt", "no-new-privileges", "--memory", "256m", "--pids-limit", "32", "--env-file", str(secret),
                                  "--entrypoint", "/worker.test", report["testImage"], "--cleanup-probe-worker"], timeout=30, limit=4096)
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
            atomic_status(evidence / "status.json", {"stage": "terminal", "result": report["result"], "controllerPid": os.getpid()})
        finally:
            for sig, handler in handlers.items():
                signal.signal(sig, handler)
    return report
