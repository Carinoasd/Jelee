"""Pure, bounded checks for one real local-image workload's memory evidence.

The frozen targets are engineering limits, not a measured image baseline.
Only known numbers and fixed labels are projected; arbitrary input is private.
"""

import copy

from container_memory import Rejected as ContainerRejected, validate_container_interval
from scan_memory_acceptance import Rejected as GCRejected, validate_gc_interval

INVALID_EVIDENCE = "image_memory_evidence_invalid"
PHASES = ("startup", "login", "cold", "warm", "negative", "cancellation", "shutdown", "stopped")
_U64 = (1 << 64) - 1
_COUNTERS = ("totalAllocBytes", "numGC", "pauseTotalNs")
_SAMPLE_NUMBERS = ("elapsedNanos", "rssBytes", "heapBytes", *_COUNTERS, "goroutines")
_STAT_COUNTERS = ("admitted", "completed", "failed", "busy", "cacheHits", "cacheMisses", "decodes", "cacheEvictions")
_STAT_NUMBERS = ("active", "reservedBytes", "maxEstimatedImageBytes", *_STAT_COUNTERS, "cacheEntries", "cacheBytes")
_CONFIGURATION = {
    "maxConcurrent": 2, "maxImageBytes": 100663296, "maxSourceBytes": 16777216,
    "maxOutputBytes": 2097152, "maxOutputDimension": 1024, "cacheBytes": 33554432,
    "cacheEntries": 128, "defaultQuality": 85, "timeoutSeconds": 15, "cacheTTLSeconds": 300,
    "gomaxprocs": 2, "passwordMemoryKiB": 65536, "passwordIterations": 3,
    "passwordParallelism": 2, "passwordConcurrency": 2, "jobs": False, "probe": False, "ignore": False,
}
_BUDGET = {
    "version": 1, "workload": "local-primary-images-100000-v1", "scope": "worker-process",
    "fixtureItems": 100000, "fixtureDistribution": {"jpeg": 95000, "png": 4936, "png16": 64},
    "warmItems": 64, "gogcPercent": 100, "goMemoryLimitBytes": 536870912,
    "processRssBudgetBytes": 486539264, "containerMemoryBytes": 805306368,
    "sampleEveryMillis": 1000, "maxSamples": 3664, "maxSampleGapNanos": 5000000000,
    "maxElapsedNanos": 3600000000000, "minimumLargeImageEstimateBytes": 83886080,
    "gcMetric": "/sched/pauses/total/gc:seconds", "maxPauseUpperNanos": 50000000,
    "pauseRatioNumerator": 1, "pauseRatioDenominator": 100, "configuration": _CONFIGURATION,
}
_NEGATIVE = ("unauthenticated401", "aclDenied404", "aclRevoked404", "unsupported415", "corrupt404",
             "sourceLimit413", "dimensionLimit413", "noPreflightDecode", "errorsRedacted", "scratchEmpty")
_CANCELLATION = ("busy503", "retryAfter", "cancelledRequest", "cancelledQueryReleased", "recovered200")


class Rejected(ValueError):
    """Fixed failure code without source values or underlying errors."""


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


def _fixed(value, expected):
    _need(type(value) is type(expected))
    if type(expected) is dict:
        _need(value.keys() == expected.keys())
        for key in expected:
            _fixed(value[key], expected[key])
    else:
        _need(value == expected)


def validate_image_budget(budget):
    budget = _object(budget)
    _need(budget.keys() == _BUDGET.keys() | {"basis"})
    for key, value in _BUDGET.items():
        _fixed(budget[key], value)
    _need(type(budget["basis"]) is str and 1 <= len(budget["basis"]) <= 1024)
    return copy.deepcopy(_BUDGET)


def _resident(value, elapsed, budget):
    value = _object(value)
    for key, expected in (("version", 1), ("sampleEveryMillis", 1000), ("maxSamples", 3664)):
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
            "approximate": True, "sampleEveryMillis": 1000, "maxSamples": 3664,
            "complete": True, "sampleCount": len(samples), "samples": samples,
            "peakRssBytes": max(s["rssBytes"] for s in samples),
            "peakHeapBytes": max(s["heapBytes"] for s in samples)}


