"""Bounded replay of one soak log. Container/ownership checks remain external."""

from collections import deque
import hashlib
import json
import re

import image_memory_acceptance as image
from images_soak_samples import SampleBlocks, Rejected as SamplesRejected
from images_soak_gc import BUDGET as GC_BUDGET, validate_hourly_gc, Rejected as HourRejected
from images_soak_trend import validate_quiescent_trend, Rejected as TrendRejected
from scan_memory_acceptance import validate_gc_interval, Rejected as GCRejected
from container_memory import validate_container_interval, Rejected as ContainerRejected

SECOND = 1_000_000_000
SLOT = 300 * SECOND
CONFIGURATION = {**image._CONFIGURATION, "jobs": True}
BUDGET = {"version": 1, "workload": "local-images-scan-soak-v1", "formalRounds": 288,
          "smokeRounds": 2, "slotSeconds": 300, "fixtureItems": 1000,
          "fixtureFiles": 2008, "fixtureDirectories": 106,
          "sampleEveryMillis": 1000, "maxSamples": 90000, "maxSampleGapNanos": 5 * SECOND,
          "heartbeatSeconds": 70, "maxEventBytes": 65536, "maxReportBytes": 2097152,
          "maxRawBytes": 67108864, "processRssBudgetBytes": 486539264,
          "containerMemoryBytes": 805306368, "heapTrendToleranceBytes": 33554432,
          "rssTrendToleranceBytes": 67108864, "sustainedHourlyGrowthBytes": 1048576,
          "configuration": CONFIGURATION, "gc": GC_BUDGET}


class Rejected(ValueError):
    """Safe fixed error, never source values."""


def need(condition):
    if not condition:
        raise Rejected("images_soak_evidence_invalid")


def uint(value):
    need(type(value) is int and 0 <= value < 1 << 64)
    return value


def obj(value):
    need(type(value) is dict)
    return value


def pairs(items):
    result = {}
    for key, value in items:
        need(key not in result)
        result[key] = value
    return result


def bad_constant(_):
    raise Rejected("images_soak_evidence_invalid")


def fixed(value, expected):
    need(type(value) is type(expected))
    if type(expected) is dict:
        need(value.keys() == expected.keys())
        for key in expected:
            fixed(value[key], expected[key])
    elif type(expected) is list:
        need(len(value) == len(expected))
        for actual, wanted in zip(value, expected):
            fixed(actual, wanted)
    else:
        need(value == expected)


def validate_soak_budget(value):
    fixed(value, BUDGET)
    # JSON round-trip returns independent bounded values without a shared dict.
    return json.loads(json.dumps(BUDGET))


def phase(value, cold):
    value = obj(value)
    start, end = uint(value.get("startedNanos")), uint(value.get("finishedNanos"))
    need(start < end and uint(value.get("elapsedNanos")) == end - start)
    expected = (1000, 0, 0) if cold else (64, 64, 64)
    for key, count in zip(("get200", "head200", "notModified304"), expected):
        need(uint(value.get(key)) == count)
    need(expected[0] <= uint(value.get("httpBytes")) <= expected[0] * 2097152)
    try:
        before, after = (image._stats(value.get(k), CONFIGURATION) for k in ("before", "after"))
    except image.Rejected:
        raise Rejected("images_soak_evidence_invalid") from None
    need(after["maxEstimatedImageBytes"] >= before["maxEstimatedImageBytes"])
    delta = {key: after[key] - before[key] for key in image._STAT_COUNTERS}
    need(all(v >= 0 for v in delta.values()))
    counts = {"admitted": sum(expected), "completed": sum(expected), "failed": 0, "busy": 0,
              "cacheHits": 0 if cold else 192, "cacheMisses": 1000 if cold else 0,
              "decodes": 1000 if cold else 0}
    need(all(delta[key] == count for key, count in counts.items()))
    need(not cold or delta["cacheEvictions"] >= 872)
    return start, end, before, after


