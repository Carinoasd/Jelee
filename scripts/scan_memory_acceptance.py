"""Pure validation for the single inventory scan's RSS and GC evidence.

Thresholds are declared before measurement. This is not baseline calibration.
Only known numeric fields and fixed labels are returned; input text is private.
"""

from container_memory import Rejected as ContainerRejected, validate_container_interval

INVALID_EVIDENCE = "scan_memory_evidence_invalid"
PHASES = ("startup", "login", "scan", "shutdown", "stopped")
_U64 = (1 << 64) - 1
_I64 = (1 << 63) - 1
_COUNTERS = ("totalAllocBytes", "numGC", "pauseTotalNs")
_SAMPLE_NUMBERS = ("elapsedNanos", "rssBytes", "heapBytes", *_COUNTERS, "goroutines")
_GC_TIMES = ("countersStartedNanos", "countersFinishedNanos", "histogramStartedNanos", "histogramFinishedNanos")
_BUDGET = {
    "version": 1, "workload": "inventory-scan-500000-v1", "scope": "worker-process",
    "fixtureFiles": 500000, "fixtureBytesPerFile": 34, "gogcPercent": 100,
    "gomaxprocs": 2, "goMemoryLimitBytes": 536870912,
    "processRssBudgetBytes": 486539264, "containerMemoryBytes": 805306368,
    "sampleEveryMillis": 1000, "maxSamples": 3600,
    "maxSampleGapNanos": 5000000000, "maxElapsedNanos": 3600000000000,
    "gcMetric": "/sched/pauses/total/gc:seconds", "maxPauseUpperNanos": 50000000,
    "pauseRatioNumerator": 1, "pauseRatioDenominator": 100,
}


class Rejected(ValueError):
    """A fixed failure code, without input values or underlying errors."""


def _need(condition):
    if not condition:
        raise Rejected(INVALID_EVIDENCE)


def _object(value):
    _need(type(value) is dict)
    return value


def _uint(value, expected=None):
    _need(type(value) is int and 0 <= value <= _U64)
    if expected is not None:
        _need(value == expected)
    return value


def validate_scan_budget(budget):
    budget = _object(budget)
    _need(budget.keys() == _BUDGET.keys() | {"basis"})
    for key, expected in _BUDGET.items():
        _need(type(budget.get(key)) is type(expected) and budget[key] == expected)
    _need(type(budget["basis"]) is str and 1 <= len(budget["basis"]) <= 1024)
    return dict(_BUDGET)


def _gc_snapshot(value, elapsed):
    value = _object(value)
    counters = _object(value.get("counters"))
    counters = {key: _uint(counters.get(key)) for key in _COUNTERS}
    times = {key: _uint(value.get(key)) for key in _GC_TIMES}
    ordered = list(times.values())
    _need(ordered == sorted(ordered) and ordered[-1] <= elapsed)
    histogram = _object(value.get("histogram"))
    _need(histogram.get("metric") == _BUDGET["gcMetric"])
    bounds, counts = histogram.get("boundsNanos"), histogram.get("counts")
    _need(type(bounds) is list and 3 <= len(bounds) <= 1024)
    _need(type(counts) is list and len(counts) + 1 == len(bounds))
    _need(bounds[0] == "-Inf" and bounds[-1] == "+Inf")
    finite = [_uint(bound) for bound in bounds[1:-1]]
    _need(finite[0] == 0 and finite[-1] <= _I64)
    _need(all(a < b for a, b in zip(finite, finite[1:])))
    counts = [_uint(count) for count in counts]
    _need(sum(counts) <= _U64 and counts[0] == 0)
    return {"counters": counters, **times, "histogram": {
        "metric": _BUDGET["gcMetric"], "boundsNanos": ["-Inf", *finite, "+Inf"], "counts": counts}}


def validate_scan_gc(before, after, elapsed, budget):
    return validate_gc_interval(before, after, elapsed, validate_scan_budget(budget))