def _mounts(inspect):
    host = _object(inspect.get("HostConfig"))
    tmpfs = _object(host.get("Tmpfs"))
    options = tmpfs.get("/image-work")
    _need(type(options) is str and len(options) <= 256)
    parts = options.split(",")
    _need(len(parts) == 8 and len(set(parts)) == len(parts))
    flags, values = set(), {}
    for part in parts:
        if "=" in part:
            key, value = part.split("=", 1)
            _need(key not in values)
            values[key] = value
        else:
            flags.add(part)
    _need(flags == {"rw", "noexec", "nosuid", "nodev"})
    _need(values.keys() == {"size", "mode", "uid", "gid"})
    _need(values["size"] in ("64m", "64M", "67108864") and values["mode"] in ("0700", "700"))
    _need(values["uid"] == "65532" and values["gid"] == "65532")
    mounts = inspect.get("Mounts")
    _need(type(mounts) is list and len(mounts) <= 32)
    media, work = [], []
    for item in mounts:
        item = _object(item)
        destination = item.get("Destination")
        _need(type(destination) is str and 1 <= len(destination) <= 4096)
        _need(not destination.startswith("/media/") and not destination.startswith("/image-work/"))
        if destination == "/media":
            media.append(item)
        if destination == "/image-work":
            work.append(item)
    _need(len(media) == 1 and media[0].get("Type") == "bind" and media[0].get("RW") is False)
    # Docker versions may expose --tmpfs only in HostConfig.Tmpfs. If Mounts
    # also contains it, it must agree; actual owner/mode is checked by Go.
    _need(len(work) <= 1)
    if work:
        _need(work[0].get("Type") == "tmpfs" and work[0].get("RW") is True)
    return {"mediaReadOnly": True, "scratchPrivateTmpfs": True, "scratchBytes": 67108864,
            "scratchMode": "0700", "scratchUID": 65532, "scratchGID": 65532}


def _stats(value, configuration):
    value = _object(value)
    result = {key: _uint(value.get(key)) for key in _STAT_NUMBERS}
    _need(result["active"] == 0 and result["reservedBytes"] == 0)
    _need(result["maxEstimatedImageBytes"] <= configuration["maxImageBytes"])
    _need(result["cacheEntries"] <= configuration["cacheEntries"] and result["cacheBytes"] <= configuration["cacheBytes"])
    return result


def _phase(value, name, expected_items, configuration, starts):
    value = _object(value)
    started, finished = _uint(value.get("startedNanos")), _uint(value.get("finishedNanos"))
    _need(starts[name] <= started < finished <= starts["warm" if name == "cold" else "negative"])
    _uint(value.get("elapsedNanos"), finished - started)
    get, head, conditional = (expected_items, 0, 0) if name == "cold" else (64, 64, 64)
    for key, expected in (("get200", get), ("head200", head), ("notModified304", conditional)):
        _uint(value.get(key), expected)
    byte_count = _uint(value.get("httpBytes"))
    _need(get <= byte_count <= get * configuration["maxOutputBytes"])
    before, after = _stats(value.get("before"), configuration), _stats(value.get("after"), configuration)
    _need(after["maxEstimatedImageBytes"] >= before["maxEstimatedImageBytes"])
    delta = {key: after[key] - before[key] for key in _STAT_COUNTERS}
    _need(all(count >= 0 for count in delta.values()))
    expected = {"admitted": get+head+conditional, "completed": get+head+conditional,
                "failed": 0, "busy": 0, "cacheHits": 0 if name == "cold" else 192,
                "cacheMisses": get if name == "cold" else 0, "decodes": get if name == "cold" else 0}
    for key, count in expected.items():
        _need(delta[key] == count)
    if name == "cold":
        _need(all(count == 0 for count in before.values()))
        _need(delta["cacheEvictions"] >= expected_items - configuration["cacheEntries"])
    return {"startedNanos": started, "finishedNanos": finished, "elapsedNanos": finished-started,
            "get200": get, "head200": head, "notModified304": conditional, "httpBytes": byte_count,
            "before": before, "after": after, "counterDelta": delta,
            "cacheHitNumerator": expected["cacheHits"], "cacheHitDenominator": expected["admitted"]}


