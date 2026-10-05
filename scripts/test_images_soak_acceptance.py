"""Synthetic smoke transcript; no measured runtime or container evidence."""

import copy
import io
import json
import unittest
from pathlib import Path

import images_soak_acceptance as m
from images_soak_samples import LIMITS
from test_image_memory_acceptance import inspect_fixture, report_fixture

RUN = "a" * 32
BYTES = 2008 * 8
S = m.SECOND


def boundary(at):
    return {"counters": {"totalAllocBytes": 1000, "numGC": 0, "pauseTotalNs": 0},
            "countersStartedNanos": at + 1, "countersFinishedNanos": at + 2,
            "histogramStartedNanos": at + 3, "histogramFinishedNanos": at + 4,
            "histogram": {"metric": "/sched/pauses/total/gc:seconds",
                          "boundsNanos": ["-Inf", 0, 1000, "+Inf"], "counts": [0, 0, 0]}}


def transcript(scope="smoke"):
    wanted = 288 if scope == "formal" else 2
    end_second = 2 + wanted * 300
    start = {"scope": scope, "runtime": {"gogcPercent": 100, "goMemoryLimitBytes": 536870912},
             "configuration": dict(m.CONFIGURATION), "cgroupBefore": {},
             "beforeReadStartedNanos": 0, "beforeReadFinishedNanos": 5, "gcBefore": boundary(10)}
    events = [(14, "start", start)]
    samples = [{"elapsedNanos": i * S + S // 10, "phase": "cold", "rssBytes": 200 << 20,
                "heapBytes": 100 << 20, "goroutines": 20, "totalAllocBytes": 1000,
                "numGC": 0, "pauseTotalNs": 0} for i in range(end_second + 4)]
    samples[0]["phase"], samples[-1]["phase"] = "startup", "stopped"
    for index in (i * 300 + 8 for i in range(wanted)):
        samples[index]["phase"] = "idle"
    cuts = set(range(60, len(samples), 60)) | {len(samples)}
    if scope == "formal":
        cuts.update(3 + hour * 3600 for hour in range(1, 25))
    offset = 0
    for cut in sorted(cuts):
        batch = samples[offset:cut]
        events.append((batch[-1]["elapsedNanos"] + S // 10, "samples",
                       {"firstSampleIndex": offset, "samples": batch,
                        "processorMaxima": {**LIMITS, "observations": len(batch)}}))
        offset = cut
    rounds = []
    stats = {k: 0 for k in m.image._STAT_NUMBERS}
    for index in range(wanted):
        offset = index * 300 * S
        before = copy.deepcopy(stats)
        stats.update(admitted=stats["admitted"] + 1000, completed=stats["completed"] + 1000,
                     cacheMisses=stats["cacheMisses"] + 1000, decodes=stats["decodes"] + 1000,
                     cacheEvictions=stats["cacheEvictions"] + 1000, cacheEntries=128,
                     cacheBytes=128000, maxEstimatedImageBytes=90 << 20)
        cold = {"startedNanos": offset + 3 * S, "finishedNanos": offset + 5 * S,
                "elapsedNanos": 2 * S, "get200": 1000, "head200": 0, "notModified304": 0,
                "httpBytes": 100000, "before": before, "after": copy.deepcopy(stats)}
        before = copy.deepcopy(stats)
        stats.update(admitted=stats["admitted"] + 192, completed=stats["completed"] + 192,
                     cacheHits=stats["cacheHits"] + 192)
        warm = {"startedNanos": offset + 6 * S, "finishedNanos": offset + 7 * S,
                "elapsedNanos": S, "get200": 64, "head200": 64, "notModified304": 64,
                "httpBytes": 6400, "before": before, "after": copy.deepcopy(stats)}
        sample = samples[index * 300 + 8]
        round_value = {"index": index, "scheduledNanos": offset + 2 * S, "startedNanos": offset + 3 * S,
                       "finishedNanos": offset + 9 * S, "cold": cold, "warm": warm,
                       "scan": {"files": 2008, "video": 1004, "image": 1004, "bytes": BYTES,
                                "doneDirectories": 106, "pending": 0, "skipped": 0, "missing": 0, "published": True},
                       "checkpoint": {"round": index, "elapsedNanos": sample["elapsedNanos"],
                                      "resident": copy.deepcopy(sample), "processor": copy.deepcopy(stats),
                                      "resources": {"runtimeConnections": 3, "observerConnections": 1,
                                                    "activeJobs": 0, "activeLeases": 0, "terminalJobs": min(index + 1, 21), "activeSessions": 2}}}
        rounds.append(copy.deepcopy(round_value))
        events.append((offset + 9 * S, "round", round_value))
    if scope == "formal":
        for hour in range(24):
            events.append(((2 + (hour + 1) * 3600) * S + 3 * S // 10, "hour",
                           {"index": hour, "gcBefore": boundary((2 + hour * 3600) * S),
                            "gcAfter": boundary((2 + (hour + 1) * 3600) * S),
                            "checkpointIndices": list(range(hour * 12, (hour + 1) * 12)),
                            "heapMedianTwiceBytes": 200 << 20, "rssMedianTwiceBytes": 400 << 20,
                            "rssMin": 200 << 20, "rssPeak": 200 << 20, "heapMin": 100 << 20, "heapPeak": 100 << 20}))
        events.append(((2 + 144 * 300) * S + S // 2, "rotation",
                       {"atRound": 144, "admin": {"rotated": True, "oldRejected": True, "newAccepted": True},
                        "viewer": {"rotated": True, "oldRejected": True, "newAccepted": True}}))
    work = {"workStartedNanos": 2 * S, "workFinishedNanos": end_second * S + S // 2, "completedRounds": wanted}
    events.append((end_second * S + 6 * S // 10, "workEnd", work))
    events.append((end_second * S + 7 * S // 10, "ready", True))
    memory = {k: copy.deepcopy(start[k]) for k in ("runtime", "cgroupBefore", "beforeReadStartedNanos", "beforeReadFinishedNanos", "gcBefore")}
    memory.update(cgroupAfter={}, gcAfter=boundary((end_second + 3) * S + 3 * S // 10),
                  afterReadStartedNanos=(end_second + 3) * S + 4 * S // 10, afterReadFinishedNanos=(end_second + 3) * S + 5 * S // 10,
                  processorMaxima={**LIMITS, "observations": len(samples)}, sampleCount=len(samples), blockCount=len(cuts), complete=True)
    final = {"version": 1, "runID": RUN, "scope": scope, "result": "passed", "errorCode": "",
             "configuration": dict(m.CONFIGURATION), "rounds": rounds, "fixtureFiles": 2008,
             "fixtureDirectories": 106, "fixtureBytes": BYTES, "workStartedNanos": work["workStartedNanos"],
             "workFinishedNanos": work["workFinishedNanos"], "fixtures": {"jpeg": 900, "png": 36, "png16": 64,
             "items": 1000, "mediaSources": 1000, "distinctPaths": 1000}, "originalSamplesUnchanged": True,
             "sourceSampleCount": 44, "memorySummary": memory, "gcBefore": boundary(2 * S), "gcAfter": boundary(end_second * S),
             "rotation": {"atRound": 0, "admin": {"rotated": False, "oldRejected": False, "newAccepted": False},
                          "viewer": {"rotated": False, "oldRejected": False, "newAccepted": False}},
             "shutdown": {"result": "passed", "signal": "SIGTERM", "inFlightBeforeStop": True,
                          "httpClosed": True, "lifetimeCancelled": True, "scratchEmpty": True,
                          "activeImages": 0, "cacheEntries": 0, "cacheBytes": 0, "poolConnections": 0}}
    for suffix in ("Before", "After"):
        final["negative" + suffix] = dict.fromkeys(m.image._NEGATIVE, True)
        final["cancellation" + suffix] = {**dict.fromkeys(m.image._CANCELLATION, True), "blockedQueries": 2}
    if scope == "formal":
        final["rotation"] = copy.deepcopy(next(data for _, kind, data in events if kind == "rotation"))
    events.append(((end_second + 4) * S, "final", final))
    records, seq = [], 0
    for at, kind, data in sorted(events, key=lambda e: e[0]):
        if kind == "ready":
            records.append({"imagesSoakReadyForSIGTERM": data})
        elif kind == "final":
            records.append({"imagesSoakAcceptance": data})
        else:
            records.append({"imagesSoakEvent": {"version": 1, "runId": RUN, "seq": seq,
                                               "kind": kind, "elapsedNanos": at, "data": data}})
            seq += 1
    return records


def encode(records):
    return b"".join(json.dumps(r, separators=(",", ":")).encode() + b"\n" for r in records)


class SoakReplayTests(unittest.TestCase):
    def validate(self, records, scope="smoke"):
        return m.validate_soak_log(io.BytesIO(encode(records)), RUN, scope, BYTES)

    def test_smoke_success_is_only_stream_validation(self):
        result = self.validate(transcript())
        self.assertTrue(result["streamValidated"])
        self.assertEqual(result["scope"], "smoke")
        self.assertEqual(result["samples"]["sampleCount"], 606)
        self.assertNotIn("finalAcceptance", result)

    def test_frozen_budget_cannot_be_relaxed(self):
        path = Path(__file__).resolve().parent.parent / "tools/image-soak-budget.json"
        budget = json.loads(path.read_bytes())
        m.validate_soak_budget(budget)
        budget["processRssBudgetBytes"] += 1
        with self.assertRaises(m.Rejected):
            m.validate_soak_budget(budget)

    def test_smoke_cannot_be_formal(self):
        with self.assertRaises(m.Rejected):
            self.validate(transcript(), "formal")

    def test_all_formal_rounds_hours_and_trend_are_replayed(self):
        result = self.validate(transcript("formal"), "formal")
        self.assertEqual(result["completedRounds"], 288)
        self.assertEqual(len(result["hourlyGC"]["hours"]), 24)
        self.assertEqual(result["trend"]["heap"]["spanTwiceBytes"], 0)

    def test_corrupt_envelope_and_order(self):
        for key, value in (("seq", 2), ("runId", "b" * 32), ("version", True),
                           ("elapsedNanos", -1), ("kind", "invented")):
            records = transcript()
            records[0]["imagesSoakEvent"][key] = value
            with self.assertRaises(m.Rejected):
                self.validate(records)

    def test_report_cannot_override_stream(self):
        for key, value in (("rounds", []), ("workFinishedNanos", 100), ("fixtureFiles", 1000),
                           ("result", "failed"), ("sourceSampleCount", 0)):
            records = transcript()
            records[-1]["imagesSoakAcceptance"][key] = value
            with self.assertRaises(m.Rejected):
                self.validate(records)

    def test_missing_ready_duplicate_final_and_truncated_stream(self):
        records = transcript()
        for corrupt in (records[:-1], records + [records[-1]],
                        [r for r in records if "imagesSoakReadyForSIGTERM" not in r]):
            with self.assertRaises(m.Rejected):
                self.validate(corrupt)

    def test_checkpoint_must_match_actual_sample(self):
        records = transcript()
        for record in records:
            if record.get("imagesSoakEvent", {}).get("kind") == "round":
                record["imagesSoakEvent"]["data"]["checkpoint"]["resident"]["heapBytes"] += 1
                break
        with self.assertRaises(m.Rejected):
            self.validate(records)

    def test_raw_syntax_limits(self):
        for raw in (b'{"x":1,"x":2}\n', b'{"x":NaN}\n', b'{"x":1}',
                    b'x' * ((2 << 20) + 1), b'PRIVATE_UNKNOWN_OUTPUT\n'):
            with self.assertRaisesRegex(m.Rejected, "^images_soak_evidence_invalid$"):
                m.validate_soak_log(io.BytesIO(raw), RUN, "smoke", BYTES)

    def test_container_and_readonly_media_are_independently_checked(self):
        records = transcript()
        original = report_fixture()["memoryProfile"]
        records[0]["imagesSoakEvent"]["data"]["cgroupBefore"] = original["before"]
        memory = records[-1]["imagesSoakAcceptance"]["memorySummary"]
        memory["cgroupBefore"], memory["cgroupAfter"] = original["before"], original["after"]
        result = m.validate_soak_log(io.BytesIO(encode(records)), RUN, "smoke", BYTES, inspect_fixture())
        self.assertTrue(result["container"]["imageMounts"]["mediaReadOnly"])
        inspect = inspect_fixture()
        inspect["Mounts"][0]["RW"] = True
        with self.assertRaises(m.Rejected):
            m.validate_soak_log(io.BytesIO(encode(records)), RUN, "smoke", BYTES, inspect)


if __name__ == "__main__":
    unittest.main()
