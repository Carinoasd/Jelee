"""Owned-container checks for the opt-in runtime memory acceptance profile."""
import json
import socket
import subprocess
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit
from urllib.request import ProxyHandler, Request, build_opener

from container_memory import validate_container_interval, validate_oom_negative


def inspect_owned(name):
    # Never return raw inspect data to a transcript: Config.Env contains a DSN.
    result = subprocess.run(["docker", "inspect", name], capture_output=True, timeout=15)
    if result.returncode:
        raise RuntimeError("owned memory container inspection failed")
    try:
        values = json.loads(result.stdout)
        if len(values) != 1 or values[0].get("Name") != "/" + name:
            raise ValueError("identity")
        return values[0]
    except (ValueError, KeyError, TypeError):
        raise RuntimeError("owned memory container inspection failed") from None


def memory_flags(gogc):
    if gogc not in (50, 100):
        raise ValueError("unsupported memory acceptance profile")
    return ["--memory", "768m", "--memory-swap", "768m", "--cpus", "2", "--pids-limit", "128",
            "--env", "GOGC=" + str(gogc), "--env", "GOMEMLIMIT=512MiB"]


def container_state(name):
    """Preserve only fixed state fields before removing an owned container."""
    try:
        state = inspect_owned(name)["State"]
        if (state["Status"] not in ("created", "running", "paused", "restarting", "removing", "exited", "dead")
                or type(state["ExitCode"]) is not int or not 0 <= state["ExitCode"] <= 255
                or type(state["OOMKilled"]) is not bool or type(state["Running"]) is not bool):
            raise ValueError("state")
        return {"status": state["Status"], "exitCode": state["ExitCode"],
                "oomKilled": state["OOMKilled"], "running": state["Running"]}
    except (OSError, subprocess.SubprocessError, RuntimeError, ValueError, KeyError, TypeError):
        return {"inspectionAvailable": False}


def retain_worker_failure_log(run, log, name, latest, dsn):
    """Save the last successful read even if Docker cannot return fresh logs."""
    record = {"previousReadLogRetained": False, "freshReadSucceeded": False}
    try:
        if latest:
            log.write(latest.replace(dsn, "[redacted database URL]"))
            log.flush()
            record["previousReadLogRetained"] = True
    except OSError:
        pass
    try:
        # The caller's run writes and redacts the command output in the log.
        result = run(["docker", "logs", name], timeout=15, check=False)
        record["freshReadSucceeded"] = result.returncode == 0
    except (OSError, subprocess.SubprocessError):
        pass
    return record


def snapshot_container(run, name, gogc):
    result = run(["docker", "exec", "--env", "JELEE_MEMORY_SNAPSHOT=true", name,
                  "/worker.test", "-test.v", "-test.run", "^TestMemoryProfileSnapshot$", "-test.timeout", "10s"], timeout=15)
    try:
        records = [json.loads(line)["memoryProfileSnapshot"] for line in result.stdout.decode().splitlines()
                   if line.startswith('{"memoryProfileSnapshot":')]
        if len(records) != 1 or records[0]["runtime"] != {"gogcPercent": gogc, "goMemoryLimitBytes": 512 * 1024**2}:
            raise ValueError("runtime settings")
        return records[0]["cgroup"]
    except (ValueError, KeyError, TypeError):
        raise RuntimeError("memory observer report is missing or invalid") from None


