"""Synthetic evidence tests, not measured workload or performance results."""

import copy
import json
from pathlib import Path
import unittest

import scan_memory_acceptance as m
from test_container_memory import inspect_fixture, profile_fixture as container_fixture


def budget_fixture():
    return json.loads((Path(__file__).resolve().parent.parent / "tools/scan-memory-budget.json").read_bytes())


def report_fixture(files=500000):
    samples = []
    for index, phase in enumerate(("startup", "login", "scan", "scan", "scan", "shutdown", "stopped")):
        samples.append({"elapsedNanos": 100000000 + index * 1000000000, "phase": phase,
                        "rssBytes": 100000000, "heapBytes": 40000000,
                        "totalAllocBytes": 2000 + index * 1000, "numGC": 2 + index * 20,
                        "pauseTotalNs": 200 + index * 100, "goroutines": 14})
    before = {"counters": {"totalAllocBytes": 1000, "numGC": 1, "pauseTotalNs": 100},
              "countersStartedNanos": 10, "countersFinishedNanos": 20,
              "histogramStartedNanos": 30, "histogramFinishedNanos": 40,
              "histogram": {"metric": "/sched/pauses/total/gc:seconds",
                            "boundsNanos": ["-Inf", 0, 1000, 20000, "+Inf"], "counts": [0, 100, 0, 0]}}
    after = {"counters": {"totalAllocBytes": 1000000, "numGC": 601, "pauseTotalNs": 1000100},
             "countersStartedNanos": 6200000010, "countersFinishedNanos": 6200000020,
             "histogramStartedNanos": 6200000030, "histogramFinishedNanos": 6200000040,
             "histogram": {"metric": "/sched/pauses/total/gc:seconds",
                           "boundsNanos": ["-Inf", 0, 1000, 20000, "+Inf"], "counts": [0, 800, 1, 0]}}
    container = container_fixture()
    return {"version": 1, "result": "passed", "fixtureFiles": files, "fixtureBytes": files * 34,
            "configuration": {"workers": 1, "maxEntries": 500000, "probe": False,
                              "nfoMode": "off", "ignoreMode": "off", "gomaxprocs": 2},
            "memoryProfile": {"version": 1, "complete": True, "elapsedNanos": 6400000000,
                              "elapsedMillis": 6400, "runtime": container["runtime"],
                              "before": container["before"], "after": container["after"],
                              "beforeReadStartedNanos": 0, "beforeReadFinishedNanos": 5,
                              "afterReadStartedNanos": 6300000000, "afterReadFinishedNanos": 6300000100,
                              "gcBefore": before, "gcAfter": after,
                              "resident": {"version": 1, "scope": "worker-process",
                                           "rssSource": "/proc/self/statm", "approximate": True,
                                           "sampleEveryMillis": 1000, "maxSamples": 3600,
                                           "complete": True, "samples": samples}},
            "scan": {"state": "succeeded", "attempts": 1, "files": files, "bytes": files * 34,
                     "skipped": 0, "missing": 0, "reviewRequired": False,
                     "inventoryRows": files, "baselineRows": files, "rootCount": 1,
                     "doneDirectories": (files + 999) // 1000 + 1, "pendingDirectories": 0,
                     "startedNanos": 2200000000, "finishedNanos": 4900000000, "elapsedNanos": 2700000000},
            "originalSamplesUnchanged": True,
            "shutdown": {"result": "passed", "signal": "SIGTERM", "httpClosed": True,
                         "lifetimeCancelled": True, "activeLeases": 0, "poolConnections": 0}}


