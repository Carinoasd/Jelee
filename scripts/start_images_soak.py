#!/usr/bin/env python3
"""Launch one detached soak from a committed snapshot, with a dedicated PG."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import sys
import time
import uuid

sys.dont_write_bytecode = True
from images_soak_monitor import atomic_status
from images_soak_snapshot import prepare, verify, remove_snapshot, SnapshotFailure
from test_scan_memory import capture
from test_image_memory import image_identity, remove_owned

ROOT = Path(__file__).resolve().parent.parent


def command(argv, timeout=30, env=None):
    value = capture(argv, timeout=timeout, env=env)
    if value.returncode:
        raise SnapshotFailure("soak_launcher_command_failed")
    return value.stdout


def process_identity(pid):
    # Field 22 after the parenthesized comm; comm itself may contain spaces.
    body = Path("/proc/%d/stat" % pid).read_text()
    return {"pid": pid, "startTicks": int(body[body.rfind(")") + 2:].split()[19]),
            "bootId": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}


def private_json(path, value):
    body = (json.dumps(value, separators=(",", ":"), allow_nan=False) + "\n").encode()
    if len(body) > 8 * 1024**2:
        raise SnapshotFailure()
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as stream:
        stream.write(body)


def worker(evidence, owned, identity, smoke):
    """This function and every import run from the frozen source directory."""
    from run_images_soak import run_case
    snapshot = json.loads((evidence / "source.json").read_bytes())
    result = {"result": "failed", "finalAcceptance": False, "runId": identity}
    pg = "jelee-soak-pg-" + identity
    volume = "jelee-soak-pg-data-" + identity
    pg_attempted = volume_attempted = False
    secret = owned / "postgres.env"
    old_handlers = {}

    def interrupted(_sig, _frame):
        raise SnapshotFailure("cancelled_by_user")

    try:
        old_handlers = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
        atomic_status(evidence / "status.json", {"stage": "provision", **process_identity(os.getpid()), "runId": identity})
        verify(ROOT, snapshot["files"])
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        for key in ("JELEE_TEST_DATABASE_URL", "JELEE_DATABASE_URL"):
            env.pop(key, None)
        command([sys.executable, "-B", "scripts/toolchain.py", "bootstrap", "--offline"], timeout=300, env=env)
        verify(ROOT, snapshot["files"])
        manifest = json.loads((ROOT / "tools/manifest.json").read_bytes())
        pin, = [item["image"] for item in manifest["existingHostDependencies"] if item["name"] == "postgres-test-image"]
        pg_image = image_identity(command(["docker", "image", "inspect", pin, "--format", "{{.Id}}"], 15))
        password = secrets.token_hex(32)
        with os.fdopen(os.open(secret, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
            stream.write("POSTGRES_DB=jelee_test\nPOSTGRES_USER=postgres\nPOSTGRES_PASSWORD=" + password + "\n")
        volume_attempted = True
        command(["docker", "volume", "create", "--label", "jelee.soak.run=" + identity, volume])
        pg_attempted = True
        command(["docker", "run", "-d", "--pull", "never", "--name", pg,
                 "--label", "jelee.soak.run=" + identity, "--publish", "127.0.0.1::5432",
                 "--env-file", str(secret), "--mount", "type=volume,src=" + volume + ",dst=/var/lib/postgresql/data",
                 pg_image])
        from runtime_memory_acceptance import inspect_owned
        inspected = inspect_owned(pg)
        binding, = inspected["NetworkSettings"]["Ports"]["5432/tcp"]
        if binding["HostIp"] != "127.0.0.1" or not 1 <= int(binding["HostPort"]) <= 65535:
            raise SnapshotFailure()
        ready = False
        for _ in range(60):
            state = capture(["docker", "exec", pg, "pg_isready", "-U", "postgres", "-d", "jelee_test"], timeout=5, limit=4096)
            if state.returncode == 0:
                ready = True
                break
            time.sleep(0.5)
        if not ready:
            raise SnapshotFailure("soak_postgres_not_ready")
        os.environ["JELEE_TEST_DATABASE_URL"] = "postgres://postgres:" + password + "@127.0.0.1:" + binding["HostPort"] + "/jelee_test?sslmode=disable"
        os.environ["JELEE_IMAGE_MEMORY_PG_CONTAINER"] = pg
        atomic_status(evidence / "status.json", {"stage": "workload", **process_identity(os.getpid()), "runId": identity,
                                                "postgresId": inspected["Id"], "postgresImage": pg_image})
        result = run_case(smoke=smoke, evidence_root=evidence / "cases", identity=identity, snapshot=snapshot)
        result["snapshotVerified"] = result.get("sourceUnchanged") is True
        result["postgresImage"] = pg_image
    except (Exception, KeyboardInterrupt) as error:
        result.update(result="failed", finalAcceptance=False,
                      failureCode=error.code if isinstance(error, SnapshotFailure) else "soak_launcher_failed")
    finally:
        for sig in old_handlers:
            signal.signal(sig, signal.SIG_IGN)
        cleanup = True
        if pg_attempted:
            try:
                remove_owned("container", pg)
            except Exception:
                cleanup = False
        if volume_attempted:
            try:
                command(["docker", "volume", "rm", volume], 30)
                remaining = command(["docker", "volume", "ls", "--filter", "name=^" + volume + "$", "--format", "{{.Name}}"], 15)
                if remaining.strip():
                    raise SnapshotFailure()
            except Exception:
                cleanup = False
        secret.unlink(missing_ok=True)
        try:
            verify(ROOT, snapshot["files"])
            remove_snapshot(owned)
        except Exception:
            cleanup = False
        result["launcherArtifactsCleaned"] = cleanup
        if not cleanup:
            result.update(result="failed", finalAcceptance=False, launcherCleanupFailure=True)
        private_json(evidence / "result.json", result)
        atomic_status(evidence / "status.json", {"stage": "terminal", "result": result["result"],
                                                "finalAcceptance": result["finalAcceptance"], "runId": identity})
        for sig, handler in old_handlers.items():
            signal.signal(sig, handler)
    return 0 if result["result"] == "passed" else 1


def launch(smoke):
    import fcntl
    parent = ROOT / ".testdata"
    parent.mkdir(exist_ok=True)
    lock = (parent / "images-soak.lock").open("a+")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock.close()
        raise SnapshotFailure("soak_already_running") from None
    identity = uuid.uuid4().hex
    evidence = parent / ("soak-launch-" + identity)
    evidence.mkdir(mode=0o700)
    owned = Path("/var/tmp") / ("jelee-soak-snapshot-" + identity)
    owned.mkdir(mode=0o700)
    process = None
    try:
        atomic_status(evidence / "status.json", {"stage": "snapshot", "runId": identity})
        snapshot = prepare(ROOT, owned)
        private_json(evidence / "source.json", snapshot)
        if not smoke and not smoke_passed(parent, snapshot["commit"]):
            raise SnapshotFailure("soak_matching_smoke_required")
        source = owned / "source"
        # Snapshot script must exist in the chosen commit, never inject live code.
        if "scripts/start_images_soak.py" not in snapshot["files"]:
            raise SnapshotFailure("soak_launcher_not_committed")
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        for key in ("JELEE_TEST_DATABASE_URL", "JELEE_DATABASE_URL", "PYTHONPATH"):
            env.pop(key, None)
        with os.fdopen(os.open(evidence / "launcher.private.log", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as output:
            process = subprocess.Popen([sys.executable, "-B", str(source / "scripts/start_images_soak.py"),
                                        "--worker", str(evidence), str(owned), identity, "smoke" if smoke else "formal"],
                                       cwd=source, env=env, stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT,
                                       start_new_session=True, pass_fds=(lock.fileno(),))
        record = {"runId": identity, **process_identity(process.pid), "sourceCommit": snapshot["commit"],
                  "sourceTree": snapshot["tree"], "scope": "smoke" if smoke else "formal",
                  "startedUtc": datetime.now(timezone.utc).isoformat(), "evidence": str(evidence), "snapshot": str(owned)}
        private_json(evidence / "registry.json", record)
        atomic_status(parent / "images-soak-active.json", record)
        return record
    except BaseException:
        if process is None:
            remove_snapshot(owned)
        raise
    finally:
        # Child inherited the same open file description and retains the lock.
        lock.close()


def smoke_passed(parent, commit):
    for path in parent.glob("soak-launch-*/result.json"):
        if path.is_symlink() or path.stat().st_size > 8 * 1024**2:
            continue
        try:
            value = json.loads(path.read_bytes())
            if (value.get("scope") == "smoke" and value.get("sourceCommit") == commit and
                    value.get("result") == "passed" and value.get("soakWorkloadPassed") is True and
                    value.get("snapshotVerified") is True and value.get("launcherArtifactsCleaned") is True and
                    value.get("testArtifactsCleaned") is True):
                return True
        except (ValueError, OSError, AttributeError):
            continue
    return False


def main():
    if sys.platform != "linux":
        raise SnapshotFailure("soak_linux_required")
    if len(sys.argv) == 6 and sys.argv[1] == "--worker":
        evidence, owned = Path(sys.argv[2]), Path(sys.argv[3])
        identity, scope = sys.argv[4:]
        if not re.fullmatch(r"[a-f0-9]{32}", identity) or owned != Path("/var/tmp") / ("jelee-soak-snapshot-" + identity) or ROOT != owned / "source" or scope not in {"smoke", "formal"}:
            raise SnapshotFailure()
        return worker(evidence, owned, identity, scope == "smoke")
    parser = argparse.ArgumentParser()
    parser.add_argument("--smoke", action="store_true")
    args = parser.parse_args()
    print(json.dumps(launch(args.smoke), separators=(",", ":")))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (Exception, KeyboardInterrupt) as error:
        print(json.dumps({"result": "failed", "failureCode": error.code if isinstance(error, SnapshotFailure) else "soak_launcher_failed"}))
        raise SystemExit(1)
