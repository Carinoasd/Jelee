"""Run the fixed five-minute native service acceptance and retain bounded evidence."""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import shutil
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent.parent
PREFIX = sys.argv[1] if len(sys.argv) == 2 else "ignore-sustained"
if len(sys.argv) > 2 or not re.fullmatch(r"[a-z0-9-]{1,60}", PREFIX):
    raise SystemExit("expected an optional evidence name containing lowercase letters, digits and hyphens")
if sys.platform not in ("linux", "win32"):
    raise SystemExit("native acceptance requires Linux or Windows")
log = ROOT / ".testdata" / (PREFIX + "-acceptance.jsonl")
summary = ROOT / ".testdata" / (PREFIX + "-summary.json")
log.parent.mkdir(exist_ok=True)
if log.exists() or summary.exists():
    raise SystemExit("refusing to replace retained evidence; use a fresh evidence name")
files = []
for folder in ("internal/platform/runtime", "internal/platform/process", "internal/platform/legacyignore", "internal/platform/legacyignorehelper"):
    files.extend(sorted((ROOT / folder).glob("*.go")))
files.extend(ROOT / name for name in ("go.mod", "go.sum", "tools/manifest.json", "LICENSE", "docs/requirements-source.md", "scripts/test_ignore_sustained.py", "scripts/toolchain.py", "scripts/run-go.ps1", "scripts/toolchain-lib.ps1"))
source_hashes = {str(p.relative_to(ROOT)).replace("\\", "/"): hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
go_args = ["test", "-count=1", "-json", "-timeout=7m", "-run", "^TestIgnoreServiceNativeSustainedSaturation$", "./internal/platform/runtime"]
if os.name == "nt":
    powershell = shutil.which("pwsh")
    if not powershell:
        raise SystemExit("PowerShell 7 is required; use the repository's Windows toolchain")
    command = [powershell, "-NoProfile", "-File", str(ROOT / "scripts/run-go.ps1")] + go_args
else:
    command = [sys.executable, str(ROOT / "scripts/toolchain.py"), "go"] + go_args
env = dict(os.environ, JELEE_IGNORE_SUSTAINED_ACCEPTANCE="true")
started = time.monotonic()
timed_out = False
with log.open("x", encoding="utf-8") as output:
    process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=os.name != "nt")
    try:
        exit_code = process.wait(timeout=480)
    except subprocess.TimeoutExpired:
        timed_out = True
        if os.name == "nt":
            subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        else:
            os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            if os.name != "nt":
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
            process.wait()
        exit_code = 124
body = log.read_text(encoding="utf-8")
passed, failed, skipped = [], [], []
measurement = None
package_passed = False
for line in body.splitlines():
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        continue
    name = event.get("Test")
    action = event.get("Action")
    if action == "pass" and name and "/" not in name:
        passed.append(name)
    if action == "pass" and not name:
        package_passed = True
    if action == "fail":
        failed.append(name or event.get("Package"))
    if action == "skip":
        skipped.append(name or event.get("Package"))
    text = event.get("Output", "")
    marker = "native saturation: "
    if marker in text:
        fields = text.split(marker, 1)[1].strip().split()
        measurement = {k: float(v) if k == "seconds" else int(v) for k, v in (field.split("=", 1) for field in fields)}
unchanged = all((ROOT / name).exists() and hashlib.sha256((ROOT / name).read_bytes()).hexdigest() == digest for name, digest in source_hashes.items())
valid = (exit_code == 0 and not failed and not skipped and package_passed and unchanged and passed == ["TestIgnoreServiceNativeSustainedSaturation"] and measurement is not None)
if valid:
    valid = (measurement["seconds"] >= 300 and measurement["rounds"] >= 100 and measurement["peak"] == 2 and measurement["active"] == 0 and measurement["timedOut"] == 0 and measurement["cancelled"] == 2 * measurement["rounds"] and measurement["rejected"] == 32 * measurement["rounds"] and measurement["started"] == 1 + 3 * measurement["rounds"])
report = {"exit": exit_code, "timeout": timed_out, "seconds": round(time.monotonic() - started, 3), "platform": sys.platform, "command": ["project-pinned-go"] + go_args, "passed": passed, "failed": failed, "skipped": skipped, "measurement": measurement, "sourceUnchanged": unchanged, "sourceHashes": source_hashes, "logSHA256": hashlib.sha256(log.read_bytes()).hexdigest(), "validated": valid}
summary.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
print(json.dumps({k: report[k] for k in ("exit", "seconds", "passed", "failed", "skipped", "measurement", "sourceUnchanged", "validated")}))
raise SystemExit(0 if valid else 1)
