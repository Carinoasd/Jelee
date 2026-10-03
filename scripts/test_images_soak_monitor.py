"""Receipt failures and a real pipe follower, without a long workload."""

import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import images_soak_monitor as monitor


EVENT = b'{"imagesSoakEvent":{"kind":"samples"}}\n'
READY = b'{"imagesSoakReadyForSIGTERM":true}\n'
FINAL = b'{"imagesSoakAcceptance":{}}\n'


class ReceiptTests(unittest.TestCase):
    def receipt(self):
        return monitor.Receipt(io.BytesIO(), 0)

    def test_chunks_have_no_cumulative_log_and_keep_receipt_gap(self):
        item = self.receipt()
        item.feed(EVENT[:8], 1)
        item.feed(EVENT[8:], 60)
        item.feed(READY + FINAL + b"PASS\n", 61)
        result = item.finish(62)
        self.assertEqual(result["maxReceiptGapSeconds"], 60)
        self.assertEqual(item.pending, b"")
        self.assertEqual(result["rawBytes"], len(EVENT + READY + FINAL + b"PASS\n"))

    def test_partial_or_arbitrary_lines_do_not_refresh_heartbeat(self):
        for body in (b"waiting\n", EVENT[:5]):
            with self.subTest(body=body):
                item = self.receipt()
                item.feed(body, 69)
                with self.assertRaisesRegex(monitor.MonitorFailure, "heartbeat_timeout"):
                    item.check_time(71)

    def test_failure_matrix(self):
        for body in (READY + READY, FINAL, READY + FINAL + EVENT,
                     b'{"a":1,"a":2}\n', b'{"x":NaN}\n', b"PASS\n",
                     b'{"imagesSoakReadyForSIGTERM":1}\n', b"[1]\n"):
            with self.subTest(body=body):
                item = self.receipt()
                with self.assertRaises(monitor.MonitorFailure):
                    item.feed(body, 1)
                    item.finish(2)

    def test_bounds_and_incomplete(self):
        item = self.receipt()
        item.feed(b"x" * 65536, 1)
        with self.assertRaisesRegex(monitor.MonitorFailure, "line_limit"):
            item.feed(b"x", 2)
        item = self.receipt()
        item.total = monitor.BUDGET["maxRawBytes"]
        with self.assertRaisesRegex(monitor.MonitorFailure, "raw_limit"):
            item.feed(EVENT, 1)
        item = self.receipt()
        item.feed(EVENT + READY + FINAL + b"PASS\nx", 1)
        with self.assertRaisesRegex(monitor.MonitorFailure, "incomplete"):
            item.finish(2)

    def test_atomic_status_small_and_replaced(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "status.json"
            monitor.atomic_status(path, {"stage": "build"})
            monitor.atomic_status(path, {"stage": "workload"})
            self.assertEqual(json.loads(path.read_bytes()), {"stage": "workload"})
            self.assertFalse(Path(str(path) + ".pending").exists())
            with self.assertRaises(monitor.MonitorFailure):
                monitor.atomic_status(path, {"stage": "x" * 4096})


@unittest.skipUnless(sys.platform == "linux", "Linux controller uses pollable pipes")
class FollowerTests(unittest.TestCase):
    def test_silent_live_follower_times_out_and_is_reaped(self):
        import time
        with tempfile.TemporaryDirectory() as directory:
            raw = Path(directory) / "raw.jsonl"
            processes = []
            popen = monitor.subprocess.Popen

            def start(*args, **kwargs):
                value = popen(*args, **kwargs)
                processes.append(value)
                return value

            started = time.monotonic()
            with patch.object(monitor, "BUDGET", dict(monitor.BUDGET, heartbeatSeconds=0.05)), \
                    patch.object(monitor.subprocess, "Popen", side_effect=start):
                with self.assertRaisesRegex(monitor.MonitorFailure, "heartbeat_timeout"):
                    monitor.follow([sys.executable, "-c", "import time;time.sleep(60)"], raw,
                                   lambda: {"State": {"Running": True}}, lambda: None, lambda _: None)
            self.assertLess(time.monotonic() - started, 5)
            self.assertIsNotNone(processes[0].poll())

    def test_real_pipe_drain_signal_once_and_private_raw(self):
        with tempfile.TemporaryDirectory() as directory:
            raw = Path(directory) / "raw.jsonl"
            gate = Path(directory) / "gate"
            code = ("import pathlib,sys,time\n"
                    "sys.stdout.buffer.write(" + repr(EVENT + READY) + ");sys.stdout.flush()\n"
                    "while not pathlib.Path(sys.argv[1]).exists():time.sleep(.01)\n"
                    "sys.stdout.buffer.write(" + repr(FINAL + b"PASS\n") + ");sys.stdout.flush()\n")
            signals, statuses = [], []

            def send():
                signals.append(True)
                gate.touch()

            def inspect():
                return {"State": {"Running": not gate.exists(), "ExitCode": 0, "OOMKilled": False}}

            result, _ = monitor.follow([sys.executable, "-c", code, str(gate)], raw, inspect, send, statuses.append)
            self.assertEqual(signals, [True])
            self.assertEqual(result["rawBytes"], raw.stat().st_size)
            self.assertEqual(raw.stat().st_mode & 0o777, 0o600)
            self.assertEqual(len(statuses), 1)

    def test_failed_follower_exit_rejected_and_raw_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            raw = Path(directory) / "raw.jsonl"
            code = "import sys;sys.stdout.buffer.write(" + repr(EVENT + READY + FINAL + b"PASS\n") + ");sys.exit(7)"
            with self.assertRaisesRegex(monitor.MonitorFailure, "exit_invalid"):
                monitor.follow([sys.executable, "-c", code], raw,
                               lambda: {"State": {"Running": False, "ExitCode": 0, "OOMKilled": False}},
                               lambda: None, lambda _: None)
            self.assertTrue(raw.read_bytes().endswith(b"PASS\n"))


if __name__ == "__main__":
    unittest.main()
