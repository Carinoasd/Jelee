"""Small controller contracts; no Docker workload or large fixture generation."""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import types
import unittest
from unittest.mock import patch

import test_scan_memory as controller


class ControllerContracts(unittest.TestCase):
    def setUp(self):
        temporary_parent = controller.ROOT / ".testdata"
        temporary_parent.mkdir(exist_ok=True)
        self.temporary = tempfile.TemporaryDirectory(prefix="scan-controller-", dir=temporary_parent)
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()

    def test_fixture_bytes_layout_and_samples_are_real(self):
        directory = self.root / "media"
        before = controller.create_fixtures(directory, 1000)
        self.assertEqual(len(controller.FIXTURE), 34)
        self.assertEqual(len(list((directory / "dir-0000").iterdir())), 1000)
        self.assertEqual(controller.fixture_path(directory, 1000).relative_to(directory).as_posix(), "dir-0001/video-001000.mkv")
        self.assertEqual(before, controller.fixture_sample(directory, 1000))
        controller.fixture_path(directory, 500).write_bytes(b"changed")
        with self.assertRaisesRegex(controller.ScanFailure, "^scan_memory_fixture_changed$"):
            controller.fixture_sample(directory, 1000)
        with self.assertRaises(controller.ScanFailure):
            controller.create_fixtures(self.root / "invalid", 999)
        self.assertFalse((self.root / "invalid").exists())

    def test_mount_and_capacity_require_native_persistent_space(self):
        mounts = (b"1 0 8:1 / / rw - ext4 /dev/root rw\n"
                  b"2 1 0:1 / /tmp/special rw - tmpfs tmpfs rw\n"
                  b"3 1 8:2 / /space\\040root rw - ext4 /dev/data rw\n")
        self.assertEqual(controller.mount_filesystem(mounts, "/tmp/owned"), "ext4")
        self.assertEqual(controller.mount_filesystem(mounts, "/tmp/special/owned"), "tmpfs")
        self.assertEqual(controller.mount_filesystem(mounts, "/space root/owned"), "ext4")
        enough = b"5242880 4096 510000"
        self.assertEqual(controller.storage_record("ext4", enough, 510000)["availableBytes"], 20 * 1024**3)
        for fs, data in (("tmpfs", enough), ("overlay", enough), ("9p", enough),
                         ("ext4", b"5242879 4096 510000"), ("ext4", b"5242880 4096 509999"),
                         ("ext4", b"-1 4096 510000"), ("ext4", b"99999999999999999999 999999 510000")):
            with self.subTest(fs=fs, data=data), self.assertRaises(controller.ScanFailure):
                controller.storage_record(fs, data, 510000)

    def test_postgres_probe_must_match_dsn_port_and_disk_mount(self):
        inspected = {"NetworkSettings": {"Ports": {"5432/tcp": [{"HostIp": "127.0.0.1", "HostPort": "55437"}]}},
                     "Config": {"Env": ["PGDATA=/var/lib/postgresql/data", "PASSWORD=PRIVATE"]},
                     "Mounts": [{"Type": "bind", "Destination": "/var/lib/postgresql/data", "Source": "PRIVATE_PATH"}]}
        def capture(argv, **kwargs):
            self.assertNotIn("PRIVATE", " ".join(argv))
            body = b"5242880 4096 510000" if "stat" in argv else b"1 0 8:1 / /var/lib/postgresql/data rw - ext4 /dev/root rw\n"
            return subprocess.CompletedProcess(argv, 0, body)
        with patch.object(controller, "inspect_owned", return_value=inspected), patch.object(controller, "capture", side_effect=capture) as commands:
            value = controller.check_postgres_storage("postgres://u:PRIVATE@127.0.0.1:55437/jelee_test", "owned-db")
            self.assertEqual(value["filesystem"], "ext4")
            self.assertNotIn("PRIVATE", json.dumps(value))
            calls = commands.call_count
            with self.assertRaises(controller.ScanFailure):
                controller.check_postgres_storage("postgres://u:PRIVATE@127.0.0.1:55438/jelee_test", "owned-db")
            self.assertEqual(commands.call_count, calls)
            inspected["Mounts"][0]["Type"] = "tmpfs"
            with self.assertRaises(controller.ScanFailure):
                controller.check_postgres_storage("postgres://u:PRIVATE@127.0.0.1:55437/jelee_test", "owned-db")
            self.assertEqual(commands.call_count, calls)

    def test_event_contract_rejects_duplicates_and_malformed_json(self):
        ready = b'{"scanMemoryReadyForSIGTERM":true}\n'
        final = b'{"scanMemoryAcceptance":{"result":"passed"}}\n'
        self.assertEqual(controller.parse_events(ready + final), (True, {"result": "passed"}))
        for body in (ready + ready, final + final, final + ready,
                     b'{"scanMemoryReadyForSIGTERM":1}\n', b'{"scanMemoryReadyForSIGTERM":true,"extra":0}\n',
                     b'{"scanMemoryAcceptance":{},"scanMemoryAcceptance":{}}\n',
                     b'{"scanMemoryAcceptance":{"bad":NaN}}\n', b'{"scanMemoryAcceptance":'):
            with self.subTest(body=body), self.assertRaises(controller.ScanFailure):
                controller.parse_events(body)

    def test_safe_log_bounds_and_redaction_survive_utf8(self):
        path = self.root / "private.txt"
        log = controller.SafeLog(path, ("postgres://u:SECRET@localhost/jelee_test", "SECRET", "C:\\Users\\Private"))
        try:
            log.write("postgres://u:SECRET@localhost/jelee_test SECRET C:\\Users\\Private 中文\n")
            log.flush()
            with patch.object(controller, "MAX_LOG_BYTES", log.bytes + 1):
                with self.assertRaisesRegex(controller.ScanFailure, "^scan_memory_log_limit$"):
                    log.write("中文")
        finally:
            log.close()
        body = path.read_text(encoding="utf-8")
        self.assertNotIn("SECRET", body)
        self.assertNotIn("Private", body)
        self.assertIn("中文", body)
        with self.assertRaises(FileExistsError):
            controller.SafeLog(path, ())

    def test_real_child_output_limit_timeout_and_nonzero(self):
        before_threads = set(threading.enumerate())
        for code, kwargs in (("import sys; sys.stdout.buffer.write(b'x'*65537); sys.stdout.flush()", {"limit": 65536}),
                             ("import time; time.sleep(5)", {"timeout": 0.1})):
            with self.subTest(code=code), self.assertRaisesRegex(controller.ScanFailure, "^scan_memory_command_limit$"):
                controller.capture([sys.executable, "-c", code], **kwargs)
        result = controller.capture([sys.executable, "-c", "import sys; print('bounded'); sys.exit(7)"])
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout.strip(), b"bounded")
        self.assertEqual(set(threading.enumerate()), before_threads)

    @unittest.skipUnless(os.name == "posix", "production controller requires native Linux process groups")
    def test_exited_child_inherited_pipe_is_still_deadline_bounded(self):
        # The parent exits immediately; its child keeps the pipe open. A wait
        # that watches only the direct process would hang in reader.join().
        before_threads = set(threading.enumerate())
        started = time.monotonic()
        code = "import subprocess,sys; subprocess.Popen([sys.executable,'-c','import time; time.sleep(10)'])"
        with self.assertRaisesRegex(controller.ScanFailure, "^scan_memory_command_limit$"):
            controller.capture([sys.executable, "-c", code], timeout=0.2)
        self.assertLess(time.monotonic() - started, 3)
        self.assertEqual(set(threading.enumerate()), before_threads)

    def exercise_case(self, mode):
        calls = []
        native = self.root / "native-parent" / "jelee-scan-memory-owned"
        native.parent.mkdir(exist_ok=True)
        native.mkdir()
        dsn = "postgres://user:PRIVATE_SECRET@127.0.0.1:55437/jelee_test"
        ready = b'{"scanMemoryReadyForSIGTERM":true}\n'
        final = {"version": 1, "result": "passed", "fixtureFiles": 1000}
        complete = ready + json.dumps({"scanMemoryAcceptance": final}).encode() + b"\nPASS\n"
        logs = [ready, complete]
        if mode == "missing-handshake":
            logs = [json.dumps({"scanMemoryAcceptance": final}).encode() + b"\nPASS\n"]
        if mode == "duplicate-event":
            logs = [ready + ready]
        if mode == "bad-json":
            logs = [b'{"scanMemoryAcceptance":']
        state = {"reads": 0}
        removed = set()

        def capture(argv, **kwargs):
            calls.append(list(argv))
            self.assertNotIn(dsn, " ".join(argv))
            self.assertNotIn("PRIVATE_SECRET", " ".join(argv))
            if "-o" in argv:
                Path(argv[argv.index("-o") + 1]).write_bytes(b"owned test binary")
            if argv[:2] == ["docker", "build"]:
                self.assertFalse((native / "database.env").exists())
                self.assertFalse((native / "media").exists())
            if argv[:2] == ["docker", "logs"]:
                index = min(state["reads"], len(logs) - 1)
                state["reads"] += 1
                return subprocess.CompletedProcess(argv, 0, logs[index])
            if argv[:3] == ["docker", "image", "inspect"]:
                return subprocess.CompletedProcess(argv, 0, b"sha256:" + b"a" * 64 + b"\n")
            if argv[:3] in (["docker", "container", "ls"], ["docker", "image", "ls"]):
                query = argv[argv.index("--filter") + 1]
                name = query.removeprefix("name=^/").removesuffix("$") if query.startswith("name=") else query.removeprefix("reference=")
                return subprocess.CompletedProcess(argv, 1 if mode == "cleanup-unverifiable" else 0,
                                                   b"" if name in removed else name.encode() + b"\n")
            if argv[:3] in (["docker", "container", "rm"], ["docker", "image", "rm"]):
                removed.add(argv[-1])
            if "--cleanup-probe-worker" in argv:
                self.assertTrue((native / "database.env").exists())
                return subprocess.CompletedProcess(argv, 1 if mode == "cleanup-failed" else 0, b"fixed cleanup status\n")
            return subprocess.CompletedProcess(argv, 0, b"owned\n")

        def inspected(_name):
            running = state["reads"] == 0 and mode not in ("missing-handshake",)
            return {"State": {"Running": running, "Status": "running" if running else "exited", "ExitCode": 0, "OOMKilled": False}}

        def fixtures(path, count):
            self.assertEqual(count, 1000)
            path.mkdir()
            for index in (0, 500, 999):
                item = controller.fixture_path(path, index)
                item.parent.mkdir(exist_ok=True)
                item.write_bytes(controller.FIXTURE)
            return controller.fixture_sample(path, count)

        validator_calls = []
        def validate(report, inspected_value, budget, expected_files):
            validator_calls.append(expected_files)
            if mode == "validator-failed":
                raise ValueError("PRIVATE validator detail")
            self.assertEqual(report, final)
            return {"result": "passed", "fixtureFiles": expected_files, "sampleCount": 10}

        fake_validator = types.SimpleNamespace(validate_scan_memory=validate)
        real_resolve = Path.resolve
        def resolve(path, *args, **kwargs):
            if path == Path("/var/tmp"):
                return native.parent
            return real_resolve(path, *args, **kwargs)

        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.dict(sys.modules, {"scan_memory_acceptance": fake_validator}))
            stack.enter_context(patch.object(controller, "ROOT", self.root))
            stack.enter_context(patch.object(controller.sys, "platform", "linux"))
            stack.enter_context(patch.object(controller, "capture", side_effect=capture))
            stack.enter_context(patch.object(controller, "inspect_owned", side_effect=inspected))
            stack.enter_context(patch.object(controller, "container_state", return_value={"status": "exited", "exitCode": 0, "oomKilled": False, "running": False}))
            stack.enter_context(patch.object(controller, "source_digest", return_value="b" * 64))
            stack.enter_context(patch.object(controller, "load_budget", return_value=({}, "c" * 64)))
            stack.enter_context(patch.object(controller, "check_fixture_storage", return_value={"nativeDisk": True}))
            stack.enter_context(patch.object(controller, "check_postgres_storage", return_value={"nativeDisk": True}))
            stack.enter_context(patch.object(controller, "create_fixtures", side_effect=fixtures))
            stack.enter_context(patch.object(controller.tempfile, "mkdtemp", return_value=str(native)))
            stack.enter_context(patch.object(Path, "resolve", resolve))
            stack.enter_context(patch.object(controller.time, "sleep"))
            stack.enter_context(patch.dict(os.environ, {"JELEE_TEST_DATABASE_URL": dsn, "JELEE_SCAN_MEMORY_PG_CONTAINER": "existing-db"}))
            result = controller.run_case(smoke=True)
        self.assertFalse(native.exists())
        self.assertNotIn("PRIVATE", json.dumps(result))
        summary = next((self.root / ".testdata").glob("scan-memory-*/summary.json"))
        self.assertEqual(json.loads(summary.read_text(encoding="utf-8")), result)
        self.assertTrue(any("--cleanup-probe-worker" in call for call in calls))
        self.assertFalse(any(call[:3] == ["docker", "container", "rm"] and "existing-db" in call for call in calls))
        if mode != "cleanup-unverifiable":
            self.assertTrue(any(call[:3] == ["docker", "container", "rm"] for call in calls))
            self.assertTrue(any(call[:3] == ["docker", "image", "rm"] for call in calls))
        return result, calls, validator_calls

    def test_smoke_success_uses_fixed_limits_and_never_claims_final_scale(self):
        result, calls, validators = self.exercise_case("success")
        self.assertEqual(result["result"], "passed")
        self.assertEqual(result["scope"], "smoke-1000")
        self.assertFalse(result["finalAcceptance"])
        self.assertTrue(result["testArtifactsCleaned"])
        self.assertEqual(validators, [1000])
        run = next(call for call in calls if call[:3] == ["docker", "run", "-d"])
        for option, value in (("--user", "65532:65532"), ("--memory", "768m"), ("--memory-swap", "768m"),
                              ("--cpus", "2"), ("--pids-limit", "128"), ("--cap-drop", "ALL"),
                              ("--security-opt", "no-new-privileges")):
            self.assertEqual(run[run.index(option) + 1], value)
        self.assertIn("--read-only", run)
        self.assertIn("GOGC=100", run)
        self.assertIn("GOMEMLIMIT=512MiB", run)
        self.assertTrue(run[run.index("--volume") + 1].endswith(":/media:ro"))
        self.assertEqual(sum(call[:4] == ["docker", "kill", "--signal", "SIGTERM"] for call in calls), 1)

    def test_failure_paths_preserve_fixed_state_and_still_clean(self):
        for mode in ("missing-handshake", "duplicate-event", "bad-json", "validator-failed", "cleanup-failed", "cleanup-unverifiable"):
            with self.subTest(mode=mode):
                # Each exercise gets its own isolated evidence root.
                previous = self.root
                self.root = previous / mode
                self.root.mkdir()
                try:
                    result, _calls, validators = self.exercise_case(mode)
                    self.assertEqual(result["result"], "failed")
                    self.assertFalse(result["finalAcceptance"])
                    self.assertEqual(result["testArtifactsCleaned"], mode not in ("cleanup-failed", "cleanup-unverifiable"))
                    if mode not in ("validator-failed", "cleanup-failed", "cleanup-unverifiable"):
                        self.assertEqual(validators, [])
                finally:
                    self.root = previous

    def test_cli_only_accepts_explicit_smoke_and_returns_failure(self):
        with patch.object(controller, "run_case", return_value={"scope": "smoke-1000", "result": "failed", "fixtureFiles": 1000,
                                                                "finalAcceptance": False, "testArtifactsCleaned": True}) as run, \
                patch.object(controller.signal, "signal"), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(controller.main(["--smoke"]), 1)
            run.assert_called_once_with(smoke=True)
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            controller.main(["--files", "1"])


if __name__ == "__main__":
    unittest.main()
