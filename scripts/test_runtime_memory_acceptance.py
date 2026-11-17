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
from test_container_memory import inspect_fixture

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

    def oom_run(self, calls):
        def run(argv, **kwargs):
            calls.append(argv[:3])
            return subprocess.CompletedProcess(argv, 0, ARMED if argv[:2] == ["docker", "logs"] else b"")
        return run

    def test_late_docker_oom_flag_is_awaited_not_assumed(self):
        record, calls = {}, []
        late = [inspect_fixture(oom=True) for _ in range(3)]
        late[0]["State"]["OOMKilled"] = late[1]["State"]["OOMKilled"] = False
        with patch.object(acceptance, "inspect_owned", side_effect=late + [inspect_fixture(oom=True)]), \
                patch.object(acceptance.time, "sleep"):
            acceptance.check_oom_negative(self.oom_run(calls), "image", "name", record)
        self.assertEqual(record["result"], "passed")
        self.assertEqual((record["attempts"], record["oomFlagMissingAttempts"]), (1, 0))
        self.assertTrue(record["container"]["oomKilled"])
        self.assertNotIn("secret", json.dumps(record))

    def test_missing_docker_oom_flag_starts_a_fresh_probe(self):
        record, calls = {}, []
        missing = inspect_fixture(oom=True)
        missing["State"]["OOMKilled"] = False
        # Two attempts never receive the flag; the third probe is fully verified.
        inspections = [missing, missing, missing, missing, inspect_fixture(oom=True), inspect_fixture(oom=True)]
        with patch.object(acceptance, "inspect_owned", side_effect=inspections), \
                patch.object(acceptance, "OOM_FLAG_SETTLE_SECONDS", 0):
            acceptance.check_oom_negative(self.oom_run(calls), "image", "name", record)
        self.assertEqual(record["result"], "passed")
        self.assertEqual((record["attempts"], record["oomFlagMissingAttempts"]), (3, 2))
        self.assertNotIn("rejectedCheck", record)
        self.assertEqual(calls.count(["docker", "run", "-d"]), 3)
        self.assertEqual(calls.count(["docker", "container", "rm"]), 3)

    def test_missing_docker_oom_flag_is_never_accepted(self):
        record, calls = {}, []
        missing = inspect_fixture(oom=True)
        missing["State"]["OOMKilled"] = False
        with patch.object(acceptance, "inspect_owned", return_value=missing), \
                patch.object(acceptance, "OOM_FLAG_SETTLE_SECONDS", 0):
            with self.assertRaisesRegex(acceptance.Rejected, "^container_memory_evidence_invalid$"):
                acceptance.check_oom_negative(self.oom_run(calls), "image", "name", record)
        self.assertEqual(record["result"], "failed")
        self.assertEqual(record["rejectedCheck"], "state_oom_killed")
        self.assertEqual(record["attempts"], acceptance.OOM_PROBE_ATTEMPTS)
        self.assertEqual(calls.count(["docker", "container", "rm"]), acceptance.OOM_PROBE_ATTEMPTS)
        self.assertNotIn("secret", json.dumps(record))

    def test_other_oom_probe_contract_failures_are_not_retried(self):
        for path, value, check in ((("State", "ExitCode"), 1, "state_exit_code"),
                                   (("HostConfig", "Memory"), 805306368, "host_memory"),
                                   (("HostConfig", "ReadonlyRootfs"), False, "host_readonly_rootfs"),
                                   (("HostConfig", "CapDrop"), [], "host_cap_drop"),
                                   (("HostConfig", "SecurityOpt"), [], "host_security_opt")):
            record, calls = {}, []
            invalid = inspect_fixture(oom=True)
            parent = invalid
            for key in path[:-1]:
                parent = parent[key]
            parent[path[-1]] = value
            with self.subTest(path=path), patch.object(acceptance, "inspect_owned", return_value=invalid), \
                    patch.object(acceptance, "OOM_FLAG_SETTLE_SECONDS", 0):
                with self.assertRaises(acceptance.Rejected):
                    acceptance.check_oom_negative(self.oom_run(calls), "image", "name", record)
                self.assertEqual(record["attempts"], 1)
                self.assertEqual(record["rejectedCheck"], check)
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
