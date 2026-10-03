"""Live copy/ACK for the fixed, test-only heap profiles; raw files stay private."""
import json
import os
from pathlib import Path
import subprocess

from heap_profile_acceptance import (MAX_PROFILE_BYTES, _run_bounded, analyze_heap_profiles, validate_heap_file,
                                     validate_heap_metadata, validate_heap_profiles)


class HeapCaptureError(ValueError):
    def __init__(self):
        super().__init__("heap_profile_capture_failed")


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise HeapCaptureError()
        result[key] = value
    return result


def _reject_constant(_value):
    raise HeapCaptureError()


def _export_profile(container, stage, destination):
    if stage not in ("before", "after"):
        raise HeapCaptureError()
    # Docker's archive endpoint cannot read live tmpfs on all engines. Run the
    # tagged binary's fixed exporter inside that mount namespace instead.
    # No TTY, shell, or log wrapper may transform these binary bytes.
    return _run_bounded(["docker", "exec", container, "/worker.test", "--export-heap-profile", stage],
                        destination.parent, os.environ.copy(), destination, MAX_PROFILE_BYTES, timeout=20)


class HeapCapture:
    def __init__(self, directory, record):
        self.directory = Path(directory)
        self.record = record
        self.record.update(result="failed", captures=[])
        self.copied = []
        # A previous run's directory is never reused, including on failure.
        try:
            self.directory.mkdir(mode=0o700)
        except OSError:
            raise HeapCaptureError() from None

    def consume(self, transcript, container, control):
        """Docker logs are cumulative; accept an unchanged prefix only once."""
        events = []
        try:
            for line in transcript.splitlines():
                if not line.startswith("{"):
                    continue
                entry = json.loads(line, object_pairs_hook=_unique_object,
                                   parse_constant=_reject_constant)
                if "heapProfileReady" in entry:
                    if set(entry) != {"heapProfileReady"} or len(events) >= 2:
                        raise HeapCaptureError()
                    events.append(validate_heap_metadata(entry["heapProfileReady"],
                                                         ("before", "after")[len(events)]))
            if len(events) < len(self.copied) or events[:len(self.copied)] != self.copied:
                raise HeapCaptureError()
            if len(events) == 2:
                validate_heap_profiles(events)
            for metadata in events[len(self.copied):]:
                self._copy(metadata, container, Path(control))
        except (OSError, ValueError, TypeError, subprocess.SubprocessError):
            raise HeapCaptureError() from None

    def _copy(self, metadata, container, control):
        stage = metadata["stage"]
        destination = self.directory / (stage + ".heap.pb.gz")
        capture = {"metadata": metadata, "result": "failed", "phase": "export"}
        self.record["captures"].append(capture)
        if destination.exists() or destination.is_symlink():
            raise HeapCaptureError()
        # The worker closes a bounded file and waits for ACK before proceeding.
        # Export failure must propagate into the caller's owned-container cleanup
        # because killing the Docker CLI alone does not stop a remote exec.
        capture["transfer"] = _export_profile(container, stage, destination)
        capture["phase"] = "validation"
        validate_heap_file(destination, metadata)
        destination.chmod(0o600)
        capture["fileValidated"] = True
        capture["phase"] = "acknowledgement"
        ack = control / ("heap-" + stage + "-copied")
        pending = ack.with_suffix(".partial")
        if ack.exists() or ack.is_symlink():
            raise HeapCaptureError()
        # Exact SHA, without a newline; the worker never sees a partial write.
        with pending.open("xb") as stream:
            stream.write(metadata["sha256"].encode("ascii"))
        os.replace(pending, ack)
        capture.update(result="passed", phase="complete", acknowledged=True)
        self.copied.append(metadata)

    def finish(self, profile, go_tool, forbidden_prefixes=(), secret_markers=()):
        """Compare the final report to live events before invoking local pprof."""
        try:
            records = validate_heap_profiles(profile.get("heapProfiles"))
            if records != self.copied:
                raise HeapCaptureError()
            tool = subprocess.run([str(go_tool), "tool", "-n", "pprof"], check=True,
                                  stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=30)
            leaf = tool.stdout.decode("utf-8").strip()
            if not leaf or "\n" in leaf or not Path(leaf).is_absolute() or not Path(leaf).is_file():
                raise HeapCaptureError()
            self.record["comparison"] = analyze_heap_profiles(
                Path(leaf), self.directory / "before.heap.pb.gz", self.directory / "after.heap.pb.gz",
                records, self.directory, forbidden_prefixes=forbidden_prefixes,
                secret_markers=secret_markers)
            self.record["result"] = "passed"
        except (OSError, ValueError, TypeError, AttributeError, subprocess.SubprocessError):
            raise HeapCaptureError() from None
