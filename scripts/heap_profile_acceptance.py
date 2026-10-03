"""Bounded, private heap-profile validation using the pinned SDK pprof leaf.

Callers own container copying, acknowledgements and the private evidence folder.
This module never downloads symbols, runs a shell, or publishes raw pprof text.
"""
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import threading
import time
import zlib
from decimal import Decimal, InvalidOperation

INVALID_EVIDENCE = "heap_profile_evidence_invalid"
MAX_PROFILE_BYTES = 8 * 1024 * 1024
MAX_EXPANDED_BYTES = 64 * 1024 * 1024
MAX_RAW_BYTES = 16 * 1024 * 1024
MAX_TOP_BYTES = 64 * 1024
_MAX_UINT64 = (1 << 64) - 1
_MAX_INT64 = (1 << 63) - 1
_STAGES = ("before", "after")


class Rejected(ValueError):
    """A fixed code, never an input value, filename, or child error."""


def _need(condition):
    if not condition:
        raise Rejected(INVALID_EVIDENCE)


def _integer(value, minimum=0, maximum=_MAX_UINT64):
    _need(type(value) is int and minimum <= value <= maximum)
    return value


def validate_heap_metadata(record, expected_stage=None):
    _need(type(record) is dict)
    _need(type(record.get("version")) is int and record["version"] == 1)
    stage = record.get("stage")
    _need(type(stage) is str and stage in _STAGES)
    if expected_stage is not None:
        _need(type(expected_stage) is str and expected_stage in _STAGES and stage == expected_stage)
    digest = record.get("sha256")
    _need(type(digest) is str and re.fullmatch(r"[0-9a-f]{64}", digest) is not None)
    return {"version": 1, "stage": stage,
            "bytes": _integer(record.get("bytes"), 1, MAX_PROFILE_BYTES), "sha256": digest,
            "memProfileRate": _integer(record.get("memProfileRate"), 1, _MAX_INT64),
            "numGC": _integer(record.get("numGC")),
            "elapsedNanos": _integer(record.get("elapsedNanos"), 0, _MAX_INT64)}


def validate_heap_profiles(records):
    _need(type(records) is list and len(records) == 2)
    before, after = [validate_heap_metadata(record, stage) for record, stage in zip(records, _STAGES)]
    _need(before["memProfileRate"] == after["memProfileRate"])
    _need(before["numGC"] <= after["numGC"] and before["elapsedNanos"] < after["elapsedNanos"])
    return [before, after]


def _regular_bytes(path, limit):
    """Read only a bounded regular file, rejecting symlinks and open races."""
    descriptor = None
    try:
        path = Path(path)
        before = path.lstat()
        _need(stat.S_ISREG(before.st_mode) and 0 < before.st_size <= limit)
        flags = os.O_RDONLY | getattr(os, "O_BINARY", 0) | getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(path, flags)
        opened = os.fstat(descriptor)
        _need(stat.S_ISREG(opened.st_mode) and (opened.st_dev, opened.st_ino) == (before.st_dev, before.st_ino))
        with os.fdopen(descriptor, "rb") as stream:
            descriptor = None
            body = stream.read(limit + 1)
        _need(len(body) == before.st_size and len(body) <= limit)
        return body
    except (OSError, TypeError, ValueError):
        raise Rejected(INVALID_EVIDENCE) from None
    finally:
        if descriptor is not None:
            os.close(descriptor)


def validate_heap_file(path, metadata):
    record = validate_heap_metadata(metadata)
    body = _regular_bytes(path, MAX_PROFILE_BYTES)
    _need(len(body) == record["bytes"] and hashlib.sha256(body).hexdigest() == record["sha256"])
    expanded = 0
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(body), mode="rb") as stream:
            while True:
                block = stream.read(min(65536, MAX_EXPANDED_BYTES - expanded + 1))
                if not block:
                    break
                expanded += len(block)
                _need(expanded <= MAX_EXPANDED_BYTES)
    except (OSError, EOFError, ValueError, zlib.error):
        raise Rejected(INVALID_EVIDENCE) from None
    _need(expanded > 0)
    return {**record, "fileValidated": True, "expandedBytes": expanded}


