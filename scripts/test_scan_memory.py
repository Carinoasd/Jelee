#!/usr/bin/env python3
"""One owned, real Fx/HTTP inventory scan under fixed memory and GC budgets."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from urllib.parse import unquote, urlsplit

sys.dont_write_bytecode = True
from test_probe_runtime import digest
from test_probe_worker import source_digest as shared_source_digest
from runtime_memory_acceptance import container_state, inspect_owned, memory_flags, retain_worker_failure_log

ROOT = Path(__file__).resolve().parent.parent
BUDGET = "tools/scan-memory-budget.json"
FIXTURE = b"Jelee synthetic inventory fixture\n"
MAX_LOG_BYTES = 8 * 1024 * 1024
MAX_REPORT_BYTES = 2 * 1024 * 1024
MIN_DISK_BYTES = 20 * 1024**3
FINAL_FILES = 500000
SMOKE_FILES = 1000


class ScanFailure(RuntimeError):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ScanFailure("scan_memory_json_invalid")
        result[key] = value
    return result


def reject_constant(_value):
    raise ScanFailure("scan_memory_json_invalid")


def strict_json(body, limit=MAX_REPORT_BYTES):
    if not isinstance(body, bytes) or not body or len(body) > limit:
        raise ScanFailure("scan_memory_json_invalid")
    try:
        return json.loads(body, object_pairs_hook=unique_object, parse_constant=reject_constant)
    except (ValueError, UnicodeError):
        raise ScanFailure("scan_memory_json_invalid") from None


def source_digest():
    names = ("scripts/test_scan_memory.py", "scripts/test_scan_memory_controller.py",
             "scripts/scan_memory_acceptance.py", "scripts/container_memory.py",
             "scripts/runtime_memory_acceptance.py", BUDGET)
    record = {"sharedSources": shared_source_digest(),
              "scanSources": {name: digest(ROOT / name) for name in names}}
    return hashlib.sha256(json.dumps(record, sort_keys=True).encode()).hexdigest()


def load_budget():
    from scan_memory_acceptance import validate_scan_budget
    try:
        with (ROOT / BUDGET).open("rb") as stream:
            body = stream.read(65537)
        value = strict_json(body, 65536)
        validate_scan_budget(value)
        return value, hashlib.sha256(body).hexdigest()
    except (OSError, ValueError):
        raise ScanFailure("scan_memory_budget_invalid") from None


def capture(argv, timeout=30, limit=MAX_LOG_BYTES, env=None):
    """Bound command output during reads, including unsuccessful commands."""
    process, reader = None, None
    chunks, size = [], 0
    failed = threading.Event()

    def terminate():
        # The production controller runs on Linux. Build subprocesses and any
        # inherited output pipes belong to this invocation's process group.
        try:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            elif process.poll() is None:
                process.kill()
        except ProcessLookupError:
            pass

    try:
        if not 0 < timeout <= 3600 or not 0 < limit <= MAX_LOG_BYTES:
            raise ScanFailure("scan_memory_command_invalid")
        process = subprocess.Popen(argv, cwd=ROOT, env=env, stdin=subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=0,
                                   start_new_session=os.name == "posix")

        def drain():
            nonlocal size
            try:
                while True:
                    data = process.stdout.read(65536)
                    if not data:
                        break
                    size += len(data)
                    if size > limit:
                        failed.set()
                        break
                    chunks.append(data)
            except (OSError, ValueError):
                failed.set()
            finally:
                process.stdout.close()

        reader = threading.Thread(target=drain, daemon=True)
        reader.start()
        deadline = time.monotonic() + timeout
        while (process.poll() is None or reader.is_alive()) and not failed.is_set() and time.monotonic() < deadline:
            interval = min(0.05, max(0.001, deadline - time.monotonic()))
            if reader.is_alive():
                reader.join(timeout=interval)
            else:
                try:
                    process.wait(timeout=interval)
                except subprocess.TimeoutExpired:
                    pass
        if process.poll() is None or reader.is_alive() or failed.is_set():
            failed.set()
            terminate()
        process.wait(timeout=2)
        reader.join(timeout=2)
        if reader.is_alive():
            raise ScanFailure("scan_memory_command_cleanup")
        if failed.is_set():
            raise ScanFailure("scan_memory_command_limit")
        return subprocess.CompletedProcess(argv, process.returncode, b"".join(chunks))
    except (OSError, subprocess.SubprocessError):
        raise ScanFailure("scan_memory_command_failed") from None
    finally:
        if process is not None:
            if process.poll() is None or reader is not None and reader.is_alive():
                terminate()
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                raise ScanFailure("scan_memory_command_cleanup") from None
        if reader is not None and reader.ident is not None:
            reader.join(timeout=2)


class SafeLog:
    def __init__(self, path, private_values):
        self.stream = os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8")
        self.private_values = sorted({str(v) for v in private_values if v}, key=len, reverse=True)
        self.bytes = 0

    def write(self, text):
        for value in self.private_values:
            text = text.replace(value, "[private]")
        data = text.encode("utf-8")
        if self.bytes + len(data) > MAX_LOG_BYTES:
            raise ScanFailure("scan_memory_log_limit")
        self.stream.write(text)
        self.bytes += len(data)

    def flush(self):
        self.stream.flush()

    def close(self):
        self.stream.close()


def _mount_path(text):
    return re.sub(r"\\(040|011|012|134)", lambda m: chr(int(m[1], 8)), text)


def mount_filesystem(body, path):
    if not isinstance(body, bytes) or len(body) > 1024 * 1024:
        raise ScanFailure("scan_memory_storage_invalid")
    selected = None
    try:
        target = PurePosixPath(path)
        for line in body.decode("utf-8", errors="strict").splitlines():
            head, tail = line.split(" - ", 1)
            mount = PurePosixPath(_mount_path(head.split()[4]))
            if target == mount or mount in target.parents:
                if selected is None or len(mount.parts) > selected[0]:
                    selected = (len(mount.parts), tail.split()[0])
        if selected is None:
            raise ValueError("missing mount")
        return selected[1]
    except (ValueError, IndexError, UnicodeError):
        raise ScanFailure("scan_memory_storage_invalid") from None


def storage_record(filesystem, body, required_inodes):
    try:
        fields = body.decode("ascii").split()
        if len(fields) != 3 or any(not re.fullmatch(r"[0-9]{1,20}", value) for value in fields):
            raise ValueError("invalid stat")
        blocks, block_size, inodes = map(int, fields)
        available = blocks * block_size
        if filesystem != "ext4" or not 0 < block_size <= 1024 * 1024 or available > (1 << 64) - 1:
            raise ValueError("not native ext4")
        if available < MIN_DISK_BYTES or inodes < required_inodes:
            raise ScanFailure("scan_memory_storage_capacity")
        return {"filesystem": filesystem, "availableBytes": available,
                "availableInodes": inodes, "nativeDisk": True}
    except (ValueError, UnicodeError):
        raise ScanFailure("scan_memory_storage_invalid") from None


def check_fixture_storage(directory, count):
    with Path("/proc/self/mountinfo").open("rb") as stream:
        mounts = stream.read(1024 * 1024 + 1)
    result = capture(["stat", "-f", "-c", "%a %S %d", "--", str(directory)], limit=4096)
    if result.returncode:
        raise ScanFailure("scan_memory_storage_invalid")
    return storage_record(mount_filesystem(mounts, str(directory)), result.stdout, count + 10000)


def check_postgres_storage(dsn, name):
    """Inspect only the existing, explicitly selected test DB; never change it."""
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", name or ""):
        raise ScanFailure("scan_memory_postgres_identity")
    parsed = urlsplit(dsn)
    inspected = inspect_owned(name)
    try:
        port = parsed.port or 5432
        bindings = inspected["NetworkSettings"]["Ports"]["5432/tcp"]
        if parsed.hostname not in ("localhost", "127.0.0.1", "::1") or not any(
                value["HostIp"] in ("127.0.0.1", "::1") and int(value["HostPort"]) == port for value in bindings):
            raise ValueError("port mismatch")
        values = dict(value.split("=", 1) for value in inspected["Config"]["Env"])
        data = values["PGDATA"]
        if not data.startswith("/") or len(data) > 1024 or any(ord(c) < 32 for c in data):
            raise ValueError("invalid data directory")
        target = PurePosixPath(data)
        mounts = [value for value in inspected["Mounts"]
                  if target == PurePosixPath(value["Destination"]) or PurePosixPath(value["Destination"]) in target.parents]
        if not mounts or max(mounts, key=lambda v: len(PurePosixPath(v["Destination"]).parts))["Type"] not in ("bind", "volume"):
            raise ValueError("persistent mounted data required")
        stat = capture(["docker", "exec", name, "stat", "-f", "-c", "%a %S %d", "--", data], limit=4096)
        mountinfo = capture(["docker", "exec", name, "cat", "/proc/self/mountinfo"], limit=1024 * 1024)
        if stat.returncode or mountinfo.returncode:
            raise ValueError("storage inspection failed")
        return storage_record(mount_filesystem(mountinfo.stdout, data), stat.stdout, 10000)
    except (ValueError, KeyError, TypeError):
        raise ScanFailure("scan_memory_postgres_identity") from None


def fixture_path(directory, index):
    return directory / ("dir-%04d" % (index // 1000)) / ("video-%06d.mkv" % index)


def fixture_sample(directory, count):
    result = []
    for index in sorted({0, count // 2, count - 1}):
        path = fixture_path(directory, index)
        info = path.stat()
        with path.open("rb") as stream:
            body = stream.read(len(FIXTURE) + 1)
        if body != FIXTURE:
            raise ScanFailure("scan_memory_fixture_changed")
        result.append((index, info.st_size, info.st_mtime_ns, info.st_ino, hashlib.sha256(body).hexdigest()))
    return result


def create_fixtures(directory, count):
    if count not in (SMOKE_FILES, FINAL_FILES):
        raise ScanFailure("scan_memory_count_invalid")
    directory.mkdir(mode=0o755)
    for index in range(count):
        path = fixture_path(directory, index)
        if index % 1000 == 0:
            path.parent.mkdir(mode=0o755)
        with path.open("xb") as stream:
            stream.write(FIXTURE)
        path.chmod(0o644)
    return fixture_sample(directory, count)


def parse_events(transcript):
    ready, report = 0, None
    for line in transcript.splitlines():
        if not line.startswith(b"{"):
            continue
        value = strict_json(line)
        if not isinstance(value, dict):
            raise ScanFailure("scan_memory_events_invalid")
        if "scanMemoryReadyForSIGTERM" in value:
            if set(value) != {"scanMemoryReadyForSIGTERM"} or value["scanMemoryReadyForSIGTERM"] is not True or ready or report is not None:
                raise ScanFailure("scan_memory_events_invalid")
            ready += 1
        elif "scanMemoryAcceptance" in value:
            if set(value) != {"scanMemoryAcceptance"} or report is not None or not isinstance(value["scanMemoryAcceptance"], dict):
                raise ScanFailure("scan_memory_events_invalid")
            report = value["scanMemoryAcceptance"]
    return bool(ready), report


def _write_json(path, report):
    body = (json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")
    if len(body) > MAX_REPORT_BYTES:
        raise ScanFailure("scan_memory_report_limit")
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as stream:
        stream.write(body)


def run_case(*, smoke=False):
    from scan_memory_acceptance import validate_scan_memory
    identity = uuid.uuid4().hex
    count = SMOKE_FILES if smoke else FINAL_FILES
    evidence = ROOT / ".testdata" / ("scan-memory-" + identity)
    evidence.parent.mkdir(exist_ok=True)
    evidence.mkdir(mode=0o700)
    report = {"version": 1, "scope": "smoke-1000" if smoke else "scan-500000",
              "result": "failed", "finalAcceptance": False, "fixtureFiles": count,
              "fixtureBytes": count * len(FIXTURE), "stage": "preflight"}
    native = log = None
    latest = b""
    base = "jelee/jelee:scan-memory-" + identity
    image, container = base + "-test", "jelee-scan-memory-" + identity
    cleanup_container = container + "-cleanup"
    schema = "jelee_probe_worker_" + identity
    cleanup_ready = False
    cleanup_failed = False
    try:
        if sys.platform != "linux":
            raise ScanFailure("scan_memory_linux_required")
        dsn = os.environ.get("JELEE_TEST_DATABASE_URL", "")
        parsed = urlsplit(dsn)
        if parsed.scheme not in ("postgres", "postgresql") or parsed.path != "/jelee_test" or not parsed.hostname or any(c in dsn for c in "\r\n\x00"):
            raise ScanFailure("scan_memory_database_invalid")
        budget, budget_hash = load_budget()
        before_source = source_digest()
        report.update(sourceDigest=before_source, budgetSha256=budget_hash)
        native_parent = Path("/var/tmp").resolve()
        report["fixtureStorage"] = check_fixture_storage(native_parent, count)
        native = Path(tempfile.mkdtemp(prefix="jelee-scan-memory-", dir=native_parent)).resolve()
        report["postgresStorage"] = check_postgres_storage(dsn, os.environ.get("JELEE_SCAN_MEMORY_PG_CONTAINER", ""))
        log = SafeLog(evidence / "acceptance.txt", (dsn, parsed.password, unquote(parsed.password or ""), ROOT, native, Path.home()))

        def run(argv, timeout=300, check=True, **kwargs):
            result = capture(argv, timeout=timeout, env=kwargs.get("env"))
            log.write(result.stdout.decode("utf-8", errors="replace"))
            log.flush()
            if check and result.returncode:
                raise ScanFailure("scan_memory_command_failed")
            return result

        report["stage"] = "build"
        run(["docker", "build", "--network", "host", "-t", base, "."], timeout=900)
        report["productionImage"] = run(["docker", "image", "inspect", base, "--format", "{{.Id}}"], timeout=15).stdout.decode().strip()
        env = dict(os.environ, CGO_ENABLED="0")
        env.pop("JELEE_TEST_DATABASE_URL", None)
        run([str(ROOT / ".bin/go"), "test", "-trimpath", "-tags", "jelee_probe_tests", "-c", "-o", str(native / "worker.test"), "./internal/platform/runtime"], env=env, timeout=900)
        report["testBinarySha256"] = digest(native / "worker.test")
        (native / "Dockerfile").write_text("FROM " + base + "\nCOPY --chmod=0555 worker.test /worker.test\n", encoding="utf-8")
        run(["docker", "build", "--network", "none", "-t", image, str(native)], timeout=300)
        report["testImage"] = run(["docker", "image", "inspect", image, "--format", "{{.Id}}"], timeout=15).stdout.decode().strip()
        secret = native / "database.env"
        with os.fdopen(os.open(secret, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8") as stream:
            stream.write("JELEE_TEST_DATABASE_URL=" + dsn + "\nJELEE_PROBE_TEST_SCHEMA=" + schema +
                         "\nJELEE_SCAN_MEMORY_ACCEPTANCE=true\nJELEE_SCAN_MEMORY_FILES=" + str(count) + "\n")
        cleanup_ready = True
        report["stage"] = "fixtures"
        inputs = native / "media"
        originals = create_fixtures(inputs, count)
        if source_digest() != before_source:
            raise ScanFailure("scan_memory_source_changed")
        report["stage"] = "scan"
        run(["docker", "run", "-d", "--name", container, "--user", "65532:65532", "--read-only", "--network", "host",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges", *memory_flags(100), "--env", "GOMAXPROCS=2",
             "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--env-file", str(secret),
             "--volume", str(inputs) + ":/media:ro", "--entrypoint", "/worker.test", image,
             "-test.v", "-test.run", "^TestScanMemoryAcceptance$", "-test.timeout", "1h"], timeout=30)
        deadline = time.monotonic() + 3600
        signalled = False
        final = None
        prior_ready = False
        prior_final = None
        while time.monotonic() < deadline:
            inspected = inspect_owned(container)
            value = capture(["docker", "logs", container], timeout=15)
            if value.returncode:
                raise ScanFailure("scan_memory_log_unavailable")
            latest = value.stdout
            ready, final = parse_events(latest)
            if prior_ready and not ready or prior_final is not None and prior_final != final:
                raise ScanFailure("scan_memory_events_invalid")
            prior_ready, prior_final = ready, final
            if ready and not signalled:
                run(["docker", "kill", "--signal", "SIGTERM", container], timeout=15)
                signalled = True
            if not inspected["State"]["Running"]:
                log.write(latest.decode("utf-8", errors="replace")); log.flush()
                report["containerExit"] = container_state(container)
                if not signalled or final is None or b"--- SKIP:" in latest or b"\nPASS\n" not in latest:
                    raise ScanFailure("scan_memory_acceptance_incomplete")
                report["stage"] = "validation"
                _write_json(evidence / "acceptance-report.private.json", final)
                report["rawAcceptanceSha256"] = digest(evidence / "acceptance-report.private.json")
                report["validation"] = validate_scan_memory(final, inspected, budget, expected_files=count)
                break
            time.sleep(1)
        else:
            raise ScanFailure("scan_memory_workload_timeout")
        if fixture_sample(inputs, count) != originals:
            raise ScanFailure("scan_memory_fixture_changed")
        if source_digest() != before_source:
            raise ScanFailure("scan_memory_source_changed")
        report.update(result="passed", stage="complete", sourceUnchanged=True,
                      originalSamplesUnchanged=True, finalAcceptance=not smoke)
    except (Exception, KeyboardInterrupt) as error:
        report.update(result="failed", finalAcceptance=False,
                      failureCode=error.code if isinstance(error, ScanFailure) else "scan_memory_acceptance_failed")
    finally:
        # Once resource cleanup starts, a second terminal signal must not skip
        # later resources or the preserved failure summary.
        cleanup_handlers = {}
        if threading.current_thread() is threading.main_thread():
            cleanup_handlers = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGTERM, signal.SIGINT)}
        if log is not None:
            if report["result"] != "passed":
                report["workerStateBeforeCleanup"] = container_state(container)
                try:
                    report["workerFailureLog"] = retain_worker_failure_log(run, log, container, latest.decode("utf-8", errors="replace"), dsn)
                except (ScanFailure, OSError):
                    report["workerFailureLog"] = {"retentionFailed": True}

            def remove_owned(kind, name):
                nonlocal cleanup_failed
                try:
                    # A failed inspect can mean a dead daemon, not absence.
                    query = (["docker", "container", "ls", "--all", "--filter", "name=^/" + name + "$", "--format", "{{.Names}}"]
                             if kind == "container" else
                             ["docker", "image", "ls", "--filter", "reference=" + name, "--format", "{{.Repository}}:{{.Tag}}"])

                    def present():
                        check = capture(query, timeout=15, limit=4096)
                        if check.returncode or check.stdout.strip() not in (b"", name.encode("ascii")):
                            raise ScanFailure("scan_memory_cleanup_unverified")
                        return bool(check.stdout.strip())

                    if present():
                        removed = capture(["docker", kind, "rm", "--force", name], timeout=30, limit=4096)
                        if removed.returncode or present():
                            cleanup_failed = True
                except (ScanFailure, OSError):
                    cleanup_failed = True

            remove_owned("container", container)
            if cleanup_ready:
                try:
                    result = capture(["docker", "run", "--name", cleanup_container, "--network", "host", "--read-only", "--cap-drop", "ALL",
                                      "--security-opt", "no-new-privileges", "--memory", "256m", "--pids-limit", "32", "--env-file", str(secret),
                                      "--entrypoint", "/worker.test", image, "--cleanup-probe-worker"], timeout=30, limit=4096)
                    cleanup_failed |= result.returncode != 0
                except (ScanFailure, OSError):
                    cleanup_failed = True
            remove_owned("container", cleanup_container)
            remove_owned("image", image)
            remove_owned("image", base)
            try:
                log.close()
                report["transcriptSha256"] = digest(evidence / "acceptance.txt")
            except OSError:
                cleanup_failed = True
        if native is not None:
            try:
                # This unique directory was created by this invocation only.
                if native.parent != Path("/var/tmp").resolve() or not native.name.startswith("jelee-scan-memory-") or native.is_symlink():
                    raise OSError("invalid owned directory")
                shutil.rmtree(native)
            except OSError:
                cleanup_failed = True
        report["testArtifactsCleaned"] = not cleanup_failed
        if cleanup_failed:
            report.update(result="failed", finalAcceptance=False, cleanupFailure=True)
        try:
            _write_json(evidence / "summary.json", report)
        finally:
            for sig, handler in cleanup_handlers.items():
                signal.signal(sig, handler)
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run one fixed scan-memory acceptance; smoke is never final scale evidence.")
    parser.add_argument("--smoke", action="store_true", help="use 1000 fixtures; no final 500000 acceptance claim")
    args = parser.parse_args(argv)

    interruption_seen = False

    def interrupted(_signum, _frame):
        nonlocal interruption_seen
        if interruption_seen:
            return
        interruption_seen = True
        raise ScanFailure("scan_memory_controller_interrupted")

    previous_handlers = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        result = run_case(smoke=args.smoke)
    except (OSError, ValueError, ScanFailure):
        print('{"result":"failed","errorCode":"scan_memory_controller_failed"}')
        return 1
    finally:
        for sig, handler in previous_handlers.items():
            signal.signal(sig, handler)
    print(json.dumps({key: result[key] for key in ("scope", "result", "fixtureFiles", "finalAcceptance", "testArtifactsCleaned")}))
    return 0 if result["result"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