def validate_round(value, index, work_start, fixture_bytes):
    value = obj(value)
    need(uint(value.get("index")) == index)
    scheduled, start, end = (uint(value.get(k)) for k in ("scheduledNanos", "startedNanos", "finishedNanos"))
    need(scheduled == work_start + index * SLOT and scheduled <= start < end < scheduled + SLOT)
    scan = obj(value.get("scan"))
    fixed(scan, {"files": 2008, "video": 1004, "image": 1004, "bytes": fixture_bytes,
                 "doneDirectories": 106, "pending": 0, "skipped": 0, "missing": 0, "published": True})
    cold_start, cold_end, _, cold_after = phase(value.get("cold"), True)
    warm_start, warm_end, warm_before, warm_after = phase(value.get("warm"), False)
    need(cold_after == warm_before and start <= cold_start < cold_end <= warm_start < warm_end <= end)
    checkpoint = obj(value.get("checkpoint"))
    need(uint(checkpoint.get("round")) == index)
    at = uint(checkpoint.get("elapsedNanos"))
    sample = obj(checkpoint.get("resident"))
    need(warm_end <= at <= end and sample.get("elapsedNanos") == at and sample.get("phase") == "idle")
    fixed(checkpoint.get("processor"), warm_after)
    resources = obj(checkpoint.get("resources"))
    need(resources.keys() == {"runtimeConnections", "observerConnections", "activeJobs", "activeLeases", "terminalJobs", "activeSessions"})
    need(uint(resources["runtimeConnections"]) <= 8 and 1 <= uint(resources["observerConnections"]) <= 4)
    need(uint(resources["terminalJobs"]) <= 21 and uint(resources["activeSessions"]) == 2)
    need(uint(resources["activeJobs"]) == uint(resources["activeLeases"]) == 0)
    return sample


def median_twice(values):
    values = sorted(values)
    return values[5] + values[6]


def validate_soak_log(stream, run_id, scope, fixture_bytes, inspect=None):
    """Replay bounded JSONL from a binary file; never return the private input.

    The caller separately checks immutable snapshot identity, heartbeat receipt
    times, Docker exit/OOM/limits, source files, and cleanup. A valid replay is
    not a final acceptance decision.
    """
    need(type(run_id) is str and re.fullmatch(r"[0-9a-f]{32}", run_id) is not None)
    need(scope in ("smoke", "formal") and uint(fixture_bytes) > 0)
    wanted = 2 if scope == "smoke" else 288
    samples = SampleBlocks()
    digest, total, seq, elapsed = hashlib.sha256(), 0, 0, 0
    recent, dropped_at = deque(), -1
    pending = {}
    ranges = [{"rssMin": None, "rssPeak": 0, "heapMin": None, "heapPeak": 0} for _ in range(24)]
    rounds, hours = [], []
    start_record = final = rotation = work_end = None
    ready = False
    work_start = None
    first_sample = last_sample = None

    def observe(sample):
        if work_start is None or sample["elapsedNanos"] < work_start:
            return
        hour = (sample["elapsedNanos"] - work_start) // (3600 * SECOND)
        if hour >= 24:
            return
        target = ranges[hour]
        for source, lo, hi in (("rssBytes", "rssMin", "rssPeak"), ("heapBytes", "heapMin", "heapPeak")):
            target[lo] = sample[source] if target[lo] is None else min(target[lo], sample[source])
            target[hi] = max(target[hi], sample[source])

    while True:
        line = stream.readline((2 << 20) + 1)
        if not line:
            break
        need(type(line) is bytes and len(line) <= 2 << 20 and line.endswith(b"\n"))
        total += len(line)
        need(total <= 64 << 20)
        digest.update(line)
        if not line.startswith(b"{"):
            need(len(line) <= 512)
            need(re.fullmatch(rb"(?:=== RUN   TestImagesSoakAcceptance|--- PASS: TestImagesSoakAcceptance \([0-9.]+s\)|PASS)?\r?\n", line) is not None)
            continue
        try:
            record = json.loads(line.decode("utf-8"), object_pairs_hook=pairs, parse_constant=bad_constant)
        except (UnicodeError, ValueError, RecursionError):
            raise Rejected("images_soak_evidence_invalid") from None
        record = obj(record)
        need(final is None)
        if record.keys() == {"imagesSoakReadyForSIGTERM"}:
            need(not ready and work_end is not None and record["imagesSoakReadyForSIGTERM"] is True)
            ready = True
            continue
        if record.keys() == {"imagesSoakAcceptance"}:
            need(ready)
            final = obj(record["imagesSoakAcceptance"])
            continue
        need(len(line) <= 64 << 10 and record.keys() == {"imagesSoakEvent"})
        event = obj(record["imagesSoakEvent"])
        need(event.keys() == {"version", "runId", "seq", "kind", "elapsedNanos", "data"})
        need(uint(event["version"]) == 1 and event["runId"] == run_id and uint(event["seq"]) == seq)
        at = uint(event["elapsedNanos"])
        need(elapsed <= at <= 25 * 3600 * SECOND and seq < 91000)
        elapsed, seq = at, seq + 1
        data, kind = obj(event["data"]), event["kind"]
        need(seq > 1 or kind == "start")
        if kind == "start":
            need(start_record is None and seq == 1 and data.get("scope") == scope)
            fixed(data.get("configuration"), CONFIGURATION)
            fixed(data.get("runtime"), {"gogcPercent": 100, "goMemoryLimitBytes": 536870912})
            start_record = data
        elif kind == "samples":
            try:
                samples.add(data, at)
            except SamplesRejected:
                raise Rejected("images_soak_evidence_invalid") from None
            for sample in data["samples"]:
                if first_sample is None:
                    first_sample = sample
                last_sample = sample
                stamp = sample["elapsedNanos"]
                if stamp in pending:
                    fixed(sample, pending.pop(stamp))
                recent.append(sample)
                if len(recent) > 400:
                    dropped_at = recent.popleft()["elapsedNanos"]
                observe(sample)
        elif kind == "round":
            need(work_end is None and len(rounds) < wanted)
            if work_start is None:
                work_start = uint(data.get("scheduledNanos"))
                need(dropped_at < work_start)
                for sample in recent:
                    observe(sample)
            sample = validate_round(data, len(rounds), work_start, fixture_bytes)
            need(data["finishedNanos"] <= at)
            matched = next((s for s in recent if s["elapsedNanos"] == sample["elapsedNanos"]), None)
            if matched is not None:
                fixed(sample, matched)
            else:
                pending[sample["elapsedNanos"]] = sample
            if rounds:
                before, previous = data["cold"]["before"], rounds[-1]["warm"]["after"]
                need(all(before[k] >= previous[k] for k in image._STAT_COUNTERS))
            rounds.append(data)
        elif kind == "rotation":
            need(scope == "formal" and rotation is None and len(rounds) == 144 and work_end is None)
            fixed(data, {"atRound": 144, "admin": {"rotated": True, "oldRejected": True, "newAccepted": True},
                         "viewer": {"rotated": True, "oldRejected": True, "newAccepted": True}})
            rotation = data
        elif kind == "hour":
            index = len(hours)
            need(scope == "formal" and work_end is None and index < 24 and uint(data.get("index")) == index)
            need(len(rounds) == (index + 1) * 12 and work_start is not None)
            fixed(data.get("checkpointIndices"), list(range(index * 12, (index + 1) * 12)))
            need(at >= work_start + (index + 1) * 3600 * SECOND)
            for metric, key in (("heapBytes", "heapMedianTwiceBytes"), ("rssBytes", "rssMedianTwiceBytes")):
                need(uint(data.get(key)) == median_twice([r["checkpoint"]["resident"][metric] for r in rounds[-12:]]))
            for key, expected in ranges[index].items():
                need(expected is not None and uint(data.get(key)) == expected)
            if hours:
                fixed(data.get("gcBefore"), hours[-1]["gcAfter"])
            hours.append(data)
        elif kind == "workEnd":
            need(work_end is None and len(rounds) == wanted and uint(data.get("completedRounds")) == wanted)
            need(uint(data.get("workStartedNanos")) == work_start)
            need(work_start + wanted * SLOT <= uint(data.get("workFinishedNanos")) <= at)
            work_end = data
        else:
            need(False)
    need(start_record is not None and final is not None and ready and work_end is not None and not pending)
    return _finish_replay(final, start_record, rounds, hours, rotation, work_end, samples, first_sample, last_sample,
                          scope, run_id, fixture_bytes, seq, total, digest.hexdigest(), inspect)