def _private_text(body, limit, forbidden_prefixes, secret_markers):
    _need(type(body) is bytes and 0 < len(body) <= limit)
    try:
        text = body.decode("utf-8", errors="strict")
    except UnicodeError:
        raise Rejected(INVALID_EVIDENCE) from None
    _need(all(c in "\n\r\t" or ord(c) >= 32 for c in text))
    normalized = text.replace("\\", "/").casefold()
    defaults = ("c:/users/", "/mnt/c/users/", "/home/", "/users/", "postgres://", "postgresql://")
    for values in (forbidden_prefixes, secret_markers):
        _need(type(values) in (list, tuple) and len(values) <= 128)
        _need(all(type(value) is str and 0 < len(value) <= 4096 for value in values))
    for value in (*defaults, *forbidden_prefixes, *secret_markers):
        _need(value.replace("\\", "/").casefold() not in normalized)
    return text


def validate_heap_raw(body, metadata, forbidden_prefixes=(), secret_markers=()):
    """Check the SDK's raw representation privately and sum inuse_space bytes."""
    record = validate_heap_metadata(metadata)
    text = _private_text(body, MAX_RAW_BYTES, forbidden_prefixes, secret_markers)
    lines = text.splitlines()
    _need(lines.count("Samples:") == lines.count("Locations") == lines.count("Mappings") == 1)
    sample_start, sample_end = lines.index("Samples:"), lines.index("Locations")
    _need(sample_start + 1 < sample_end < lines.index("Mappings"))
    periods = [line.removeprefix("Period: ") for line in lines if line.startswith("Period: ")]
    _need(periods == [str(record["memProfileRate"])])
    _need(lines.count("PeriodType: space bytes") == 1)
    types = [value.removesuffix("[dflt]") for value in lines[sample_start + 1].split()]
    _need(types == ["alloc_objects/count", "alloc_space/bytes", "inuse_objects/count", "inuse_space/bytes"])
    count, total = 0, 0
    for line in lines[sample_start + 2:sample_end]:
        match = re.fullmatch(r"\s*((?:-?\d+\s+){3}-?\d+):\s*(?:\d+\s*)*", line)
        if match is None:
            # SDK raw may include allocation-size labels following a sample.
            _need(count > 0 and line.startswith("                ") and ":[" in line and line.endswith("]"))
            continue
        tokens = match.group(1).split()
        _need(all(len(value) <= 20 for value in tokens))
        values = [int(value) for value in tokens]
        _need(all(0 <= value <= _MAX_INT64 for value in values))
        total += values[3]
        _need(total <= _MAX_INT64)
        count += 1
    return {"stage": record["stage"], "privacyChecked": True, "sampleType": "inuse_space",
            "unit": "bytes", "sampleCount": count, "sampledInuseBytes": total}


def _bytes_token(value):
    _need(len(value) <= 32)
    if value == "0":
        return 0
    _need(re.fullmatch(r"-?\d+(?:\.\d{1,2})?B", value) is not None)
    number = Decimal(value[:-1])
    _need(number == number.to_integral_value() and abs(number) <= _MAX_INT64)
    return int(number)


def _percentage(value):
    _need(value.endswith("%") and len(value) <= 32)
    try:
        number = Decimal(value[:-1])
    except InvalidOperation:
        raise Rejected(INVALID_EVIDENCE) from None
    _need(number.is_finite())