class ScanMemoryAcceptanceTests(unittest.TestCase):
    def validate(self, report, **kwargs):
        return m.validate_scan_memory(report, inspect_fixture(), budget_fixture(), **kwargs)

    def rejected(self, call, *args, **kwargs):
        with self.assertRaises(m.Rejected) as caught:
            call(*args, **kwargs)
        self.assertEqual(str(caught.exception), "scan_memory_evidence_invalid")
        self.assertIsNone(caught.exception.__cause__)

    def gc(self, before, after, elapsed=6400000000):
        return m.validate_scan_gc(before, after, elapsed, budget_fixture())

    def test_success_projects_owned_safe_values_without_changing_input(self):
        report = report_fixture()
        report["private"] = "PRIVATE_DSN"
        report["memoryProfile"]["resident"]["samples"][0]["secret"] = "PRIVATE_DSN"
        report["memoryProfile"]["gcAfter"]["secret"] = "PRIVATE_DSN"
        original = copy.deepcopy(report)
        result = self.validate(report)
        self.assertEqual(result["scope"], "acceptance")
        self.assertEqual(result["gc"]["observedPauses"], 701)
        self.assertEqual(result["gc"]["counterDelta"]["numGC"], 600)
        self.assertEqual(result["gc"]["histogramPauseUpperNanos"], 720000)
        self.assertNotIn("PRIVATE", json.dumps(result))
        result["resident"]["samples"][0]["rssBytes"] = 1
        result["gc"]["after"]["histogram"]["counts"][0] = 1
        self.assertEqual(report, original)

    def test_smoke_cannot_satisfy_full_acceptance_and_is_labeled(self):
        smoke = report_fixture(1000)
        self.rejected(self.validate, smoke)
        self.assertEqual(self.validate(smoke, expected_files=1000)["scope"], "smoke")
        for value in (True, 0, 100, "500000", None):
            self.rejected(self.validate, report_fixture(), expected_files=value)

    def test_budget_cannot_be_raised_or_recalibrated_from_observation(self):
        for key, value in (("processRssBudgetBytes", 486539265), ("gogcPercent", 50),
                           ("maxPauseUpperNanos", 50000001), ("pauseRatioDenominator", 99),
                           ("maxSamples", 4000), ("version", True), ("workload", "other")):
            with self.subTest(key=key):
                budget = budget_fixture()
                budget[key] = value
                self.rejected(m.validate_scan_budget, budget)

    def test_rss_equal_budget_passes_but_one_byte_over_in_any_phase_fails(self):
        report = report_fixture()
        report["memoryProfile"]["resident"]["samples"][0]["rssBytes"] = 486539264
        self.validate(report)
        for index in range(7):
            changed = copy.deepcopy(report)
            changed["memoryProfile"]["resident"]["samples"][index]["rssBytes"] = 486539265
            self.rejected(self.validate, changed)

    def test_missing_reversed_incomplete_or_gapped_samples_are_rejected(self):
        mutations = (
            lambda r: r["samples"].pop(1),
            lambda r: r["samples"].reverse(),
            lambda r: r.update(complete=False),
            lambda r: r["samples"][0].update(elapsedNanos=1000000001),
            lambda r: r["samples"][1].update(elapsedNanos=6000000000),
            lambda r: r["samples"][1].update(rssBytes=True),
            lambda r: r["samples"][1].update(numGC=0),
            lambda r: r["samples"].__imul__(600),
        )
        for mutation in mutations:
            report = report_fixture()
            mutation(report["memoryProfile"]["resident"])
            self.rejected(self.validate, report)

    def test_scan_requires_real_counts_full_publication_and_bounded_lifecycle(self):
        for key, value in (("state", "running"), ("attempts", 2), ("files", 499999),
                           ("bytes", 16999999), ("skipped", 1), ("missing", 1),
                           ("reviewRequired", True), ("inventoryRows", 499999),
                           ("baselineRows", 499999), ("rootCount", 2),
                           ("doneDirectories", 500), ("pendingDirectories", 1),
                           ("startedNanos", 100), ("finishedNanos", 6400000001),
                           ("elapsedNanos", 1)):
            with self.subTest(key=key):
                report = report_fixture()
                report["scan"][key] = value
                self.rejected(self.validate, report)
        for key, value in (("httpClosed", False), ("lifetimeCancelled", False),
                           ("activeLeases", 1), ("poolConnections", 1), ("signal", "SIGKILL")):
            report = report_fixture()
            report["shutdown"][key] = value
            self.rejected(self.validate, report)

    def test_effective_configuration_runtime_and_container_are_all_required(self):
        for group, key, value in (("configuration", "workers", 2), ("configuration", "probe", True),
                                  ("configuration", "nfoMode", "read-only"), ("configuration", "ignoreMode", "merged"),
                                  ("configuration", "gomaxprocs", 4), ("configuration", "maxEntries", 1000)):
            report = report_fixture()
            report[group][key] = value
            self.rejected(self.validate, report)
        report = report_fixture()
        report["memoryProfile"]["runtime"]["goMemoryLimitBytes"] += 1
        self.rejected(self.validate, report)
        inspect = inspect_fixture()
        inspect["HostConfig"]["MemorySwap"] += 1
        self.rejected(m.validate_scan_memory, report_fixture(), inspect, budget_fixture())
        report = report_fixture()
        report["memoryProfile"]["after"]["events"]["oom_kill"] = 1
        self.rejected(self.validate, report)

    def test_gc_uses_all_events_beyond_256_and_keeps_zero_as_unobserved(self):
        p = report_fixture()["memoryProfile"]
        result = self.gc(p["gcBefore"], p["gcAfter"])
        self.assertEqual(result["histogramCountDelta"], [0, 700, 1, 0])
        p["gcAfter"]["histogram"]["counts"] = p["gcBefore"]["histogram"]["counts"].copy()
        result = self.gc(p["gcBefore"], p["gcAfter"])
        self.assertEqual(result["observedPauses"], 0)
        self.assertIsNone(result["maxPauseUpperNanos"])

    def test_gc_pause_threshold_uses_upper_bound_not_bucket_midpoint(self):
        for upper, accepted in ((50000000, True), (50000001, False), (60000000, False)):
            p = report_fixture()["memoryProfile"]
            for key in ("gcBefore", "gcAfter"):
                p[key]["histogram"] = {"metric": "/sched/pauses/total/gc:seconds",
                                      "boundsNanos": ["-Inf", 0, 40000000, upper, "+Inf"],
                                      "counts": [0, 0, 0, 0]}
            p["gcAfter"]["histogram"]["counts"][2] = 1
            if accepted:
                self.assertEqual(self.gc(p["gcBefore"], p["gcAfter"])["maxPauseUpperNanos"], upper)
            else:
                self.rejected(self.gc, p["gcBefore"], p["gcAfter"])

    def test_both_gc_ratio_gates_accept_equality_and_reject_one_ns_over(self):
        p = report_fixture()["memoryProfile"]
        before, after = p["gcBefore"], p["gcAfter"]
        # Exact 6-second read intervals and an exact 60ms aggregate upper sum.
        after.update(countersStartedNanos=6000000020, countersFinishedNanos=6000000030,
                     histogramStartedNanos=6000000040, histogramFinishedNanos=6000000050)
        after["counters"]["pauseTotalNs"] = before["counters"]["pauseTotalNs"] + 60000000
        before["histogram"]["boundsNanos"] = after["histogram"]["boundsNanos"] = ["-Inf", 0, 1, "+Inf"]
        before["histogram"]["counts"] = [0, 0, 0]
        after["histogram"]["counts"] = [0, 60000000, 0]
        self.gc(before, after)
        changed = copy.deepcopy(after)
        changed["counters"]["pauseTotalNs"] += 1
        self.rejected(self.gc, before, changed)
        changed = copy.deepcopy(after)
        changed["histogram"]["counts"][1] += 1
        self.rejected(self.gc, before, changed)

    def test_gc_rejects_bad_bounds_types_counter_reversal_and_infinite_events(self):
        mutations = (
            lambda a: a["histogram"]["counts"].__setitem__(-1, 1),
            lambda a: a["histogram"]["counts"].__setitem__(0, 1),
            lambda a: a["histogram"]["counts"].__setitem__(1, 99),
            lambda a: a["histogram"]["counts"].__setitem__(1, True),
            lambda a: a["histogram"]["counts"].__setitem__(1, 1 << 64),
            lambda a: a["histogram"]["boundsNanos"].__setitem__(2, 1001),
            lambda a: a["histogram"]["boundsNanos"].__setitem__(2, 1000.0),
            lambda a: a["histogram"]["boundsNanos"].__setitem__(-1, float("inf")),
            lambda a: a["histogram"].update(metric="/gc/pauses:seconds"),
            lambda a: a["counters"].update(numGC=0),
            lambda a: a.update(histogramStartedNanos=1),
        )
        for mutation in mutations:
            p = report_fixture()["memoryProfile"]
            mutation(p["gcAfter"])
            self.rejected(self.gc, p["gcBefore"], p["gcAfter"])

    def test_profile_chronology_and_complete_final_report_are_required(self):
        for key, value in (("complete", False), ("elapsedNanos", 0), ("elapsedMillis", 6401),
                           ("afterReadFinishedNanos", 6400000001), ("beforeReadFinishedNanos", 100)):
            report = report_fixture()
            report["memoryProfile"][key] = value
            self.rejected(self.validate, report)
        for key, value in (("result", "failed"), ("errorCode", "failed"),
                           ("originalSamplesUnchanged", False), ("fixtureBytes", 1)):
            report = report_fixture()
            report[key] = value
            self.rejected(self.validate, report)


if __name__ == "__main__":
    unittest.main()