def validate_image_memory(report, inspect, budget, expected_items=100000):
    """Check the successful workload together with its actual exited container."""
    fixed = validate_image_budget(budget)
    _need(type(expected_items) is int and expected_items in (1000, 100000))
    report, inspect = _object(report), _object(inspect)
    _uint(report.get("version"), 1)
    _uint(report.get("fixtureItems"), expected_items)
    _need(report.get("result") == "passed" and report.get("errorCode", "") == "")
    _need(report.get("originalSamplesUnchanged") is True)
    sampled = _uint(report.get("sourceSampleCount"))
    _need(0 < sampled <= 64)
    configuration = _object(report.get("configuration"))
    for key, value in _CONFIGURATION.items():
        _fixed(configuration.get(key), value)
    boundary = max(100, expected_items//20)
    fixtures = {"jpeg": expected_items-boundary, "png": boundary-64, "png16": 64,
                "items": expected_items, "mediaSources": expected_items, "distinctPaths": expected_items}
    observed_fixtures = _object(report.get("fixtures"))
    for key, count in fixtures.items():
        _uint(observed_fixtures.get(key), count)
    memory = _object(report.get("memoryProfile"))
    _uint(memory.get("version"), 1)
    _need(memory.get("complete") is True)
    elapsed = _uint(memory.get("elapsedNanos"))
    _need(0 < elapsed <= fixed["maxElapsedNanos"])
    _uint(memory.get("elapsedMillis"), elapsed//1000000)
    runtime = _object(memory.get("runtime"))
    _uint(runtime.get("gogcPercent"), fixed["gogcPercent"])
    _uint(runtime.get("goMemoryLimitBytes"), fixed["goMemoryLimitBytes"])
    resident = _resident(memory.get("resident"), elapsed, fixed)
    try:
        container = validate_container_interval(memory.get("before"), memory.get("after"), inspect)
        gc = validate_gc_interval(memory.get("gcBefore"), memory.get("gcAfter"), elapsed, fixed)
    except (ContainerRejected, GCRejected):
        raise Rejected(INVALID_EVIDENCE) from None
    container["imageMounts"] = _mounts(inspect)
    read_keys = ("beforeReadStartedNanos", "beforeReadFinishedNanos", "afterReadStartedNanos", "afterReadFinishedNanos")
    reads = {key: _uint(memory.get(key)) for key in read_keys}
    chronology = [reads[read_keys[0]], reads[read_keys[1]], gc["before"]["countersStartedNanos"],
                  gc["before"]["histogramFinishedNanos"], resident["samples"][0]["elapsedNanos"],
                  resident["samples"][-1]["elapsedNanos"], gc["after"]["countersStartedNanos"],
                  gc["after"]["histogramFinishedNanos"], reads[read_keys[2]], reads[read_keys[3]], elapsed]
    _need(chronology == sorted(chronology))
    _need(all(gc["before"]["counters"][key] <= resident["samples"][0][key]
              <= resident["samples"][-1][key] <= gc["after"]["counters"][key] for key in _COUNTERS))
    observed = _object(memory.get("processor"))
    processor = {key: _uint(observed.get(key)) for key in ("observations", "maxActive", "maxReservedBytes", "maxEstimatedImageBytes", "maxCacheEntries", "maxCacheBytes")}
    _need(0 < processor["observations"] <= len(resident["samples"])+1)
    _need(processor["maxActive"] == 2 and processor["maxReservedBytes"] == 2*_CONFIGURATION["maxImageBytes"])
    _need(fixed["minimumLargeImageEstimateBytes"] < processor["maxEstimatedImageBytes"] <= _CONFIGURATION["maxImageBytes"])
    _need(0 < processor["maxCacheEntries"] <= 128 and 0 < processor["maxCacheBytes"] <= 33554432)
    starts = {phase: next(s["elapsedNanos"] for s in resident["samples"] if s["phase"] == phase) for phase in PHASES}
    cold = _phase(report.get("cold"), "cold", expected_items, _CONFIGURATION, starts)
    warm = _phase(report.get("warm"), "warm", expected_items, _CONFIGURATION, starts)
    _need(cold["after"] == warm["before"])
    negative, cancellation = _object(report.get("negative")), _object(report.get("cancellation"))
    _need(all(negative.get(key) is True for key in _NEGATIVE))
    _uint(cancellation.get("blockedQueries"), 2)
    _need(all(cancellation.get(key) is True for key in _CANCELLATION))
    shutdown = _object(report.get("shutdown"))
    _need(shutdown.get("result") == "passed" and shutdown.get("signal") == "SIGTERM")
    _need(all(shutdown.get(key) is True for key in ("inFlightBeforeStop", "httpClosed", "lifetimeCancelled", "scratchEmpty")))
    for key in ("activeImages", "cacheEntries", "cacheBytes", "poolConnections"):
        _uint(shutdown.get(key), 0)
    return {"validated": True, "scope": "acceptance" if expected_items == 100000 else "smoke",
            "finalAcceptance": expected_items == 100000, "fixtureItems": expected_items, "fixtures": fixtures,
            "configuration": dict(_CONFIGURATION), "elapsedNanos": elapsed, "container": container,
            "runtime": {"gogcPercent": 100, "goMemoryLimitBytes": 536870912}, "resident": resident,
            "processor": processor, "gc": gc, **reads, "cold": cold, "warm": warm,
            "negative": {key: True for key in _NEGATIVE},
            "cancellation": {"blockedQueries": 2, **{key: True for key in _CANCELLATION}},
            "shutdown": {"result": "passed", "signal": "SIGTERM", "inFlightBeforeStop": True,
                         "httpClosed": True, "lifetimeCancelled": True, "scratchEmpty": True,
                         "activeImages": 0, "cacheEntries": 0, "cacheBytes": 0, "poolConnections": 0},
            "sourceSampleCount": sampled, "originalSamplesUnchanged": True, "budget": fixed}