def validate_gc_interval(before, after, elapsed, budget):
    """Use whole-interval histogram deltas, with conservative integer bounds.

    Counter and histogram reads are not atomic. Each ratio uses the shortest
    interval guaranteed by that metric's recorded read boundaries.
    The workload entry point must first validate its frozen budget. The scan
    and image workloads share these raw measurements and the same GC metric.
    """
    budget = _object(budget)
    for key in ("maxElapsedNanos", "maxPauseUpperNanos", "pauseRatioNumerator", "pauseRatioDenominator"):
        _need(_uint(budget.get(key)) > 0)
    _need(budget.get("gcMetric") == _BUDGET["gcMetric"])
    elapsed = _uint(elapsed)
    _need(0 < elapsed <= budget["maxElapsedNanos"])
    before, after = _gc_snapshot(before, elapsed), _gc_snapshot(after, elapsed)
    _need(before["histogram"]["boundsNanos"] == after["histogram"]["boundsNanos"])
    histogram_span = after["histogramStartedNanos"] - before["histogramFinishedNanos"]
    counter_span = after["countersStartedNanos"] - before["countersFinishedNanos"]
    _need(0 < histogram_span <= elapsed and 0 < counter_span <= elapsed)
    counter_delta = {}
    for key in _COUNTERS:
        value = after["counters"][key] - before["counters"][key]
        _need(value >= 0)
        counter_delta[key] = value
    delta = [a - b for b, a in zip(before["histogram"]["counts"], after["histogram"]["counts"])]
    _need(all(value >= 0 for value in delta))
    total, maximum = 0, None
    for count, upper in zip(delta, after["histogram"]["boundsNanos"][1:]):
        if not count:
            continue
        _need(type(upper) is int and 0 < upper <= budget["maxPauseUpperNanos"])
        total += count * upper
        _need(total <= _U64)
        maximum = upper
    denominator, numerator = budget["pauseRatioDenominator"], budget["pauseRatioNumerator"]
    _need(total * denominator <= histogram_span * numerator)
    _need(counter_delta["pauseTotalNs"] * denominator <= counter_span * numerator)
    return {"passed": True, "metric": budget["gcMetric"], "before": before, "after": after,
            "counterDelta": counter_delta, "histogramCountDelta": delta,
            "observedPauses": sum(delta), "maxPauseUpperNanos": maximum,
            "histogramPauseUpperNanos": total, "histogramIntervalNanos": histogram_span,
            "counterIntervalNanos": counter_span}


def _resident(value, elapsed, budget):
    value = _object(value)
    for key, expected in (("version", 1), ("sampleEveryMillis", 1000), ("maxSamples", 3600)):
        _uint(value.get(key), expected)
    _need(value.get("scope") == "worker-process" and value.get("rssSource") == "/proc/self/statm")
    _need(value.get("complete") is True and value.get("approximate") is True)
    source = value.get("samples")
    _need(type(source) is list and len(PHASES) <= len(source) <= budget["maxSamples"])
    samples, phases = [], []
    for raw in source:
        raw = _object(raw)
        sample = {key: _uint(raw.get(key)) for key in _SAMPLE_NUMBERS}
        phase = raw.get("phase")
        _need(type(phase) is str and phase in PHASES)
        sample["phase"] = phase
        if not phases or phase != phases[-1]:
            phases.append(phase)
        _need(phases == list(PHASES[:len(phases)]))
        _need(0 < sample["rssBytes"] <= budget["processRssBudgetBytes"] and sample["goroutines"] > 0)
        _need(sample["elapsedNanos"] <= elapsed)
        if samples:
            prior = samples[-1]
            _need(0 < sample["elapsedNanos"] - prior["elapsedNanos"] <= budget["maxSampleGapNanos"])
            _need(all(sample[key] >= prior[key] for key in _COUNTERS))
        else:
            _need(sample["elapsedNanos"] <= 1000000000)
        samples.append(sample)
    _need(phases == list(PHASES) and elapsed - samples[-1]["elapsedNanos"] <= budget["maxSampleGapNanos"])
    return {"version": 1, "scope": "worker-process", "rssSource": "/proc/self/statm",
            "approximate": True, "sampleEveryMillis": 1000, "maxSamples": 3600,
            "complete": True, "sampleCount": len(samples), "samples": samples,
            "peakRssBytes": max(s["rssBytes"] for s in samples),
            "peakHeapBytes": max(s["heapBytes"] for s in samples)}


