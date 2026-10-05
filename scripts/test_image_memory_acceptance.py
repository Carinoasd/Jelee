"""Synthetic contract tests; these are not measured image workload results."""

import copy
import json
from pathlib import Path
import unittest

import image_memory_acceptance as m
from test_container_memory import inspect_fixture as container_inspect
from test_scan_memory_acceptance import report_fixture as scan_fixture


def budget_fixture():
    return json.loads((Path(__file__).resolve().parent.parent / "tools/image-memory-budget.json").read_bytes())


def inspect_fixture():
    value = container_inspect()
    value["HostConfig"]["Tmpfs"] = {"/image-work": "rw,noexec,nosuid,nodev,size=64m,mode=0700,uid=65532,gid=65532"}
    value["Mounts"] = [{"Type": "bind", "Destination": "/media", "Source": "PRIVATE_HOST_DIRECTORY", "RW": False}]
    return value


def report_fixture(items=100000):
    memory = scan_fixture()["memoryProfile"]
    memory["elapsedNanos"], memory["elapsedMillis"] = 8400000000, 8400
    memory["resident"]["maxSamples"] = 3664
    sample = memory["resident"]["samples"][0]
    memory["resident"]["samples"] = [
        {**sample, "phase": phase, "elapsedNanos": 100000000+index*1000000000,
         "totalAllocBytes": 2000+index*1000, "numGC": 2+index*20, "pauseTotalNs": 200+index*100}
        for index, phase in enumerate(("startup", "login", "cold", "cold", "warm", "negative", "cancellation", "shutdown", "stopped"))]
    memory["gcAfter"].update(countersStartedNanos=8200000010, countersFinishedNanos=8200000020,
                             histogramStartedNanos=8200000030, histogramFinishedNanos=8200000040)
    memory.update(afterReadStartedNanos=8300000000, afterReadFinishedNanos=8300000100)
    memory["processor"] = {"observations": 8, "maxActive": 2, "maxReservedBytes": 192<<20,
                           "maxEstimatedImageBytes": 90<<20, "maxCacheEntries": 128, "maxCacheBytes": 128000}
    empty = {key: 0 for key in m._STAT_NUMBERS}
    cold_after = {**empty, "maxEstimatedImageBytes": 90<<20, "admitted": items, "completed": items,
                  "cacheMisses": items, "decodes": items, "cacheEntries": 128,
                  "cacheBytes": 128000, "cacheEvictions": items-128}
    warm_after = {**cold_after, "admitted": items+192, "completed": items+192, "cacheHits": 192}
    boundary = max(100, items//20)
    return {"version": 1, "result": "passed", "errorCode": "", "fixtureItems": items,
            "configuration": dict(m._CONFIGURATION),
            "fixtures": {"items": items, "mediaSources": items, "distinctPaths": items,
                         "jpeg": items-boundary, "png": boundary-64, "png16": 64},
            "cold": {"startedNanos": 2200000000, "finishedNanos": 3900000000, "elapsedNanos": 1700000000,
                     "get200": items, "head200": 0, "notModified304": 0, "httpBytes": items*700,
                     "before": empty, "after": cold_after},
            "warm": {"startedNanos": 4200000000, "finishedNanos": 4900000000, "elapsedNanos": 700000000,
                     "get200": 64, "head200": 64, "notModified304": 64, "httpBytes": 64*700,
                     "before": dict(cold_after), "after": warm_after},
            "negative": {key: True for key in m._NEGATIVE},
            "cancellation": {"blockedQueries": 2, **{key: True for key in m._CANCELLATION}},
            "sourceSampleCount": 64, "originalSamplesUnchanged": True,
            "shutdown": {"result": "passed", "signal": "SIGTERM", "inFlightBeforeStop": True,
                         "httpClosed": True, "lifetimeCancelled": True, "scratchEmpty": True,
                         "activeImages": 0, "cacheEntries": 0, "cacheBytes": 0, "poolConnections": 0},
            "memoryProfile": memory}


class ImageMemoryAcceptanceTests(unittest.TestCase):
    def validate(self, report, **kwargs):
        return m.validate_image_memory(report, inspect_fixture(), budget_fixture(), **kwargs)

    def rejected(self, call, *args, **kwargs):
        with self.assertRaises(m.Rejected) as caught:
            call(*args, **kwargs)
        self.assertEqual(str(caught.exception), "image_memory_evidence_invalid")
        self.assertIsNone(caught.exception.__cause__)

    def test_full_and_smoke_are_distinct_and_projection_owns_only_safe_values(self):
        report = report_fixture()
        report["private"] = report["memoryProfile"]["gcAfter"]["private"] = "PRIVATE_DSN"
        report["memoryProfile"]["resident"]["samples"][0]["private"] = "PRIVATE_HOST"
        original = copy.deepcopy(report)
        value = self.validate(report)
        self.assertTrue(value["finalAcceptance"])
        self.assertEqual(value["scope"], "acceptance")
        self.assertEqual(value["cold"]["cacheHitNumerator"], 0)
        self.assertEqual(value["warm"]["cacheHitNumerator"], 192)
        self.assertEqual(value["gc"]["observedPauses"], 701)
        self.assertNotIn("PRIVATE", json.dumps(value))
        value["resident"]["samples"][0]["rssBytes"] = 1
        value["gc"]["after"]["histogram"]["counts"][0] = 1
        value["cold"]["after"]["completed"] = 1
        self.assertEqual(report, original)
        smoke = report_fixture(1000)
        self.rejected(self.validate, smoke)
        value = self.validate(smoke, expected_items=1000)
        self.assertFalse(value["finalAcceptance"])
        self.assertEqual(value["scope"], "smoke")
        self.assertEqual(value["fixtures"]["png16"], 64)
        for count in (True, 1, 10000, "100000", None):
            self.rejected(self.validate, report_fixture(), expected_items=count)

    def test_budget_exact_values_and_nested_types_cannot_drift(self):
        for key, value in (("processRssBudgetBytes", 486539265), ("maxSamples", 4000),
                           ("fixtureItems", 1000), ("warmItems", 63), ("gogcPercent", 50),
                           ("goMemoryLimitBytes", 805306368), ("maxPauseUpperNanos", 50000001),
                           ("pauseRatioDenominator", 99), ("maxElapsedNanos", 3600000000001),
                           ("minimumLargeImageEstimateBytes", 0), ("version", True)):
            with self.subTest(key=key):
                budget = budget_fixture()
                budget[key] = value
                self.rejected(m.validate_image_budget, budget)
        for group, key, value in (("configuration", "jobs", 0), ("configuration", "maxConcurrent", 3),
                                  ("fixtureDistribution", "jpeg", 94999), ("fixtureDistribution", "png16", True)):
            budget = budget_fixture()
            budget[group][key] = value
            self.rejected(m.validate_image_budget, budget)

    def test_rss_accepts_exact_limit_rejects_one_byte_over_in_every_phase(self):
        report = report_fixture()
        for sample in report["memoryProfile"]["resident"]["samples"]:
            sample["rssBytes"] = 486539264
        self.validate(report)
        for index in range(len(report["memoryProfile"]["resident"]["samples"])):
            bad = copy.deepcopy(report)
            bad["memoryProfile"]["resident"]["samples"][index]["rssBytes"] += 1
            self.rejected(self.validate, bad)

    def test_missing_reversed_gapped_or_invalid_samples_fail_closed(self):
        mutations = (
            lambda r: r["samples"].pop(1), lambda r: r["samples"].reverse(),
            lambda r: r.update(complete=False), lambda r: r.update(approximate=False),
            lambda r: r.update(rssSource="/proc/self/smaps"),
            lambda r: r["samples"][0].update(elapsedNanos=1000000001),
            lambda r: r["samples"][1].update(elapsedNanos=6000000000),
            lambda r: r["samples"][1].update(elapsedNanos=100000000),
            lambda r: r["samples"][1].update(rssBytes=True),
            lambda r: r["samples"][1].update(rssBytes=0),
            lambda r: r["samples"][1].update(rssBytes=float("nan")),
            lambda r: r["samples"][1].update(heapBytes=-1),
            lambda r: r["samples"][1].update(goroutines=0),
            lambda r: r["samples"][1].update(numGC=0),
            lambda r: r["samples"][1].pop("pauseTotalNs"),
            lambda r: r["samples"][1].update(totalAllocBytes=1<<64),
        )
        for mutation in mutations:
            report = report_fixture()
            mutation(report["memoryProfile"]["resident"])
            self.rejected(self.validate, report)

    def test_sample_capacity_is_bounded_without_silently_dropping_tail(self):
        memory = report_fixture()["memoryProfile"]
        resident = memory["resident"]
        exemplar = resident["samples"][0]
        phases = ["startup", "login"]+["cold"]*3657+["warm", "negative", "cancellation", "shutdown", "stopped"]
        self.assertEqual(len(phases), 3664)
        resident["samples"] = [{**exemplar, "phase": phase, "elapsedNanos": 100000000+index*1000000} for index, phase in enumerate(phases)]
        value = m._resident(resident, memory["elapsedNanos"], m.validate_image_budget(budget_fixture()))
        self.assertEqual(value["sampleCount"], 3664)
        resident["samples"].append({**resident["samples"][-1], "elapsedNanos": 3800000000})
        self.rejected(m._resident, resident, memory["elapsedNanos"], m.validate_image_budget(budget_fixture()))

    def test_gc_delegate_preserves_upper_bound_and_both_shortest_interval_gates(self):
        report = report_fixture()
        before, after = report["memoryProfile"]["gcBefore"], report["memoryProfile"]["gcAfter"]
        after.update(countersStartedNanos=8200000020, countersFinishedNanos=8200000030,
                     histogramStartedNanos=8200000040, histogramFinishedNanos=8200000050)
        before["histogram"].update(boundsNanos=["-Inf", 0, 1, "+Inf"], counts=[0, 0, 0])
        after["histogram"].update(boundsNanos=["-Inf", 0, 1, "+Inf"], counts=[0, 82000000, 0])
        after["counters"]["pauseTotalNs"] = before["counters"]["pauseTotalNs"]+82000000
        self.validate(report)
        for mutation in (lambda a: a["counters"].update(pauseTotalNs=a["counters"]["pauseTotalNs"]+1),
                         lambda a: a["histogram"]["counts"].__setitem__(1, 82000001),
                         lambda a: a["histogram"]["counts"].__setitem__(-1, 1),
                         lambda a: a["histogram"]["boundsNanos"].__setitem__(2, float("inf")),
                         lambda a: a["histogram"]["counts"].__setitem__(1, True),
                         lambda a: a.update(histogramStartedNanos=1)):
            bad = copy.deepcopy(report)
            mutation(bad["memoryProfile"]["gcAfter"])
            self.rejected(self.validate, bad)
        for upper, passes in ((50000000, True), (50000001, False)):
            bad = report_fixture()
            for key in ("gcBefore", "gcAfter"):
                bad["memoryProfile"][key]["histogram"].update(boundsNanos=["-Inf", 0, upper, "+Inf"], counts=[0, 0, 0])
            bad["memoryProfile"]["gcAfter"]["histogram"]["counts"][1] = 1
            if passes:
                self.validate(bad)
            else:
                self.rejected(self.validate, bad)

    def test_memory_read_chronology_and_whole_interval_are_required(self):
        for key, value in (("complete", False), ("elapsedNanos", 0), ("elapsedNanos", 3600000000001),
                           ("elapsedMillis", 8401), ("beforeReadFinishedNanos", 11),
                           ("afterReadStartedNanos", 8000000000), ("afterReadFinishedNanos", 8400000001)):
            report = report_fixture()
            report["memoryProfile"][key] = value
            self.rejected(self.validate, report)
        for key in ("totalAllocBytes", "numGC", "pauseTotalNs"):
            report = report_fixture()
            report["memoryProfile"]["gcAfter"]["counters"][key] = 0
            self.rejected(self.validate, report)

    def test_real_http_success_and_decode_counters_must_agree(self):
        for group, key, value in (("cold", "get200", 99999), ("cold", "head200", 1),
                                  ("cold", "httpBytes", 0), ("cold", "httpBytes", 100000*(2<<20)+1),
                                  ("warm", "get200", 63), ("warm", "head200", 63),
                                  ("warm", "notModified304", 63), ("cold", "startedNanos", 1),
                                  ("warm", "finishedNanos", 5200000000), ("warm", "elapsedNanos", 1)):
            report = report_fixture()
            report[group][key] = value
            self.rejected(self.validate, report)
        for phase, key, delta in (("cold", "completed", -1), ("cold", "decodes", -1),
                                  ("cold", "cacheMisses", -1), ("cold", "cacheHits", 1),
                                  ("cold", "failed", 1), ("warm", "cacheHits", -1),
                                  ("warm", "decodes", 1), ("warm", "busy", 1),
                                  ("warm", "cacheMisses", 1), ("warm", "cacheEvictions", -1)):
            report = report_fixture()
            report[phase]["after"][key] += delta
            self.rejected(self.validate, report)

    def test_quiescent_checkpoints_and_sampled_processor_limits_are_both_needed(self):
        for key, value in (("observations", 0), ("maxActive", 1), ("maxActive", 3),
                           ("maxReservedBytes", 0), ("maxEstimatedImageBytes", 80<<20),
                           ("maxEstimatedImageBytes", (96<<20)+1), ("maxCacheEntries", 129),
                           ("maxCacheBytes", (32<<20)+1), ("observations", True)):
            report = report_fixture()
            report["memoryProfile"]["processor"][key] = value
            self.rejected(self.validate, report)
        for phase in ("cold", "warm"):
            for boundary in ("before", "after"):
                for key, value in (("active", 1), ("reservedBytes", 1), ("cacheEntries", 129), ("cacheBytes", (32<<20)+1)):
                    report = report_fixture()
                    report[phase][boundary][key] = value
                    self.rejected(self.validate, report)

    def test_actual_fixture_counts_and_default_configuration_cannot_be_substituted(self):
        for key in ("items", "mediaSources", "distinctPaths", "jpeg", "png", "png16"):
            report = report_fixture()
            report["fixtures"][key] -= 1
            self.rejected(self.validate, report)
        for key, value in (("maxConcurrent", 1), ("maxImageBytes", 256<<20), ("cacheEntries", 100000),
                           ("passwordMemoryKiB", 1024), ("passwordConcurrency", 1),
                           ("jobs", True), ("probe", True), ("ignore", True), ("gomaxprocs", 4)):
            report = report_fixture()
            report["configuration"][key] = value
            self.rejected(self.validate, report)
        for key, value in (("gogcPercent", 50), ("goMemoryLimitBytes", 768<<20)):
            report = report_fixture()
            report["memoryProfile"]["runtime"][key] = value
            self.rejected(self.validate, report)

    def test_negative_cancellation_originals_and_stop_evidence_cannot_be_omitted(self):
        for group, fields in (("negative", m._NEGATIVE), ("cancellation", m._CANCELLATION),
                              ("shutdown", ("inFlightBeforeStop", "httpClosed", "lifetimeCancelled", "scratchEmpty"))):
            for field in fields:
                report = report_fixture()
                report[group][field] = False
                self.rejected(self.validate, report)
        for group, key, value in (("cancellation", "blockedQueries", 1), ("shutdown", "activeImages", 1),
                                  ("shutdown", "cacheEntries", 1), ("shutdown", "cacheBytes", 1),
                                  ("shutdown", "poolConnections", 1), ("shutdown", "signal", "SIGKILL")):
            report = report_fixture()
            report[group][key] = value
            self.rejected(self.validate, report)
        for key, value in (("originalSamplesUnchanged", False), ("sourceSampleCount", 0),
                           ("sourceSampleCount", 65), ("result", "failed"), ("errorCode", "PRIVATE_ERROR")):
            report = report_fixture()
            report[key] = value
            self.rejected(self.validate, report)

    def test_actual_container_limits_oom_and_image_mounts_are_required(self):
        for mutation in (
            lambda i: i["HostConfig"].update(MemorySwap=805306369),
            lambda i: i["HostConfig"].update(ReadonlyRootfs=False),
            lambda i: i["State"].update(OOMKilled=True),
            lambda i: i["State"].update(ExitCode=1),
            lambda i: i["Mounts"][0].update(RW=True),
            lambda i: i["Mounts"].append(dict(i["Mounts"][0])),
            lambda i: i["Mounts"].append({"Destination": "/media/override", "Type": "bind", "RW": False}),
            lambda i: i["Mounts"].append({"Destination": "/image-work", "Type": "bind", "RW": True}),
            lambda i: i["HostConfig"]["Tmpfs"].update({"/image-work": "rw,noexec,nosuid,nodev,size=64m,mode=0777,uid=65532,gid=65532"}),
            lambda i: i["HostConfig"]["Tmpfs"].update({"/image-work": "rw,noexec,nosuid,nodev,size=128m,mode=0700,uid=65532,gid=65532"}),
        ):
            inspected = inspect_fixture()
            mutation(inspected)
            self.rejected(m.validate_image_memory, report_fixture(), inspected, budget_fixture())
        report = report_fixture()
        report["memoryProfile"]["after"]["events"]["oom_kill"] = 1
        self.rejected(self.validate, report)
        inspected = inspect_fixture()
        inspected["HostConfig"]["Tmpfs"]["/image-work"] = "rw,noexec,nosuid,nodev,size=67108864,mode=700,uid=65532,gid=65532"
        inspected["Mounts"].append({"Destination": "/image-work", "Type": "tmpfs", "RW": True})
        self.assertTrue(m.validate_image_memory(report_fixture(), inspected, budget_fixture())["validated"])


if __name__ == "__main__":
    unittest.main()
