"""Exercise live artifact/ACK failures without a Docker daemon."""
import copy
import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import runtime_heap_controller as controller
import test_nfo_worker as worker


def fixture(stage, data):
    return {"version": 1, "stage": stage, "bytes": len(data),
            "sha256": hashlib.sha256(data).hexdigest(), "memProfileRate": 524288,
            "numGC": 2 if stage == "before" else 20,
            "elapsedNanos": 1 if stage == "before" else 300000000001}


def transcript(records):
    return "\n".join(json.dumps({"heapProfileReady": value}) for value in records)


class HeapControllerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="heap-controller-", dir=Path.cwd())
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.control = self.root / "control"
        self.control.mkdir()
        self.data = gzip.compress(b"bounded synthetic gzip fixture")
        self.records = [fixture(stage, self.data) for stage in ("before", "after")]
        self.record = {}
        self.capture = controller.HeapCapture(self.root / "private", self.record)

    def copy_file(self, container, stage, destination):
        self.assertEqual(container, "owned")
        self.assertEqual(destination.name, stage + ".heap.pb.gz")
        destination.write_bytes(self.data)
        return {"stdoutBytes": len(self.data), "stderrBytes": 0, "exitCode": 0}

    def test_export_is_bounded_binary_exec_without_tty_or_shell(self):
        destination = self.capture.directory / "before.heap.pb.gz"
        with patch.object(controller, "_run_bounded", return_value={"exitCode": 0}) as run:
            controller._export_profile("owned", "before", destination)
            argv, cwd, env, path, limit = run.call_args.args
            self.assertEqual(argv, ["docker", "exec", "owned", "/worker.test", "--export-heap-profile", "before"])
            self.assertEqual(cwd, destination.parent)
            self.assertEqual(path, destination)
            self.assertEqual(limit, 8 * 1024 * 1024)
            self.assertEqual(run.call_args.kwargs, {"timeout": 20})
            self.assertEqual(env, os.environ.copy())

    def test_cumulative_logs_copy_once_and_ack_exact_hash(self):
        with patch.object(controller, "_export_profile", side_effect=self.copy_file) as run:
            self.capture.consume(transcript(self.records[:1]), "owned", self.control)
            self.capture.consume(transcript(self.records[:1]), "owned", self.control)
            self.capture.consume(transcript(self.records), "owned", self.control)
            self.capture.consume(transcript(self.records), "owned", self.control)
            self.assertEqual(run.call_count, 2)
        for record in self.records:
            self.assertEqual((self.control / ("heap-" + record["stage"] + "-copied")).read_bytes(),
                             record["sha256"].encode())
        self.assertEqual(self.capture.copied, self.records)
        self.assertEqual(self.record["result"], "failed")  # Analysis still required.
        self.assertTrue(all(c["acknowledged"] for c in self.record["captures"]))

    def test_duplicate_changed_reversed_and_missing_events_fail(self):
        with patch.object(controller, "_export_profile", side_effect=self.copy_file):
            self.capture.consume(transcript(self.records[:1]), "owned", self.control)
        changed = copy.deepcopy(self.records)
        changed[0]["sha256"] = "a" * 64
        values = [self.records[:1] * 2, changed, list(reversed(self.records)), [], self.records * 2]
        for records in values:
            with self.subTest(records=records), patch.object(controller, "_export_profile") as run:
                with self.assertRaisesRegex(controller.HeapCaptureError, "^heap_profile_capture_failed$"):
                    self.capture.consume(transcript(records), "owned", self.control)
                run.assert_not_called()

    def test_malformed_ready_json_never_copies(self):
        values = ['{"heapProfileReady":NaN}', '{"heapProfileReady":{},"heapProfileReady":{}}',
                  '{"heapProfileReady":', '{"heapProfileReady":{},"path":"PRIVATE"}']
        for value in values:
            with self.subTest(value=value), patch.object(controller, "_export_profile") as run:
                with self.assertRaisesRegex(controller.HeapCaptureError, "^heap_profile_capture_failed$"):
                    self.capture.consume(value, "owned", self.control)
                run.assert_not_called()

    def test_bad_copy_never_acknowledges_and_retains_previous_profile(self):
        with patch.object(controller, "_export_profile", side_effect=self.copy_file):
            self.capture.consume(transcript(self.records[:1]), "owned", self.control)
        def corrupt(_container, _stage, destination):
            destination.write_bytes(b"PRIVATE damaged")
        with patch.object(controller, "_export_profile", side_effect=corrupt):
            with self.assertRaisesRegex(controller.HeapCaptureError, "^heap_profile_capture_failed$"):
                self.capture.consume(transcript(self.records), "owned", self.control)
        self.assertFalse((self.control / "heap-after-copied").exists())
        self.assertEqual((self.capture.directory / "before.heap.pb.gz").read_bytes(), self.data)
        self.assertEqual([c["result"] for c in self.record["captures"]], ["passed", "failed"])

    def test_copy_timeout_is_safe_and_unacknowledged(self):
        with patch.object(controller, "_export_profile", side_effect=subprocess.TimeoutExpired("PRIVATE command", 20)):
            with self.assertRaisesRegex(controller.HeapCaptureError, "^heap_profile_capture_failed$"):
                self.capture.consume(transcript(self.records[:1]), "owned", self.control)
        self.assertEqual(list(self.control.iterdir()), [])
        self.assertNotIn("PRIVATE", json.dumps(self.record))

    def test_existing_evidence_directory_is_never_overwritten(self):
        with self.assertRaises(controller.HeapCaptureError):
            controller.HeapCapture(self.capture.directory, {})

    def test_worker_keeps_before_profile_and_cleans_up_after_failed_copy(self):
        fixtures = self.root / "fixtures"
        fixtures.mkdir()
        (fixtures / "video-180p.mp4").write_bytes(b"synthetic media fixture")
        (self.root / "tools").mkdir()
        (self.root / "tools/manifest.json").write_text(json.dumps({"mediaTools": {"platforms": {"linux-amd64": {}}}}))
        calls = []
        def run(argv, **_kwargs):
            calls.append(argv)
            if "-o" in argv and "./internal/platform/runtime" in argv:
                Path(argv[argv.index("-o") + 1]).write_bytes(b"fake test binary")
            if argv[:2] == ["docker", "logs"]:
                output = transcript(self.records).encode()
            elif argv[:2] == ["docker", "inspect"]:
                output = b'{"Running":true,"Status":"running","ExitCode":0,"OOMKilled":false}'
            else:
                output = b"owned\n"
            return subprocess.CompletedProcess(argv, 0, output)
        def export(_container, stage, destination):
            if stage == "after":
                raise subprocess.TimeoutExpired("PRIVATE", 20)
            destination.write_bytes(self.data)
            return {"stdoutBytes": len(self.data), "stderrBytes": 0, "exitCode": 0}
        with patch.object(worker, "ROOT", self.root), patch.object(worker.sys, "platform", "linux"), \
                patch.object(worker, "WITH_MEMORY", True), patch.object(worker, "WITH_SUSTAINED", True), \
                patch.object(worker, "WITH_FAMILY", True), patch.object(worker, "WITH_IGNORE", True), \
                patch.object(worker, "source_digest", return_value="a" * 64), \
                patch.object(worker, "select_fixtures", return_value=(fixtures, {})), \
                patch.object(worker.subprocess, "run", side_effect=run), \
                patch.object(controller, "_export_profile", side_effect=export), \
                patch.object(worker, "container_state", return_value={"status": "running"}), \
                patch.dict(os.environ, {"JELEE_TEST_DATABASE_URL": "postgres://user:PRIVATE@localhost/jelee_test"}):
            with self.assertRaises(controller.HeapCaptureError):
                worker.run_case(10, resident_budget={}, budget_identity={})
        report_path = self.root / (".testdata/" + worker.EVIDENCE_PREFIX + "-case-10-gogc100.json")
        result = json.loads(report_path.read_text())
        self.assertEqual(result["result"], "failed")
        self.assertTrue(result["testArtifactsCleaned"])
        self.assertEqual([c["result"] for c in result["heapComparison"]["captures"]], ["passed", "failed"])
        evidence = self.root / (".testdata/" + worker.EVIDENCE_PREFIX + "-heap-gogc100")
        self.assertEqual((evidence / "before.heap.pb.gz").read_bytes(), self.data)
        self.assertTrue(any("--cleanup-probe-worker" in call for call in calls))
        self.assertTrue(any(call[:3] == ["docker", "container", "rm"] for call in calls))
        self.assertTrue(any(call[:3] == ["docker", "image", "rm"] for call in calls))
        self.assertFalse([path for path in (self.root / ".testdata").glob("nfo-worker-*")
                          if path.is_dir() and path != evidence])
        self.assertNotIn("PRIVATE", json.dumps(result))

    def test_finish_requires_final_pair_and_successful_analysis(self):
        with patch.object(controller, "_export_profile", side_effect=self.copy_file):
            self.capture.consume(transcript(self.records), "owned", self.control)
        leaf = self.root / "pprof"
        leaf.write_bytes(b"fake leaf; never executed")
        with patch.object(controller.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, str(leaf).encode())), \
                patch.object(controller, "analyze_heap_profiles", return_value={"result": "passed", "netDeltaBytes": -4}) as analyze:
            with self.assertRaises(controller.HeapCaptureError):
                self.capture.finish({"heapProfiles": self.records[:1]}, "go")
            analyze.assert_not_called()
            self.capture.finish({"heapProfiles": self.records}, "go")
            self.assertEqual(self.record["result"], "passed")
            self.assertEqual(self.record["comparison"]["netDeltaBytes"], -4)
        self.record["result"] = "failed"
        with patch.object(controller.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, str(leaf).encode())), \
                patch.object(controller, "analyze_heap_profiles", side_effect=ValueError("PRIVATE")):
            with self.assertRaisesRegex(controller.HeapCaptureError, "^heap_profile_capture_failed$"):
                self.capture.finish({"heapProfiles": self.records}, "go")
        self.assertEqual(self.record["result"], "failed")
        self.assertTrue((self.capture.directory / "before.heap.pb.gz").exists())


if __name__ == "__main__":
    unittest.main()