def _finish_replay(final, start, rounds, hours, rotation, work, samples, first, last, scope, run_id, fixture_bytes, events, raw_bytes, raw_sha, inspect):
    need(uint(final.get("version")) == 1 and final.get("runID") == run_id and final.get("scope") == scope)
    need(final.get("result") == "passed" and final.get("errorCode") == "")
    fixed(final.get("configuration"), CONFIGURATION)
    fixed(final.get("rounds"), rounds)
    fixed(final.get("fixtures"), {"jpeg": 900, "png": 36, "png16": 64, "items": 1000, "mediaSources": 1000, "distinctPaths": 1000})
    for key, count in (("fixtureFiles", 2008), ("fixtureDirectories", 106), ("fixtureBytes", fixture_bytes)):
        need(uint(final.get(key)) == count)
    for key in ("workStartedNanos", "workFinishedNanos"):
        need(uint(final.get(key)) == work[key])
    need(final.get("originalSamplesUnchanged") is True and 0 < uint(final.get("sourceSampleCount")) <= 64)
    for suffix in ("Before", "After"):
        negative, cancel = obj(final.get("negative" + suffix)), obj(final.get("cancellation" + suffix))
        need(all(negative.get(k) is True for k in image._NEGATIVE))
        need(uint(cancel.get("blockedQueries")) == 2 and all(cancel.get(k) is True for k in image._CANCELLATION))
    shutdown = obj(final.get("shutdown"))
    need(shutdown.get("result") == "passed" and shutdown.get("signal") == "SIGTERM")
    need(all(shutdown.get(k) is True for k in ("inFlightBeforeStop", "httpClosed", "lifetimeCancelled", "scratchEmpty")))
    need(all(uint(shutdown.get(k)) == 0 for k in ("activeImages", "cacheEntries", "cacheBytes", "poolConnections")))
    memory = obj(final.get("memorySummary"))
    need(memory.get("complete") is True)
    try:
        summary = samples.summary()
    except SamplesRejected:
        raise Rejected("images_soak_evidence_invalid") from None
    for key in ("sampleCount", "blockCount"):
        need(uint(memory.get(key)) == summary[key])
    fixed(memory.get("processorMaxima"), {**summary["processorMaxima"], "observations": summary["sampleCount"]})
    maxima = summary["processorMaxima"]
    need(maxima["maxActive"] == 2 and maxima["maxReservedBytes"] == 192 << 20)
    need(80 << 20 < maxima["maxEstimatedImageBytes"] <= 96 << 20)
    need(maxima["maxCacheEntries"] > 0 and maxima["maxCacheBytes"] > 0)
    for key in ("runtime", "cgroupBefore", "beforeReadStartedNanos", "beforeReadFinishedNanos", "gcBefore"):
        fixed(memory.get(key), start.get(key))
    need(first is not None and last is not None and first["phase"] == "startup" and last["phase"] == "stopped")
    need(last["elapsedNanos"] >= work["workFinishedNanos"])
    elapsed = uint(memory.get("afterReadFinishedNanos"))
    try:
        whole_gc = validate_gc_interval(memory.get("gcBefore"), memory.get("gcAfter"), elapsed, GC_BUDGET)
        work_gc = validate_gc_interval(final.get("gcBefore"), final.get("gcAfter"), elapsed, GC_BUDGET)
        need(work["workStartedNanos"] <= work_gc["before"]["countersStartedNanos"]
             <= work_gc["before"]["histogramFinishedNanos"] <= work["workStartedNanos"] + 5 * SECOND)
        need(work["workFinishedNanos"] - 5 * SECOND <= work_gc["after"]["countersStartedNanos"]
             <= work_gc["after"]["histogramFinishedNanos"] <= work["workFinishedNanos"])
        chronology = [uint(memory["beforeReadStartedNanos"]), uint(memory["beforeReadFinishedNanos"]),
                      whole_gc["before"]["countersStartedNanos"], whole_gc["before"]["histogramFinishedNanos"],
                      first["elapsedNanos"], last["elapsedNanos"], whole_gc["after"]["countersStartedNanos"],
                      whole_gc["after"]["histogramFinishedNanos"], uint(memory.get("afterReadStartedNanos")), elapsed]
        need(chronology == sorted(chronology))
        need(all(whole_gc["before"]["counters"][k] <= first[k] <= last[k] <= whole_gc["after"]["counters"][k] for k in image._COUNTERS))
        if scope == "formal":
            need(len(hours) == 24 and rotation is not None)
            fixed(final.get("rotation"), rotation)
            fixed(hours[0]["gcBefore"], final["gcBefore"])
            fixed(hours[-1]["gcAfter"], final["gcAfter"])
            hourly = validate_hourly_gc([hours[0]["gcBefore"], *[h["gcAfter"] for h in hours]], work["workStartedNanos"], work["workFinishedNanos"], elapsed)
            checkpoints = [{"round": r["index"], **{k: r["checkpoint"]["resident"][k] for k in ("elapsedNanos", "heapBytes", "rssBytes", "goroutines")}} for r in rounds]
            trend = validate_quiescent_trend(checkpoints, work["workStartedNanos"])
        else:
            need(not hours and rotation is None)
            fixed(final.get("rotation"), {"atRound": 0, "admin": {"rotated": False, "oldRejected": False, "newAccepted": False}, "viewer": {"rotated": False, "oldRejected": False, "newAccepted": False}})
            hourly, trend = None, None
    except (GCRejected, HourRejected, TrendRejected):
        raise Rejected("images_soak_evidence_invalid") from None
    container = None
    if inspect is not None:
        try:
            container = validate_container_interval(memory.get("cgroupBefore"), memory.get("cgroupAfter"), inspect)
            container["imageMounts"] = image._mounts(inspect)
        except (ContainerRejected, image.Rejected):
            raise Rejected("images_soak_evidence_invalid") from None
    return {"streamValidated": True, "scope": scope, "runId": run_id, "completedRounds": len(rounds),
            "workStartedNanos": work["workStartedNanos"], "workFinishedNanos": work["workFinishedNanos"],
            "events": events, "rawBytes": raw_bytes, "rawSha256": raw_sha, "samples": summary,
            "wholeGC": whole_gc, "workGC": work_gc, "hourlyGC": hourly, "trend": trend, "container": container}