def check_production_entry(run, image, name, secret_folder, dsn, schema, gogc, record):
    """Use /jelee itself against the worker's already migrated owned schema."""
    parts = urlsplit(dsn)
    query = dict(parse_qsl(parts.query, keep_blank_values=True))
    query.update(search_path=schema, application_name=name)
    owned_dsn = urlunsplit(parts._replace(query=urlencode(query)))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    environment = secret_folder / "main-memory.env"
    environment.write_text("JELEE_DATABASE_URL=" + owned_dsn + "\nJELEE_LISTEN=127.0.0.1:" + str(port) +
                           "\nJELEE_ALLOWED_HOSTS=127.0.0.1\nJELEE_ENABLE_ACCOUNTS=true\nJELEE_ENABLE_METRICS=true\n")
    environment.chmod(0o600)
    record.update(result="failed", stage="start", entrypoint="/jelee")
    started = False
    try:
        run(["docker", "run", "-d", "--name", name, "--read-only", "--network", "host", "--cap-drop", "ALL",
             "--security-opt", "no-new-privileges", *memory_flags(gogc),
             "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--env-file", str(environment),
             "--entrypoint", "/jelee", image])
        started = True
        record["stage"] = "readiness"
        opener = build_opener(ProxyHandler({}))
        deadline = time.monotonic() + 20
        while True:
            if not inspect_owned(name)["State"]["Running"]:
                raise RuntimeError("production memory entry exited during startup")
            try:
                with opener.open(Request("http://127.0.0.1:" + str(port) + "/readyz"), timeout=2) as response:
                    if response.status == 200 and json.loads(response.read(4096))["data"]["status"] == "ready":
                        break
            except (OSError, ValueError, KeyError):
                pass
            if time.monotonic() >= deadline:
                raise RuntimeError("production memory entry readiness timed out")
            time.sleep(0.1)
        record["stage"] = "observerBefore"
        before = snapshot_container(run, name, gogc)
        record["stage"] = "health"
        for route, expected in (("/healthz", "ok"), ("/readyz", "ready")):
            with opener.open(Request("http://127.0.0.1:" + str(port) + route), timeout=3) as response:
                if response.status != 200 or json.loads(response.read(4096))["data"]["status"] != expected:
                    raise RuntimeError("production memory entry health contract failed")
        record["stage"] = "observerAfter"
        after = snapshot_container(run, name, gogc)
        record["stage"] = "stop"
        run(["docker", "stop", "--time", "15", name], timeout=20)
        record["stage"] = "validation"
        inspected = inspect_owned(name)
        # Verify the real entrypoint and its startup environment without retaining secrets.
        effective = dict(value.split("=", 1) for value in inspected["Config"]["Env"])
        if inspected["Config"]["Entrypoint"] != ["/jelee"] or effective.get("GOGC") != str(gogc) or effective.get("GOMEMLIMIT") != "512MiB":
            raise RuntimeError("production memory entry settings differ")
        record.update(validate_container_interval(before, after, inspected))
        record.update(entrypoint="/jelee", health="passed", readiness="passed", stop="passed",
                      runtimeSettingsObservedIn="separate observer process in the same container",
                      mainStartupEnvironmentVerified=True, mainRuntimeSettingsDirectlyObserved=False)
        record.update(result="passed", stage="complete")
        return record
    finally:
        record["stateBeforeCleanup"] = container_state(name)
        if started:
            try:
                run(["docker", "container", "rm", "--force", name], timeout=20)
                record["containerRemoved"] = True
            except (OSError, subprocess.SubprocessError, RuntimeError):
                record.update(result="failed", containerRemoved=False)
                raise
            finally:
                environment.unlink(missing_ok=True)
        else:
            environment.unlink(missing_ok=True)


def check_oom_negative(run, image, name, record):
    """A separate 64 MiB container must demonstrate the OOM rejection path."""
    record.update(result="failed", stage="start")
    started = False
    try:
        run(["docker", "run", "-d", "--name", name, "--read-only", "--network", "none", "--cap-drop", "ALL",
             "--security-opt", "no-new-privileges", "--memory", "64m", "--memory-swap", "64m",
             "--cpus", "1", "--pids-limit", "32", "--env", "GOGC=off", "--env", "GOMEMLIMIT=off",
             "--env", "JELEE_MEMORY_OOM_PROBE=true", "--entrypoint", "/worker.test", image,
             "-test.v", "-test.run", "^TestMemoryProfileOOMProbe$", "-test.timeout", "20s"])
        started = True
        record["stage"] = "wait"
        run(["docker", "wait", name], timeout=30)
        record["stage"] = "validation"
        logs = run(["docker", "logs", name], timeout=15)
        try:
            armed = [json.loads(line) for line in logs.stdout.decode().splitlines()
                     if line.startswith('{"memoryOOMProbe":')]
            if armed != [{"memoryOOMProbe": "armed", "hardLimitBytes": 67108864, "attemptBytes": 134217728}]:
                raise ValueError("probe not armed")
        except (ValueError, UnicodeError):
            raise RuntimeError("controlled OOM probe did not confirm its allocation phase") from None
        record["allocationPhaseConfirmed"] = True
        record.update(validate_oom_negative(inspect_owned(name)))
        record.update(result="passed", stage="complete")
        return record
    finally:
        record["stateBeforeCleanup"] = container_state(name)
        if started:
            try:
                run(["docker", "container", "rm", "--force", name], timeout=20)
                record["containerRemoved"] = True
            except (OSError, subprocess.SubprocessError, RuntimeError):
                record.update(result="failed", containerRemoved=False)
                raise