def summarize_heap_top(body, forbidden_prefixes=(), secret_markers=()):
    text = _private_text(body, MAX_TOP_BYTES, forbidden_prefixes, secret_markers)
    lines = text.splitlines()
    _need(lines.count("Type: inuse_space") == 1)
    headers = [index for index, line in enumerate(lines) if line.split() == ["flat", "flat%", "sum%", "cum", "cum%"]]
    _need(len(headers) == 1)
    totals = [re.fullmatch(r"Showing nodes accounting for (\S+), (\S+) of (\S+) total", line) for line in lines]
    totals = [match for match in totals if match is not None]
    _need(len(totals) == 1)
    _bytes_token(totals[0].group(1))
    _percentage(totals[0].group(2))
    magnitude = _bytes_token(totals[0].group(3))
    _need(magnitude >= 0)
    functions = []
    for line in lines[headers[0] + 1:]:
        if not line.strip():
            continue
        fields = line.split(maxsplit=5)
        _need(len(fields) == 6 and len(functions) < 20)
        flat, cumulative = _bytes_token(fields[0]), _bytes_token(fields[3])
        for index in (1, 2, 4):
            _percentage(fields[index])
        name = fields[5]
        inline = name.endswith(" (inline)")
        if inline:
            name = name.removesuffix(" (inline)")
        _need(0 < len(name) <= 1024 and all(32 <= ord(c) < 127 for c in name))
        _need(not name.startswith(("/", "\\")) and "://" not in name and re.search(r"[A-Za-z]:[\\/]", name) is None)
        functions.append({"function": name, "flatBytes": flat, "cumulativeBytes": cumulative, "inline": inline})
    # pprof's diff report total is the sum of absolute sample values, not its
    # signed net change. The latter is computed from the two raw profiles.
    return {"sampleType": "inuse_space", "unit": "bytes", "totalMagnitudeBytes": magnitude, "functions": functions}


def _run_bounded(argv, cwd, env, output_path, max_stdout, timeout=30):
    """Drain both child pipes with hard byte limits, then kill/wait/join."""
    process, readers = None, []
    failed = threading.Event()
    counts = [0, 0]
    try:
        _need(0 < timeout <= 30 and 0 < max_stdout <= MAX_RAW_BYTES)
        descriptor = os.open(output_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "wb") as output:
            process = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)

            def drain(stream, index, limit, sink=None):
                try:
                    while True:
                        data = stream.read(65536)
                        if not data:
                            break
                        counts[index] += len(data)
                        if counts[index] > limit:
                            failed.set()
                            break
                        if sink is not None:
                            sink.write(data)
                except (OSError, ValueError):
                    failed.set()
                finally:
                    stream.close()

            readers = [threading.Thread(target=drain, args=(process.stdout, 0, max_stdout, output)),
                       threading.Thread(target=drain, args=(process.stderr, 1, MAX_TOP_BYTES))]
            for reader in readers:
                reader.start()
            deadline = time.monotonic() + timeout
            while process.poll() is None and not failed.is_set() and time.monotonic() < deadline:
                try:
                    process.wait(timeout=min(0.05, max(0.001, deadline - time.monotonic())))
                except subprocess.TimeoutExpired:
                    pass
            if process.poll() is None:
                failed.set()
                process.kill()
            process.wait()
            for reader in readers:
                reader.join()
            _need(not failed.is_set() and process.returncode == 0)
        return {"stdoutBytes": counts[0], "stderrBytes": counts[1], "exitCode": 0}
    except (OSError, ValueError, subprocess.SubprocessError):
        raise Rejected(INVALID_EVIDENCE) from None
    finally:
        if process is not None:
            if process.poll() is None:
                process.kill()
            process.wait()
        for reader in readers:
            if reader.ident is not None:
                reader.join()


