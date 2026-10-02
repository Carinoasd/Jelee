#!/usr/bin/env python3
"""Run fixed memory contracts and retain safe evidence before any workload.

Only fixed step names, states, elapsed time and exit codes enter the report.
Child output and exceptions may contain credentials and are never retained.
"""

import json
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent.parent
REPORT = ROOT / ".testdata/runtime-memory-contracts.json"
FAILURE = "Runtime memory contracts failed; inspect the safe contracts report."


def _write_report(report):
    REPORT.parent.mkdir(exist_ok=True)
    # Replacing a complete temporary document keeps earlier evidence readable
    # if the controller is interrupted while updating the next step.
    pending = REPORT.with_suffix(".json.tmp")
    pending.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    pending.replace(REPORT)


def run_contracts():
    """Return the failed child status, using shell status for a signal death."""
    commands = (
        ("container-evidence", [sys.executable, "-B", "scripts/test_container_memory.py"], 120),
        ("controller-evidence", [sys.executable, "-B", "scripts/test_runtime_memory_acceptance.py"], 120),
        ("resident-evidence", [sys.executable, "-B", "scripts/test_resident_memory.py"], 120),
        ("resident-controller", [sys.executable, "-B", "scripts/test_resident_controller.py"], 120),
        ("heap-evidence", [sys.executable, "-B", "scripts/test_heap_profile_acceptance.py"], 180),
        ("heap-controller", [sys.executable, "-B", "scripts/test_runtime_heap_controller.py"], 120),
        ("compose-configuration", [sys.executable, "-B", "scripts/check_memory_compose.py"], 180),
        ("native-runtime", [str(ROOT / ".bin/go"), "test", "-tags", "jelee_probe_tests", "-count=1",
                            "-run", "^Test(MemoryProfile(RuntimeSubprocess|CgroupEvidenceRequired)$|Resident|HeapProfile)",
                            "./internal/platform/runtime"], 600),
    )
    report = {"version": 1, "result": "failed", "scope": "memory contracts only",
              "workloadStarted": False, "childOutputRetained": False,
              "steps": [{"name": name, "status": "not_run"} for name, _, _ in commands]}
    _write_report(report)
    for step, (_, argv, timeout) in zip(report["steps"], commands):
        step["status"] = "running"
        _write_report(report)
        started = time.monotonic()
        try:
            result = subprocess.run(argv, cwd=ROOT, stdout=subprocess.DEVNULL,
                                    stderr=subprocess.DEVNULL, timeout=timeout)
            # Keep the subprocess return code verbatim in evidence. POSIX
            # subprocess uses -N for signal N; shells report that as 128+N.
            step["returnCode"] = result.returncode
            exit_code = result.returncode if result.returncode >= 0 else 128 - result.returncode
            step["status"] = "passed" if exit_code == 0 else "failed"
        except subprocess.TimeoutExpired:
            step.update(status="failed", failureCode="timeout")
            exit_code = 124
        except OSError:
            step.update(status="failed", failureCode="command_unavailable")
            exit_code = 127
        except KeyboardInterrupt:
            step.update(status="failed", failureCode="interrupted")
            exit_code = 130
        step["elapsedMillis"] = int((time.monotonic() - started) * 1000)
        step["exitCode"] = exit_code
        report["exitCode"] = exit_code
        if exit_code:
            _write_report(report)
            return exit_code
        _write_report(report)
    report["result"] = "passed"
    _write_report(report)
    return 0


def main():
    if len(sys.argv) != 1:
        print(FAILURE, file=sys.stderr)
        return 2
    try:
        exit_code = run_contracts()
    except (OSError, ValueError, subprocess.SubprocessError):
        # Filesystem and process exceptions can embed paths or private values.
        print(FAILURE, file=sys.stderr)
        return 1
    if exit_code:
        print(FAILURE, file=sys.stderr)
    else:
        print("Runtime memory contracts passed; safe report saved.")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