def validate_scan_memory(report, inspect, budget, expected_files=500000):
    """Validate the successful Go report and actual exited container together.

    Explicit 1000-file smoke checks use the same bounds but are labeled smoke.
    A smoke result is never the 500000-entry requirement's acceptance result.
    """
    fixed = validate_scan_budget(budget)
    _need(type(expected_files) is int and expected_files in (1000, 500000))
    report = _object(report)
    _uint(report.get("version"), 1)
    _need(report.get("result") == "passed" and report.get("errorCode", "") == ""
          and report.get("originalSamplesUnchanged") is True)
    _uint(report.get("fixtureFiles"), expected_files)
    expected_bytes = expected_files * fixed["fixtureBytesPerFile"]
    _uint(report.get("fixtureBytes"), expected_bytes)
    configuration = _object(report.get("configuration"))
    required_config = {"workers": 1, "maxEntries": 500000, "probe": False,
                       "nfoMode": "off", "ignoreMode": "off", "gomaxprocs": 2}
    for key, expected in required_config.items():
        _need(type(configuration.get(key)) is type(expected) and configuration[key] == expected)
    memory = _object(report.get("memoryProfile"))
    _uint(memory.get("version"), 1)
    _need(memory.get("complete") is True)
    elapsed = _uint(memory.get("elapsedNanos"))
    _need(0 < elapsed <= fixed["maxElapsedNanos"])
    _uint(memory.get("elapsedMillis"), elapsed // 1000000)
    runtime = _object(memory.get("runtime"))
    _uint(runtime.get("gogcPercent"), fixed["gogcPercent"])
    _uint(runtime.get("goMemoryLimitBytes"), fixed["goMemoryLimitBytes"])
    try:
        container = validate_container_interval(memory.get("before"), memory.get("after"), inspect)
    except ContainerRejected:
        raise Rejected(INVALID_EVIDENCE) from None
    resident = _resident(memory.get("resident"), elapsed, fixed)
    gc = validate_scan_gc(memory.get("gcBefore"), memory.get("gcAfter"), elapsed, budget)
    read_keys = ("beforeReadStartedNanos", "beforeReadFinishedNanos", "afterReadStartedNanos", "afterReadFinishedNanos")
    reads = {key: _uint(memory.get(key)) for key in read_keys}
    chronology = [reads[read_keys[0]], reads[read_keys[1]], gc["before"]["countersStartedNanos"],
                  gc["before"]["histogramFinishedNanos"], resident["samples"][0]["elapsedNanos"],
                  resident["samples"][-1]["elapsedNanos"], gc["after"]["countersStartedNanos"],
                  gc["after"]["histogramFinishedNanos"], reads[read_keys[2]], reads[read_keys[3]], elapsed]
    _need(chronology == sorted(chronology))
    _need(all(gc["before"]["counters"][k] <= resident["samples"][0][k]
              <= resident["samples"][-1][k] <= gc["after"]["counters"][k] for k in _COUNTERS))
    scan = _object(report.get("scan"))
    _need(scan.get("state") == "succeeded" and scan.get("reviewRequired") is False)
    required_scan = {"attempts": 1, "files": expected_files, "bytes": expected_bytes,
                     "skipped": 0, "missing": 0, "inventoryRows": expected_files,
                     "baselineRows": expected_files, "rootCount": 1,
                     "doneDirectories": (expected_files + 999) // 1000 + 1, "pendingDirectories": 0}
    for key, expected in required_scan.items():
        _uint(scan.get(key), expected)
    started, finished = _uint(scan.get("startedNanos")), _uint(scan.get("finishedNanos"))
    _need(0 < started < finished <= elapsed)
    _uint(scan.get("elapsedNanos"), finished - started)
    phase_start = {phase: next(s["elapsedNanos"] for s in resident["samples"] if s["phase"] == phase) for phase in PHASES}
    _need(phase_start["scan"] <= started < finished <= phase_start["shutdown"])
    shutdown = _object(report.get("shutdown"))
    _need(shutdown.get("result") == "passed" and shutdown.get("signal") == "SIGTERM")
    _need(shutdown.get("httpClosed") is True and shutdown.get("lifetimeCancelled") is True)
    _uint(shutdown.get("activeLeases"), 0)
    _uint(shutdown.get("poolConnections"), 0)
    return {"validated": True, "scope": "acceptance" if expected_files == 500000 else "smoke",
            "fixtureFiles": expected_files, "fixtureBytes": expected_bytes,
            "configuration": required_config, "elapsedNanos": elapsed,
            "container": container, "runtime": {"gogcPercent": 100, "goMemoryLimitBytes": 536870912},
            "resident": resident, "gc": gc, **reads,
            "scan": {**required_scan, "state": "succeeded", "reviewRequired": False,
                     "startedNanos": started, "finishedNanos": finished, "elapsedNanos": finished - started},
            "shutdown": {"result": "passed", "signal": "SIGTERM", "httpClosed": True,
                         "lifetimeCancelled": True, "activeLeases": 0, "poolConnections": 0},
            "originalSamplesUnchanged": True, "budget": fixed}
