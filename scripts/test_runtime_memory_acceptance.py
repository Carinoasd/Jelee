"""Failure evidence must survive cleanup without retaining private inspect data."""
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

import runtime_memory_acceptance as acceptance

ARMED = b'{"memoryOOMProbe":"armed","hardLimitBytes":67108864,"attemptBytes":134217728}\n'

def inspection(running=False, code=1, oom=False):
    return {"State": {"Status": "running" if running else "exited", "Running": running,
                      "ExitCode": code, "OOMKilled": oom, "Error": "PRIVATE_ERROR"},
            "Config": {"Env": ["DATABASE_URL=PRIVATE_SECRET"]}, "Mounts": ["PRIVATE_PATH"]}


class FailureEvidenceTests(unittest.TestCase):
    def test_previous_worker_log_survives_failed_fresh_read(self):
        dsn = "postgres://PRIVATE_SECRET@localhost/db"
        for failure in (1, subprocess.TimeoutExpired("docker", 15)):
            log = io.StringIO()
            def run(argv, **kwargs):
                self.assertIn("[redacted database URL]", log.getvalue())
                self.assertNotIn("PRIVATE_SECRET", log.getvalue())
                self.assertFalse(kwargs["check"])
                if isinstance(failure, Exception):
                    raise failure
                return subprocess.CompletedProcess(argv, failure, b"")
            with self.subTest(failure=type(failure).__name__):
                result = acceptance.retain_worker_failure_log(run, log, "owned", "last read: " + dsn, dsn)
                self.assertEqual(result, {"previousReadLogRetained": True, "freshReadSucceeded": False})

    def test_fresh_worker_log_status_follows_exit_code(self):
        run = Mock(return_value=subprocess.CompletedProcess([], 0, b""))
        result = acceptance.retain_worker_failure_log(run, io.StringIO(), "owned", "", "dsn")
        self.assertEqual(result, {"previousReadLogRetained": False, "freshReadSucceeded": True})

    def test_startup_failure_retained_before_removal(self):
        record = {}
        calls = []
        with tempfile.TemporaryDirectory(prefix="memory-contract-", dir=Path.cwd()) as folder:
            def run(argv, **kwargs):
                calls.append(argv)
                if argv[:3] == ["docker", "container", "rm"]:
                    self.assertEqual(record["stage"], "readiness")
                    self.assertEqual(record["stateBeforeCleanup"]["exitCode"], 1)
                return subprocess.CompletedProcess(argv, 0, b"")

            with patch.object(acceptance, "inspect_owned", return_value=inspection()):
                with self.assertRaisesRegex(RuntimeError, "exited during startup"):
                    acceptance.check_production_entry(run, "owned-image", "owned-name", Path(folder),
                                                      "postgres://user:PRIVATE_SECRET@localhost/jelee_test",
                                                      "owned_schema", 100, record)
            self.assertFalse((Path(folder) / "main-memory.env").exists())
        self.assertEqual(record["result"], "failed")
        self.assertTrue(record["containerRemoved"])
        self.assertEqual(len(calls), 2)
        self.assertNotIn("PRIVATE", json.dumps(record))

    def test_observer_failure_has_stage_and_precleanup_running_state(self):
        record = {}
        response = Mock(status=200)
        response.read.return_value = b'{"data":{"status":"ready"}}'
        opener = Mock()
        opener.open.return_value = contextlib.nullcontext(response)
        with tempfile.TemporaryDirectory(prefix="memory-contract-", dir=Path.cwd()) as folder, \
                patch.object(acceptance, "inspect_owned", return_value=inspection(running=True, code=0)), \
                patch.object(acceptance, "build_opener", return_value=opener), \
                patch.object(acceptance, "snapshot_container", side_effect=RuntimeError("observer failed")):
            with self.assertRaisesRegex(RuntimeError, "observer failed"):
                acceptance.check_production_entry(Mock(), "image", "name", Path(folder),
                                                  "postgres://user:secret@localhost/jelee_test", "schema", 50, record)
        self.assertEqual(record["stage"], "observerBefore")
        self.assertTrue(record["stateBeforeCleanup"]["running"])
        self.assertTrue(record["containerRemoved"])
        self.assertEqual(record["result"], "failed")

    def test_oom_wait_failure_retained_before_removal(self):
        record = {}
        def run(argv, **kwargs):
            if argv[:2] == ["docker", "wait"]:
                raise subprocess.TimeoutExpired("docker", 30)
            if argv[:3] == ["docker", "container", "rm"]:
                self.assertEqual(record["stateBeforeCleanup"]["exitCode"], 137)
                self.assertTrue(record["stateBeforeCleanup"]["oomKilled"])
            return subprocess.CompletedProcess(argv, 0, b"")
        with patch.object(acceptance, "inspect_owned", return_value=inspection(code=137, oom=True)):
            with self.assertRaises(subprocess.TimeoutExpired):
                acceptance.check_oom_negative(run, "image", "name", record)
        self.assertEqual(record["stage"], "wait")
        self.assertEqual(record["result"], "failed")
        self.assertTrue(record["containerRemoved"])
        self.assertNotIn("PRIVATE", json.dumps(record))

    def test_cleanup_failure_cannot_report_passed(self):
        record = {}
        def run(argv, **kwargs):
            if argv[:3] == ["docker", "container", "rm"]:
                raise RuntimeError("cleanup failed")
            return subprocess.CompletedProcess(argv, 0, ARMED)
        with patch.object(acceptance, "inspect_owned", return_value=inspection(code=137, oom=True)), \
                patch.object(acceptance, "validate_oom_negative", return_value={"validated": True}):
            with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
                acceptance.check_oom_negative(run, "image", "name", record)
        self.assertEqual(record["result"], "failed")
        self.assertFalse(record["containerRemoved"])
        self.assertEqual(record["stateBeforeCleanup"]["exitCode"], 137)

    def test_oom_before_allocation_or_invalid_marker_is_rejected(self):
        for logs in (b"", ARMED + ARMED, b'{"memoryOOMProbe":"armed"}\n', b'{"memoryOOMProbe":INVALID}\n'):
            record = {}
            run = Mock(return_value=subprocess.CompletedProcess([], 0, logs))
            with self.subTest(logs=logs), \
                    patch.object(acceptance, "inspect_owned", return_value=inspection(code=137, oom=True)), \
                    patch.object(acceptance, "validate_oom_negative") as validate:
                with self.assertRaisesRegex(RuntimeError, "allocation phase"):
                    acceptance.check_oom_negative(run, "image", "name", record)
                validate.assert_not_called()
            self.assertEqual(record["result"], "failed")
            self.assertTrue(record["containerRemoved"])

    def test_unavailable_or_malformed_inspection_never_invents_success(self):
        with patch.object(acceptance, "inspect_owned", side_effect=RuntimeError("PRIVATE_ERROR")):
            self.assertEqual(acceptance.container_state("name"), {"inspectionAvailable": False})
        for key, value in (("Status", "PRIVATE_ERROR"), ("ExitCode", True),
                           ("ExitCode", -1), ("Running", 1), ("OOMKilled", "false")):
            malformed = inspection()
            malformed["State"][key] = value
            with self.subTest(key=key, value=value), \
                    patch.object(acceptance, "inspect_owned", return_value=malformed):
                self.assertEqual(acceptance.container_state("name"), {"inspectionAvailable": False})


if __name__ == "__main__":
    unittest.main()