def _write_state(directory, record):
    pending = directory / "heap-analysis-state.pending"
    with os.fdopen(os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8") as output:
        json.dump(record, output, sort_keys=True)
        output.write("\n")
    pending.replace(directory / "heap-analysis-state.json")


def analyze_heap_profiles(pprof_tool, before_path, after_path, metadata, private_dir,
                          forbidden_prefixes=(), secret_markers=()):
    """Run the already resolved pinned SDK leaf on two fixed local files.

    The private directory must be owned by this acceptance run. Raw and top
    outputs stay there, including partial output on failure. Only the returned
    whitelist summary and heap-analysis-state.json are suitable for publishing.
    """
    state = {"version": 1, "result": "failed", "stage": "validation", "steps": []}
    directory = None
    state_started = False
    try:
        directory = Path(private_dir)
        _need(directory.is_absolute() and not directory.is_symlink() and directory.is_dir())
        directory = directory.resolve()
        _need(not (directory / "heap-analysis-state.json").exists())
        _write_state(directory, state)
        state_started = True
        records = validate_heap_profiles(metadata)
        paths = [Path(before_path), Path(after_path)]
        tool = Path(pprof_tool)
        _need(tool.is_absolute() and tool.is_file() and tool.name in ("pprof", "pprof.exe"))
        files = []
        for path, record in zip(paths, records):
            _need(path.name == record["stage"] + ".heap.pb.gz" and path.parent.resolve() == directory)
            files.append(validate_heap_file(path, record))
        work = directory / "pprof-work"
        work.mkdir(mode=0o700)
        env = dict(os.environ, PPROF_TMPDIR=str(work), PPROF_BINARY_PATH=str(work), PPROF_TOOLS=str(work))
        raw_summaries, tops = [], {}
        jobs = [("before-raw", ["-raw", "-symbolize=none", paths[0].name], MAX_RAW_BYTES),
                ("after-raw", ["-raw", "-symbolize=none", paths[1].name], MAX_RAW_BYTES),
                ("before-top", ["-top", "-inuse_space", "-unit=bytes", "-nodecount=20", "-symbolize=none", paths[0].name], MAX_TOP_BYTES),
                ("after-top", ["-top", "-inuse_space", "-unit=bytes", "-nodecount=20", "-symbolize=none", paths[1].name], MAX_TOP_BYTES),
                ("diff-top", ["-top", "-inuse_space", "-unit=bytes", "-nodecount=20", "-symbolize=none", "-base", paths[0].name, paths[1].name], MAX_TOP_BYTES)]
        for name, arguments, limit in jobs:
            state["stage"] = name
            step = {"name": name, "status": "running"}
            state["steps"].append(step)
            _write_state(directory, state)
            output = directory / (name + ".private.txt")
            outcome = _run_bounded([str(tool), *arguments], directory, env, output, limit)
            body = _regular_bytes(output, limit)
            if name.endswith("-raw"):
                raw_summaries.append(validate_heap_raw(body, records[len(raw_summaries)], forbidden_prefixes, secret_markers))
            else:
                tops[name.removesuffix("-top")] = summarize_heap_top(body, forbidden_prefixes, secret_markers)
            step.update(status="passed", **outcome)
            _write_state(directory, state)
        # Detect files changed during analysis; do not publish mixed artifacts.
        for path, record in zip(paths, records):
            validate_heap_file(path, record)
        result = {"version": 1, "result": "passed", "sampleType": "inuse_space", "unit": "bytes",
                  "sampled": True, "privacyChecked": True, "profiles": files,
                  "before": {**raw_summaries[0], **tops["before"]},
                  "after": {**raw_summaries[1], **tops["after"]},
                  "diff": {**tops["diff"], "netDeltaBytes": raw_summaries[1]["sampledInuseBytes"] - raw_summaries[0]["sampledInuseBytes"]}}
        state.update(result="passed", stage="complete")
        _write_state(directory, state)
        return result
    except (OSError, ValueError, TypeError, subprocess.SubprocessError):
        state["result"] = "failed"
        state["failureCode"] = INVALID_EVIDENCE
        if state["steps"] and state["steps"][-1]["status"] == "running":
            state["steps"][-1]["status"] = "failed"
        if state_started and directory is not None and directory.is_dir():
            try:
                _write_state(directory, state)
            except OSError:
                pass
        raise Rejected(INVALID_EVIDENCE) from None
