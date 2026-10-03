"""Controller lifecycle failure injection; does not represent a soak workload."""
import contextlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import run_images_soak as controller
import test_image_memory as images
from test_image_memory_controller import fixture_manifest


class ControllerTests(unittest.TestCase):
    def exercise(self, mode="success", smoke=True):
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            root = Path(directory)
            native = root / "native"
            native.mkdir()
            calls, removed = [], []
            with patch.object(images, "MAX_SOURCE_BYTES", 1024):
                manifest, _ = fixture_manifest()
            # Validation of fixture metadata is covered by its own real contracts.
            def capture(argv, **_kwargs):
                calls.append(argv)
                body = b""
                if argv[-1] == "version":
                    body = b"go version go1.27.1 linux/amd64\n"
                elif "-o" in argv:
                    Path(argv[argv.index("-o") + 1]).write_bytes(b"binary")
                elif argv[:3] == ["docker", "image", "inspect"]:
                    body = b"sha256:" + b"1" * 64 + b"\n"
                elif "--count" in argv:
                    self.assertEqual(argv[-1], "1000")
                    body = json.dumps(manifest).encode()
                if "--cleanup-probe-worker" in argv and mode == "cleanup":
                    return subprocess.CompletedProcess(argv, 1, b"")
                return subprocess.CompletedProcess(argv, 0, body)

            def follow(argv, raw, inspect, send_term, status):
                self.assertEqual(argv[:3], ["docker", "logs", "--follow"])
                body = (native / "database.env").read_text()
                self.assertIn("JELEE_IMAGES_SOAK_MODE=" + ("smoke" if smoke else "formal"), body)
                self.assertIn("JELEE_IMAGES_SOAK_RUN_ID=", body)
                self.assertNotIn("JELEE_IMAGES_MEMORY_ACCEPTANCE", body)
                raw.write_bytes(b"private-test-log")
                if mode == "heartbeat":
                    raise controller.MonitorFailure("soak_heartbeat_timeout")
                if mode == "interrupt":
                    raise KeyboardInterrupt()
                send_term()
                status({"stage": "workload"})
                return {"rawBytes": 16}, inspect()

            def validate(stream, **kwargs):
                self.assertEqual(stream.read(), b"private-test-log")
                self.assertEqual(kwargs["fixture_bytes"], manifest["totalBytes"] + manifest["negativeBytes"])
                self.assertEqual(kwargs["scope"], "smoke" if smoke else "formal")
                if mode == "validation":
                    raise ValueError("PRIVATE")
                return {"container": None if mode == "missing-container" else {}, "streamValidated": True}

            patches = {"ROOT": root, "capture": capture, "follow": follow,
                       "load_soak_budget": lambda: ({}, "a" * 64),
                       "check_fixture_storage": lambda *_: {}, "check_postgres_storage": lambda *_: {},
                       "validate_manifest": lambda *_: manifest,
                       "validate_soak_log": validate,
                       "digest": lambda *_: "b" * 64,
                       "remove_owned": lambda kind, name: removed.append((kind, name)),
                       "remove_native": lambda path: shutil.rmtree(path),
                       "inspect_owned": lambda *_: {"State": {"Running": False, "ExitCode": 0, "OOMKilled": False}},
                       "container_state": lambda *_: {"running": mode in ("heartbeat", "interrupt")}}
            for name, value in patches.items():
                stack.enter_context(patch.object(controller, name, value))
            stack.enter_context(patch.object(controller.sys, "platform", "linux"))
            stack.enter_context(patch.object(Path, "cwd", return_value=root))
            stack.enter_context(patch.object(controller.tempfile, "mkdtemp", return_value=str(native)))
            stack.enter_context(patch.object(controller, "source_digest", side_effect=["a", "b" if mode == "source" else "a"]))
            stack.enter_context(patch.object(controller, "fixture_sample", side_effect=[[], [1] if mode == "fixture" else []]))
            stack.enter_context(patch.dict(os.environ, {"JELEE_TEST_DATABASE_URL": "postgres://user:PRIVATE@localhost/jelee_test"}))
            result = controller.run_case(smoke=smoke)
            self.assertFalse(native.exists())
            summary = next((root / ".testdata").glob("image-soak-*/summary.json"))
            self.assertEqual(json.loads(summary.read_bytes()), result)
            self.assertNotIn("PRIVATE", json.dumps(result))
            self.assertFalse(result["finalAcceptance"])
            self.assertEqual(result["result"], "passed" if mode == "success" else "failed")
            self.assertEqual(result["testArtifactsCleaned"], mode != "cleanup")
            self.assertEqual(len(removed), 4)
            workload = next(call for call in calls if call[:3] == ["docker", "run", "-d"])
            self.assertIn("^TestImagesSoakAcceptance$", workload)
            self.assertEqual(workload[-1], "25h")
            self.assertIn("sha256:" + "1" * 64, workload)
            self.assertIn("--read-only", workload)
            self.assertEqual(workload[workload.index("--memory") + 1], "768m")
            self.assertTrue(any("--cleanup-probe-worker" in call for call in calls))
            if mode in ("heartbeat", "interrupt"):
                self.assertTrue(any(call[:2] == ["docker", "stop"] for call in calls))

    def test_smoke_and_formal_preserve_fixed_workload(self):
        for smoke in (True, False):
            with self.subTest(smoke=smoke):
                self.exercise(smoke=smoke)

    def test_failures_preserve_raw_and_cleanup_owned_resources(self):
        for mode in ("heartbeat", "interrupt", "validation", "missing-container", "source", "fixture", "cleanup"):
            with self.subTest(mode=mode):
                self.exercise(mode)


if __name__ == "__main__":
    unittest.main()
