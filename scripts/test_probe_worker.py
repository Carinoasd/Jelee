#!/usr/bin/env python3
"""Required real scan/probe/cache acceptance in the protected production profile."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid
from urllib.parse import urlparse

from test_probe_runtime import digest
from test_sandbox_native import select_fixtures

ROOT = Path(__file__).resolve().parent.parent


def source_digest():
    paths = [ROOT / "go.mod", ROOT / "go.sum", ROOT / "Dockerfile", Path(__file__).resolve()]
    paths += [path for path in (ROOT / "internal").rglob("*.go") if not path.name.endswith("_test.go") or path.parent == ROOT / "internal/platform/runtime"]
    paths += list((ROOT / "internal/adapter/postgres/migrations").glob("*.sql"))
    paths += [path for path in (ROOT / "cmd").rglob("*.go") if not path.name.endswith("_test.go")]
    paths += [ROOT / "tools/manifest.json", ROOT / "tools/media.go", ROOT / "tools/runtime.go", ROOT / "scripts/test_probe_runtime.py", ROOT / "scripts/test_sandbox_native.py", ROOT / "scripts/toolchain.py"]
    paths += list((ROOT / "tools/runtime-image").glob("*.go"))
    record = {str(path.relative_to(ROOT)): digest(path) for path in sorted(paths)}
    return hashlib.sha256(json.dumps(record, sort_keys=True).encode()).hexdigest()


def main():
    if sys.platform != "linux":
        raise RuntimeError("probe worker acceptance requires native Linux Docker")
    dsn = os.environ.get("JELEE_TEST_DATABASE_URL", "")
    parsed = urlparse(dsn)
    if parsed.scheme not in ("postgres", "postgresql") or parsed.path != "/jelee_test" or not parsed.hostname or "\n" in dsn or "\r" in dsn:
        raise RuntimeError("dedicated jelee_test PostgreSQL URL required")
    media = json.loads((ROOT / "tools/manifest.json").read_text())["mediaTools"]["platforms"]["linux-amd64"]
    fixtures, originals = select_fixtures(None, media)
    before_source = source_digest()
    (ROOT / ".testdata").mkdir(exist_ok=True)
    transcript = ROOT / ".testdata/probe-worker-acceptance.txt"
    identity = uuid.uuid4().hex
    base = "jelee/jelee:probe-worker-" + identity
    image = base + "-test"
    container = "jelee-probe-worker-" + identity
    cleanup_container = container + "-cleanup"
    schema = "jelee_probe_worker_" + identity
    report = {"sourceDigest": before_source, "profile": "production Linux amd64; UID 65532; read-only root and media; no capabilities; no-new-privileges", "rounds": []}
    with transcript.open("w", encoding="utf-8") as log, tempfile.TemporaryDirectory(prefix="probe-worker-", dir=ROOT / ".testdata") as temp:
        temp = Path(temp)
        secret_file = temp / "database.env"
        cleanup_ready = False
        def run(argv, timeout=300, check=True, **kwargs):
            result = subprocess.run(argv, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout, **kwargs)
            log.write(result.stdout.decode(errors="replace")); log.flush()
            if check and result.returncode:
                raise RuntimeError("probe worker command failed; inspect ignored acceptance log")
            return result
        try:
            run(["docker", "build", "--network", "host", "-t", base, "."], timeout=600)
            report["productionImage"] = run(["docker", "image", "inspect", base, "--format", "{{.Id}}"]).stdout.decode().strip()
            env = dict(os.environ, CGO_ENABLED="0")
            run([str(ROOT / ".bin/go"), "test", "-tags", "jelee_probe_tests", "-c", "-o", str(temp / "worker.test"), "./internal/platform/runtime"], env=env)
            (temp / "Dockerfile").write_text("FROM " + base + "\nCOPY --chmod=0555 worker.test /worker.test\n")
            run(["docker", "build", "--network", "none", "-t", image, str(temp)])
            # The derived build context contains no credential file.
            secret_file.write_text("JELEE_TEST_DATABASE_URL=" + dsn + "\nJELEE_REQUIRE_PROBE_WORKER=true\nJELEE_PROBE_TEST_SCHEMA=" + schema + "\n")
            secret_file.chmod(0o600)
            cleanup_ready = True
            inputs = temp / "media"; inputs.mkdir()
            control = temp / "control"; control.mkdir()
            for key, name in (("a", "video-180p.mp4"), ("b", "video-360p.mp4")):
                shutil.copyfile(fixtures / name, temp / ("seed-" + key + ".mp4"))
            for n in range(1000):
                os.link(temp / "seed-a.mp4", inputs / ("media-%04d.mp4" % n))
            before_inputs = {path.name: {"sha256": digest(path), "size": path.stat().st_size, "mtimeNanos": path.stat().st_mtime_ns, "inode": path.stat().st_ino} for path in inputs.iterdir()}
            args = ["docker", "run", "-d", "--name", container, "--read-only", "--network", "host", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "768m", "--pids-limit", "128", "--cpus", "2", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--env-file", str(secret_file), "--volume", str(inputs) + ":/media:ro", "--volume", str(control) + ":/control:ro", "--entrypoint", "/worker.test", image, "-test.v", "-test.run", "^TestProductionProbeWorkerAcceptance$", "-test.timeout", "26m"]
            run(args)
            deadline = time.monotonic() + 27 * 60
            replaced = False
            latest = ""
            reported_rounds = set()
            while time.monotonic() < deadline:
                state = json.loads(subprocess.run(["docker", "inspect", container, "--format", "{{json .State}}"], check=True, stdout=subprocess.PIPE, timeout=15).stdout)
                # Keep repeated inspect/log output out of the transcript below.
                logs = subprocess.run(["docker", "logs", container], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=15).stdout.decode(errors="replace")
                latest = logs
                for line in logs.splitlines():
                    if not line.startswith("{"):
                        continue
                    entry = json.loads(line)
                    if "round" in entry and entry["round"] not in reported_rounds:
                        report["rounds"].append(entry); reported_rounds.add(entry["round"])
                        print(json.dumps(entry), flush=True)
                if not replaced and '{"readyForReplacement":true}' in logs:
                    for n in range(17):
                        incoming = inputs / ("replace-%04d" % n)
                        os.link(temp / "seed-b.mp4", incoming)
                        os.replace(incoming, inputs / ("media-%04d.mp4" % n))
                    (control / "continue").write_text("17 owned directory entries replaced\n")
                    replaced = True
                if not state["Running"]:
                    log.write(latest); log.flush()
                    if state["ExitCode"] != 0 or not replaced or "--- SKIP:" in latest or "\nPASS\n" not in latest:
                        raise RuntimeError("required worker acceptance failed or skipped")
                    break
                time.sleep(1)
            else:
                log.write(latest); log.flush()
                raise RuntimeError("worker acceptance timed out")
            after_inputs = {path.name: {"sha256": digest(path), "size": path.stat().st_size, "mtimeNanos": path.stat().st_mtime_ns, "inode": path.stat().st_ino} for path in inputs.iterdir()}
            changed = sorted(name for name in before_inputs if before_inputs[name] != after_inputs[name])
            if changed != ["media-%04d.mp4" % n for n in range(17)]:
                raise RuntimeError("media mutations differ from the 17 controlled replacements")
            if originals != {name: digest(fixtures / name) for name in originals} or before_source != source_digest():
                raise RuntimeError("original fixtures or source changed during acceptance")
            if [row["metadataProberCalls"] for row in report["rounds"]] != [1000, 0, 17]:
                raise RuntimeError("incorrect metadata invocation counts")
            report.update(originalHashesUnchanged=True, controlledReplacements=17, hardlinkedPaths=1000, copiedMediaPaths=0, initialMediaBytes=sum(value["size"] for value in before_inputs.values()), seeds={name: digest(temp / ("seed-" + name + ".mp4")) for name in ("a", "b")}, result="passed")
        finally:
            # Only this invocation's UUID resources are removed. Never prune.
            cleanup_failed = False
            def remove_owned(kind, name):
                nonlocal cleanup_failed
                try:
                    exists = run(["docker", kind, "inspect", name, "--format", "{{.Id}}"], timeout=15, check=False)
                    if exists.returncode == 0:
                        cleanup_failed |= run(["docker", kind, "rm", "--force", name], timeout=30, check=False).returncode != 0
                except (OSError, subprocess.SubprocessError):
                    cleanup_failed = True
            remove_owned("container", container)
            try:
                if cleanup_ready:
                    cleanup_failed |= run(["docker", "run", "--name", cleanup_container, "--network", "host", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "256m", "--pids-limit", "32", "--env-file", str(secret_file), "--entrypoint", "/worker.test", image, "--cleanup-probe-worker"], timeout=30, check=False).returncode != 0
            except (OSError, subprocess.SubprocessError):
                cleanup_failed = True
            finally:
                remove_owned("container", cleanup_container)
                remove_owned("image", image)
                remove_owned("image", base)
            if cleanup_failed:
                raise RuntimeError("owned acceptance schema/container/image cleanup failed")
    report["testArtifactsCleaned"] = True
    (ROOT / ".testdata/probe-worker-summary.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": "passed", "metadataProberCalls": [1000, 0, 17], "originalHashesUnchanged": True, "testArtifactsCleaned": True}), flush=True)


def terminate(signum, _frame):
    raise SystemExit(128 + signum)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, terminate)
    try:
        main()
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError, KeyError, TypeError) as error:
        print("probe worker acceptance failed: " + str(error), file=sys.stderr)
        sys.exit(1)
