#!/usr/bin/env python3
"""Real 400 NFO / 100 video / 500 image acceptance; no synthetic operation counts."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import uuid
from urllib.parse import urlparse
import zlib

sys.dont_write_bytecode = True
from test_probe_runtime import digest
from test_probe_worker import source_digest as probe_source_digest
from test_sandbox_native import select_fixtures

ROOT = Path(__file__).resolve().parent.parent
WITH_FAMILY = os.environ.get("JELEE_FAMILY_IGNORE_ACCEPTANCE") == "true"
WITH_IGNORE = WITH_FAMILY or os.environ.get("JELEE_NFO_IGNORE_ACCEPTANCE") == "true"
EVIDENCE_PREFIX = "family-ignore-worker" if WITH_FAMILY else ("ignore-worker" if WITH_IGNORE else "nfo-worker")


def source_digest():
    record = {"sharedSources": probe_source_digest(), "controller": digest(Path(__file__).resolve())}
    return hashlib.sha256(json.dumps(record, sort_keys=True).encode()).hexdigest()


def png(width):
    def chunk(kind, body):
        return struct.pack(">I", len(body)) + kind + body + struct.pack(">I", zlib.crc32(kind + body))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, 1, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(b"\x00" + b"\x11\x22\x33" * width)) + chunk(b"IEND", b""))


def nfo_bytes(index, count, changed=False):
    faults = count // 40
    if index >= count - faults:
        return b"<movie><title>PRIVATE_FIXTURE broken</movie>"
    text = "<movie><title>PRIVATE_FIXTURE 测试片名 %03d</title><year>%d</year><plot>Generated text.</plot></movie>" % (index, 2025 if changed else 2024)
    if index >= count - 2 * faults:
        text = "<movie><title>PRIVATE_FIXTURE semantic</title><year>invalid</year></movie>"
    elif index >= count - 3 * faults:
        text = "<episode/>"
    elif index >= count - 4 * faults:
        text = "<episodedetails><title>PRIVATE_FIXTURE one</title><season>1</season><episode>1</episode></episodedetails><episodedetails><title>PRIVATE_FIXTURE two</title><season>1</season><episode>2</episode></episodedetails>"
    encoding = ("UTF-8", "UTF-16LE", "UTF-16BE", "GBK")[index * 4 // count]
    label = "UTF-16" if encoding.startswith("UTF-16") else encoding
    payload = ('<?xml version="1.0" encoding="%s"?>' % label + text).encode(encoding)
    return ({"UTF-16LE": b"\xff\xfe", "UTF-16BE": b"\xfe\xff"}.get(encoding, b"") + payload)


def snapshot(directory):
    return {p.name: {"sha256": digest(p), "bytes": p.stat().st_size, "mtimeNanos": p.stat().st_mtime_ns}
            for p in directory.iterdir()}


def run_case(total):
    if sys.platform != "linux":
        raise RuntimeError("NFO worker acceptance requires Linux Docker")
    dsn = os.environ.get("JELEE_TEST_DATABASE_URL", "")
    parsed = urlparse(dsn)
    if parsed.scheme not in ("postgres", "postgresql") or parsed.path != "/jelee_test" or not parsed.hostname or any(c in dsn for c in "\r\n"):
        raise RuntimeError("dedicated jelee_test PostgreSQL required")
    media = json.loads((ROOT / "tools/manifest.json").read_text())["mediaTools"]["platforms"]["linux-amd64"]
    fixtures, original_hashes = select_fixtures(None, media)
    before_source = source_digest()
    (ROOT / ".testdata").mkdir(exist_ok=True)
    identity = uuid.uuid4().hex
    base = "jelee/jelee:nfo-worker-" + identity
    image, container = base + "-test", "jelee-nfo-worker-" + identity
    cleanup_container = container + "-cleanup"
    schema = "jelee_probe_worker_" + identity  # Existing test helper validates this exact owned prefix.
    nfo_count, video_count, image_count = total * 4 // 10, total // 10, total // 2
    changed_nfo, changed_images = (17, 23) if total == 1000 else (3, 4)
    report = {"sourceDigest": before_source, "profile": "Linux amd64 UID65532; readonly root/media; no capabilities; no-new-privileges; 2 CPUs/768MiB/128 PIDs", "fixtureFiles": total, "fixtures": {"nfo": nfo_count, "video": video_count, "image": image_count, "invalidXML": total // 100, "semanticInvalid": total // 100, "warningFiles": total // 100, "multiEpisodeFiles": total // 100}, "rounds": []}
    report["ignoreEnabled"] = WITH_IGNORE
    report["familyIgnoreEnabled"] = WITH_FAMILY
    transcript = ROOT / (".testdata/" + EVIDENCE_PREFIX + "-acceptance.txt")
    with transcript.open("a", encoding="utf-8") as log, tempfile.TemporaryDirectory(prefix="nfo-worker-", dir=ROOT / ".testdata") as temp:
        log.write("\nFIXTURE FILE COUNT %d\n" % total)
        temp = Path(temp)
        secret_file = temp / "database.env"
        cleanup_ready = False

        def run(argv, timeout=300, check=True, **kwargs):
            # argv contains no credentials; the secret stays in the private env file.
            log.write("$ " + " ".join(str(a) for a in argv) + "\n"); log.flush()
            result = subprocess.run(argv, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout, **kwargs)
            output = result.stdout.decode(errors="replace").replace(dsn, "[redacted database URL]")
            log.write(output); log.flush()
            if check and result.returncode:
                raise RuntimeError("NFO acceptance command failed; inspect ignored log")
            return result

        try:
            run(["docker", "build", "--network", "host", "-t", base, "."], timeout=600)
            report["productionImage"] = run(["docker", "image", "inspect", base, "--format", "{{.Id}}"]).stdout.decode().strip()
            env = dict(os.environ, CGO_ENABLED="0")
            run([str(ROOT / ".bin/go"), "test", "-tags", "jelee_probe_tests", "-c", "-o", str(temp / "worker.test"), "./internal/platform/runtime"], env=env)
            (temp / "Dockerfile").write_text("FROM " + base + "\nCOPY --chmod=0555 worker.test /worker.test\n")
            run(["docker", "build", "--network", "none", "-t", image, str(temp)])
            # No secrets or media were present in the derived build context.
            secret_file.write_text("JELEE_TEST_DATABASE_URL=" + dsn + "\nJELEE_REQUIRE_NFO_WORKER=true\nJELEE_PROBE_TEST_SCHEMA=" + schema + "\nJELEE_NFO_FIXTURE_FILES=" + str(total) + "\n")
            if WITH_IGNORE:
                with secret_file.open("a") as env_file:
                    env_file.write("JELEE_NFO_IGNORE_ACCEPTANCE=true\n")
                    if WITH_FAMILY:
                        env_file.write("JELEE_FAMILY_IGNORE_ACCEPTANCE=true\n")
            secret_file.chmod(0o600); cleanup_ready = True
            inputs = temp / "media"; inputs.mkdir()
            control = temp / "control"; control.mkdir()
            shutil.copyfile(fixtures / "video-180p.mp4", temp / "seed.mp4")
            for n in range(video_count):
                os.link(temp / "seed.mp4", inputs / ("video-%04d.mp4" % n))
            for n in range(nfo_count):
                (inputs / ("metadata-%04d.nfo" % n)).write_bytes(nfo_bytes(n, nfo_count))
            for n in range(image_count):
                (inputs / ("image-%04d.png" % n)).write_bytes(png(1))
            if WITH_IGNORE:
                (inputs / ".jeleeignore").write_bytes(b"ignored-*\n")
                (inputs / "ignored-video.mp4").write_bytes(b"invalid excluded video")
                (inputs / "ignored-metadata.nfo").write_bytes(b"<movie><broken>")
                (inputs / "ignored-image.png").write_bytes(b"invalid excluded image")
            if WITH_FAMILY:
                (inputs / ".jeleeignore").write_bytes(b"ignored-video.mp4\n!video-*\n")
                (inputs / ".ignore").write_bytes(b"ignored-*\nvideo-*\n")
            before_inputs = snapshot(inputs)
            run(["docker", "run", "-d", "--name", container, "--read-only", "--network", "host", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "768m", "--pids-limit", "128", "--cpus", "2", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--env-file", str(secret_file), "--volume", str(inputs) + ":/media:ro", "--volume", str(control) + ":/control:ro", "--entrypoint", "/worker.test", image, "-test.v", "-test.run", "^TestProductionNFOWorkerAcceptance$", "-test.timeout", "16m"])
            deadline = time.monotonic() + 17 * 60
            replaced, terminated = False, False
            reported_rounds = set()
            latest = ""
            while time.monotonic() < deadline:
                state = json.loads(subprocess.run(["docker", "inspect", container, "--format", "{{json .State}}"], check=True, stdout=subprocess.PIPE, timeout=15).stdout)
                latest = subprocess.run(["docker", "logs", container], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=15).stdout.decode(errors="replace")
                for line in latest.splitlines():
                    if not line.startswith("{"):
                        continue
                    entry = json.loads(line)
                    if "round" in entry and entry["round"] not in reported_rounds:
                        report["rounds"].append(entry); reported_rounds.add(entry["round"])
                        print(json.dumps(dict(entry, fixtureFiles=total)), flush=True)
                    if entry.get("shutdown") == "passed":
                        report["shutdown"] = entry
                    if entry.get("cancellationRecovery") == "passed":
                        report["cancellationRecovery"] = entry
                if not replaced and '{"readyForReplacement":true}' in latest:
                    for n in range(changed_nfo):
                        incoming = inputs / ("replacement-%04d" % n)
                        incoming.write_bytes(nfo_bytes(n, nfo_count, changed=True))
                        os.replace(incoming, inputs / ("metadata-%04d.nfo" % n))
                    for n in range(changed_images):
                        incoming = inputs / ("replacement-image-%04d" % n)
                        incoming.write_bytes(png(2))
                        os.replace(incoming, inputs / ("image-%04d.png" % n))
                    (control / "continue").write_text("%d NFO and %d image owned replacements\n" % (changed_nfo, changed_images))
                    replaced = True
                if not terminated and '{"readyForSIGTERM":true}' in latest:
                    run(["docker", "kill", "--signal", "SIGTERM", container], timeout=15)
                    terminated = True
                if not state["Running"]:
                    log.write(latest.replace(dsn, "[redacted database URL]")); log.flush()
                    if state["ExitCode"] != 0 or not replaced or not terminated or "--- SKIP:" in latest or "\nPASS\n" not in latest or "shutdown" not in report or "cancellationRecovery" not in report:
                        raise RuntimeError("required mixed acceptance failed or skipped")
                    break
                time.sleep(1)
            else:
                log.write(latest.replace(dsn, "[redacted database URL]")); log.flush()
                raise RuntimeError("mixed acceptance timeout")
            changed = sorted(name for name, value in before_inputs.items() if snapshot_value(inputs / name) != value)
            expected = sorted(["metadata-%04d.nfo" % n for n in range(changed_nfo)] + ["image-%04d.png" % n for n in range(changed_images)])
            if changed != expected or len(list(inputs.iterdir())) != total + (5 if WITH_FAMILY else (4 if WITH_IGNORE else 0)):
                raise RuntimeError("unexpected fixture mutation")
            if original_hashes != {name: digest(fixtures / name) for name in original_hashes} or before_source != source_digest():
                raise RuntimeError("original fixtures or source changed")
            if [r["parseCalls"] for r in report["rounds"]] != [nfo_count, 0, changed_nfo] or [r["metadataChildStarts"] for r in report["rounds"]] != [video_count, 0, 0]:
                raise RuntimeError("actual invocation counts differ")
            report.update(result="passed", originalFixtureHashesUnchanged=True, controlledNFOReplacements=changed_nfo, controlledImageReplacements=changed_images, unchangedVideoHashes=True, sourceUnchanged=True)
        finally:
            cleanup_failed = False
            def remove_owned(kind, name):
                nonlocal cleanup_failed
                try:
                    if run(["docker", kind, "inspect", name, "--format", "{{.Id}}"], timeout=15, check=False).returncode == 0:
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
                remove_owned("container", cleanup_container); remove_owned("image", image); remove_owned("image", base)
            if cleanup_failed:
                raise RuntimeError("owned schema/container/image cleanup failed")
    report["testArtifactsCleaned"] = True
    return report


def main():
    (ROOT / ".testdata").mkdir(exist_ok=True)
    (ROOT / (".testdata/" + EVIDENCE_PREFIX + "-acceptance.txt")).write_text("")
    report = {"cases": [run_case(1000), run_case(100)], "result": "passed"}
    (ROOT / (".testdata/" + EVIDENCE_PREFIX + "-summary.json")).write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": "passed", "fixtureFiles": [1000, 100], "SIGTERMJoined": True, "testArtifactsCleaned": True}), flush=True)


def snapshot_value(path):
    info = path.stat()
    return {"sha256": digest(path), "bytes": info.st_size, "mtimeNanos": info.st_mtime_ns}


def terminate(signum, _frame):
    raise SystemExit(128 + signum)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, terminate)
    try:
        main()
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError, KeyError, TypeError) as error:
        print("NFO worker acceptance failed: " + str(error), file=sys.stderr)
        sys.exit(1)
