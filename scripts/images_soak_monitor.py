"""Bounded Linux log follower; receipt timing is independent of worker clocks."""

import hashlib
import json
import os
from pathlib import Path
import selectors
import subprocess
import time

from images_soak_acceptance import BUDGET, pairs, bad_constant


class MonitorFailure(RuntimeError):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def atomic_status(path, value):
    body = (json.dumps(value, separators=(",", ":"), allow_nan=False) + "\n").encode()
    if len(body) > 4096:
        raise MonitorFailure("soak_status_limit")
    pending = Path(str(path) + ".pending")
    with os.fdopen(os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "wb") as stream:
        stream.write(body)
    os.replace(pending, path)


class Receipt:
    """Retain only one bounded line and counters, never the cumulative log."""

    def __init__(self, output, started):
        self.output = output
        self.started = self.last_heartbeat = self.last_read = started
        self.max_gap = 0.0
        self.pending = bytearray()
        self.total = self.lines = 0
        self.ready = self.final = self.passed = False
        self.checksum = hashlib.sha256()

    def check_time(self, now):
        if now < self.last_read or now - self.started > 25 * 3600:
            raise MonitorFailure("soak_controller_deadline")
        if now - self.last_heartbeat > BUDGET["heartbeatSeconds"]:
            raise MonitorFailure("soak_heartbeat_timeout")

    def feed(self, body, now):
        self.check_time(now)
        if not isinstance(body, bytes) or not body or len(body) > 65536:
            raise MonitorFailure("soak_read_invalid")
        if self.total + len(body) > BUDGET["maxRawBytes"]:
            raise MonitorFailure("soak_raw_limit")
        self.output.write(body)
        # DrvFS can select a large Python file buffer. Publish each received
        # chunk so a live observer (or a failed controller) retains raw evidence.
        self.output.flush()
        self.checksum.update(body)
        self.total += len(body)
        self.last_read = now
        self.pending.extend(body)
        while b"\n" in self.pending:
            index = self.pending.index(b"\n") + 1
            line = bytes(self.pending[:index])
            del self.pending[:index]
            self.line(line, now)
        limit = BUDGET["maxReportBytes"] if self.pending.startswith(b'{"imagesSoakAcceptance":') else BUDGET["maxEventBytes"]
        if len(self.pending) > limit:
            raise MonitorFailure("soak_line_limit")

    def line(self, line, now):
        final = line.startswith(b'{"imagesSoakAcceptance":')
        if len(line) > BUDGET["maxReportBytes" if final else "maxEventBytes"]:
            raise MonitorFailure("soak_line_limit")
        self.lines += 1
        if line.startswith(b"{"):
            try:
                value = json.loads(line, object_pairs_hook=pairs, parse_constant=bad_constant)
            except (ValueError, UnicodeError, RecursionError):
                raise MonitorFailure("soak_json_invalid") from None
            if type(value) is not dict:
                raise MonitorFailure("soak_json_invalid")
            if set(value) == {"imagesSoakEvent"} and not self.final:
                event = value["imagesSoakEvent"]
                if type(event) is not dict or event.get("kind") not in {"start", "samples", "hour", "round", "rotation", "workEnd"}:
                    raise MonitorFailure("soak_event_invalid")
                # Full semantic and clock replay is mandatory after exit.
                self.max_gap = max(self.max_gap, now - self.last_heartbeat)
                self.last_heartbeat = now
            elif set(value) == {"imagesSoakReadyForSIGTERM"} and value["imagesSoakReadyForSIGTERM"] is True and not self.ready and not self.final:
                self.ready = True
            elif set(value) == {"imagesSoakAcceptance"} and self.ready and not self.final:
                self.final = True
            else:
                raise MonitorFailure("soak_event_invalid")
        elif line == b"PASS\n":
            if not self.final or self.passed:
                raise MonitorFailure("soak_pass_invalid")
            self.passed = True

    def finish(self, now):
        self.check_time(now)
        if self.pending or not (self.ready and self.final and self.passed):
            raise MonitorFailure("soak_log_incomplete")
        self.output.flush()
        return {"rawBytes": self.total, "rawSha256": self.checksum.hexdigest(),
                "lines": self.lines, "maxReceiptGapSeconds": max(self.max_gap, now - self.last_heartbeat),
                "elapsedSeconds": now - self.started, "heartbeatLimitSeconds": BUDGET["heartbeatSeconds"]}


def follow(argv, raw_path, inspect, send_term, status):
    """One owned follower process; callers own the workload and its cleanup."""
    process = None
    try:
        with os.fdopen(os.open(raw_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as raw:
            process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT, start_new_session=True, bufsize=0)
            os.set_blocking(process.stdout.fileno(), False)
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                started = time.monotonic()
                receipt = Receipt(raw, started)
                next_inspect = next_status = started
                signalled = eof = False
                exited_at = None
                inspected = None
                while True:
                    now = time.monotonic()
                    receipt.check_time(now)
                    if now >= next_inspect:
                        inspected = inspect()
                        state = inspected["State"]
                        if state["Running"] is False and exited_at is None:
                            exited_at = time.monotonic()
                        next_inspect = time.monotonic() + 5
                    if now >= next_status:
                        status({"stage": "workload", "elapsedSeconds": int(now - started),
                                "rawBytes": receipt.total, "sigtermSent": signalled})
                        next_status = time.monotonic() + 300
                    for _key, _mask in selector.select(timeout=0.25):
                        body = os.read(process.stdout.fileno(), 65536)
                        if body:
                            receipt.feed(body, time.monotonic())
                        else:
                            eof = True
                            selector.unregister(process.stdout)
                    if receipt.ready and not signalled:
                        send_term()
                        signalled = True
                    if eof:
                        returncode = process.wait(timeout=5)
                        inspected = inspect()
                        state = inspected["State"]
                        if returncode != 0 or not signalled or state["Running"] is not False or state["ExitCode"] != 0 or state["OOMKilled"] is not False:
                            raise MonitorFailure("soak_exit_invalid")
                        return receipt.finish(time.monotonic()), inspected
                    if exited_at is not None and time.monotonic() - exited_at > 10:
                        raise MonitorFailure("soak_log_drain_timeout")
    finally:
        if process is not None:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            process.stdout.close()
