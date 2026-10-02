"""RSS evidence contract tests; fixtures are not measured baselines."""
import copy
import hashlib
import json
import unittest

import resident_memory as m


def profile_fixture():
    samples = []
    phases = ["startup", "cold", "warm", "changed"] + ["sustained"] * 300
    phases += ["cancellation", "shutdown", "stopped"]
    for second, phase in enumerate(phases):
        samples.append({"elapsedNanos": second * 1_000_000_000, "phase": phase,
                        "rssBytes": (144 if phase == "cold" else 80) * 1024 * 1024,
                        "heapBytes": (128 if phase == "cold" else 8) * 1024 * 1024,
                        "totalAllocBytes": 200 * 1024 * 1024 + second * 1024,
                        "numGC": second // 10, "pauseTotalNs": second * 100,
                        "goroutines": 14})
    return {"version": 1, "elapsedMillis": 306000,
            "runtime": {"gogcPercent": 100, "goMemoryLimitBytes": 536870912},
            "sourceDigest": "f" * 64,
            "resident": {"version": 1, "scope": "worker-process", "sampleEveryMillis": 1000,
                         "rssSource": "/proc/self/statm", "approximate": True,
                         "maxSamples": 1024, "complete": True, "samples": samples}}


def budget_fixture():
    return {"version": 1, "scope": "worker-process", "workload": "mixed-1000-default-kdf-v1",
            "rssSource": "/proc/self/statm",
            "sampleEveryMillis": 1000, "profiles": [
                {"gogcPercent": 100, "goMemoryLimitBytes": 536870912,
                 "processRssBudgetBytes": 192 * 1024 * 1024, "baselinePeakBytes": 160 * 1024 * 1024,
                 "baselineSourceDigest": "a" * 64, "baselineEvidenceSha256": "b" * 64},
                {"gogcPercent": 50, "goMemoryLimitBytes": 536870912,
                 "processRssBudgetBytes": 176 * 1024 * 1024, "baselinePeakBytes": 152 * 1024 * 1024,
                 "baselineSourceDigest": "c" * 64, "baselineEvidenceSha256": "d" * 64}]}


def baseline_fixture():
    budget = budget_fixture()
    cases = []
    for reference in budget["profiles"]:
        profile = profile_fixture()
        profile["runtime"]["gogcPercent"] = reference["gogcPercent"]
        profile["resident"]["samples"][1]["rssBytes"] = reference["baselinePeakBytes"]
        cases.append({"gogcPercent": reference["gogcPercent"],
                      "sourceDigest": reference["baselineSourceDigest"], "memoryProfile": profile})
    baseline = {"version": 1, "cases": cases}
    digest = hashlib.sha256((json.dumps(baseline, sort_keys=True) + "\n").encode()).hexdigest()
    for reference in budget["profiles"]:
        reference["baselineEvidenceSha256"] = digest
    return budget, baseline, digest


def results_fixture(budget):
    cases = []
    for gogc in (100, 50):
        profile = profile_fixture()
        profile["runtime"]["gogcPercent"] = gogc
        cases.append({"result": "passed", "testArtifactsCleaned": True, "sourceUnchanged": True,
                      "sourceDigest": "f" * 64, "memoryProfile": profile,
                      "residentValidation": m.validate_resident_budget(profile, budget, gogc)})
    return cases


class ResidentMemoryTests(unittest.TestCase):
    def rejected(self, call, *args, **kwargs):
        with self.assertRaises(m.Rejected) as raised:
            call(*args, **kwargs)
        self.assertEqual(str(raised.exception), "resident_memory_evidence_invalid")
        self.assertIsNone(raised.exception.__cause__)

    def test_baseline_validation_is_an_owned_projection_not_a_budget_pass(self):
        profile = profile_fixture()
        profile["secret"] = "PRIVATE_DSN=secret/private"
        profile["resident"]["samples"][1]["secret"] = "secret/private"
        original = copy.deepcopy(profile)
        result = m.validate_resident_samples(profile)
        self.assertTrue(result["samplesValidated"])
        self.assertEqual(result["rssSource"], "/proc/self/statm")
        self.assertIs(result["approximate"], True)
        self.assertNotIn("budgetPassed", result)
        self.assertNotIn("budget", result)
        self.assertNotIn("secret", json.dumps(result))
        self.assertEqual(result["sampleCount"], 307)
        self.assertEqual(result["peakRssBytes"], 150994944)
        self.assertEqual(result["peakHeapBytes"], 134217728)
        self.assertEqual([p["phase"] for p in result["phases"]],
                         ["startup", "cold", "warm", "changed", "sustained", "cancellation", "shutdown", "stopped"])
        self.assertEqual(result["phases"][4]["sampleCount"], 300)
        self.assertEqual(result["sustainedNanos"], 300000000000)
        # Last sustained ticker is only 299 seconds after its first sample.
        self.assertEqual(result["phases"][4]["lastElapsedNanos"] - result["phases"][4]["firstElapsedNanos"], 299000000000)
        result["samples"][0]["rssBytes"] = 1
        result["phases"][0]["peakRssBytes"] = 1
        self.assertEqual(profile, original)

    def test_frozen_budget_accepts_equal_limit_and_rejects_one_byte_over_in_any_phase(self):
        budget = budget_fixture()
        bound = budget["profiles"][0]["processRssBudgetBytes"]
        for phase in ("startup", "cold", "warm", "changed", "sustained", "cancellation", "shutdown", "stopped"):
            with self.subTest(phase=phase):
                profile = profile_fixture()
                sample = next(s for s in profile["resident"]["samples"] if s["phase"] == phase)
                sample["rssBytes"] = bound
                result = m.validate_resident_budget(profile, budget, 100)
                self.assertTrue(result["budgetPassed"])
                self.assertEqual(result["peakRssBytes"], bound)
                sample["rssBytes"] += 1
                self.rejected(m.validate_resident_budget, profile, budget, 100)

    def test_budget_selects_actual_runtime_and_keeps_only_safe_baseline_identity(self):
        profile, budget = profile_fixture(), budget_fixture()
        budget["reason"] = "secret/private"
        budget["profiles"][1]["reason"] = "secret/private"
        profile["runtime"]["gogcPercent"] = 50
        saved = copy.deepcopy(budget)
        result = m.validate_resident_budget(profile, budget, 50)
        self.assertEqual(result["budget"]["gogcPercent"], 50)
        self.assertEqual(result["budget"]["processRssBudgetBytes"], 184549376)
        self.assertEqual(result["budget"]["baselineSourceDigest"], "c" * 64)
        self.assertNotEqual(profile["sourceDigest"], result["budget"]["baselineSourceDigest"])
        self.assertNotIn("secret", json.dumps(result))
        result["budget"]["baselineSourceDigest"] = "0" * 64
        self.assertEqual(budget, saved)
        self.rejected(m.validate_resident_budget, profile, budget, 100)
        for invalid in (True, False, 0, 75, 200, "50", None, 50.0):
            self.rejected(m.validate_resident_budget, profile, budget, invalid)
        for key, value in (("gogcPercent", True), ("gogcPercent", "50"),
                           ("goMemoryLimitBytes", 805306368), ("goMemoryLimitBytes", None)):
            changed = copy.deepcopy(profile)
            changed["runtime"][key] = value
            self.rejected(m.validate_resident_budget, changed, budget, 50)

    def test_sample_numeric_values_are_strict_and_required(self):
        for key in ("elapsedNanos", "rssBytes", "heapBytes", "totalAllocBytes", "numGC", "pauseTotalNs", "goroutines"):
            for invalid in (True, False, -1, 1.0, float("nan"), float("inf"), "0", None, 1 << 64):
                with self.subTest(key=key, invalid=invalid):
                    profile = profile_fixture()
                    profile["resident"]["samples"][10][key] = invalid
                    self.rejected(m.validate_resident_samples, profile)
            profile = profile_fixture()
            del profile["resident"]["samples"][10][key]
            self.rejected(m.validate_resident_samples, profile)
        for key in ("rssBytes", "goroutines"):
            profile = profile_fixture()
            profile["resident"]["samples"][10][key] = 0
            self.rejected(m.validate_resident_samples, profile)

    def test_every_phase_must_be_present_in_order_and_known(self):
        for phase in ("startup", "cold", "warm", "changed", "sustained", "cancellation", "shutdown", "stopped"):
            profile = profile_fixture()
            profile["resident"]["samples"] = [s for s in profile["resident"]["samples"] if s["phase"] != phase]
            with self.subTest(missing=phase):
                self.rejected(m.validate_resident_samples, profile)
        for replacement in ("cold", "secret/private", None, True, [], {}):
            profile = profile_fixture()
            profile["resident"]["samples"][20]["phase"] = replacement
            with self.subTest(replacement=replacement):
                self.rejected(m.validate_resident_samples, profile)
        profile = profile_fixture()
        del profile["resident"]["samples"][10]["phase"]
        self.rejected(m.validate_resident_samples, profile)

    def test_time_must_increase_without_large_gaps_and_cover_real_sustained_duration(self):
        for ns in (9_000_000_000, 8_999_999_999, 15_000_000_001):
            profile = profile_fixture()
            profile["resident"]["samples"][10]["elapsedNanos"] = ns
            self.rejected(m.validate_resident_samples, profile)
        profile = profile_fixture()
        profile["resident"]["samples"] = [s for s in profile["resident"]["samples"] if not 10 <= s["elapsedNanos"] // 1_000_000_000 < 14]
        self.assertTrue(m.validate_resident_samples(profile)["samplesValidated"])  # Exactly five-second gap.
        profile["resident"]["samples"] = [s for s in profile["resident"]["samples"] if s["elapsedNanos"] != 14_000_000_000]
        self.rejected(m.validate_resident_samples, profile)
        profile = profile_fixture()
        for sample in profile["resident"]["samples"]:
            sample["elapsedNanos"] += 1_000_000_001
        profile["elapsedMillis"] += 1001
        self.rejected(m.validate_resident_samples, profile)
        profile = profile_fixture()
        for sample in profile["resident"]["samples"]:
            if sample["phase"] in ("cancellation", "shutdown", "stopped"):
                sample["elapsedNanos"] -= 1
        self.rejected(m.validate_resident_samples, profile)  # Sustained is one ns too short.

    def test_last_sample_must_match_profile_end_with_only_millisecond_rounding(self):
        for elapsed_ms in (306000, 305999, 311000):
            profile = profile_fixture()
            profile["elapsedMillis"] = elapsed_ms
            self.assertTrue(m.validate_resident_samples(profile)["samplesValidated"])
        for elapsed_ms in (305998, 311001, 0, True, "306000", None):
            profile = profile_fixture()
            profile["elapsedMillis"] = elapsed_ms
            self.rejected(m.validate_resident_samples, profile)

    def test_cumulative_runtime_counters_cannot_reset_but_memory_can_fall(self):
        for key in ("totalAllocBytes", "numGC", "pauseTotalNs"):
            profile = profile_fixture()
            profile["resident"]["samples"][20][key] = profile["resident"]["samples"][19][key] - 1
            with self.subTest(counter=key):
                self.rejected(m.validate_resident_samples, profile)
        profile = profile_fixture()
        profile["resident"]["samples"][20].update(rssBytes=1, heapBytes=0, goroutines=1)
        self.assertTrue(m.validate_resident_samples(profile)["samplesValidated"])

    def test_sample_count_is_bounded_and_metadata_cannot_raise_the_cap(self):
        profile = profile_fixture()
        samples = profile["resident"]["samples"]
        extra = []
        for i in range(1, 718):
            sample = dict(samples[0], elapsedNanos=i * 1_000_000)
            extra.append(sample)
        profile["resident"]["samples"] = samples[:1] + extra + samples[1:]
        self.assertEqual(len(profile["resident"]["samples"]), 1024)
        self.assertEqual(m.validate_resident_samples(profile)["sampleCount"], 1024)
        profile["resident"]["samples"].insert(718, dict(samples[0], elapsedNanos=718_000_000))
        self.rejected(m.validate_resident_samples, profile)
        profile["resident"]["maxSamples"] = 1025
        self.rejected(m.validate_resident_samples, profile)
        for key, value in (("version", 2), ("version", True), ("scope", "container"),
                           ("rssSource", "/proc/self/smaps_rollup"), ("rssSource", None),
                           ("approximate", False), ("approximate", 1),
                           ("sampleEveryMillis", 999), ("sampleEveryMillis", True),
                           ("maxSamples", True), ("maxSamples", 512), ("complete", False), ("complete", 1)):
            profile = profile_fixture()
            profile["resident"][key] = value
            self.rejected(m.validate_resident_samples, profile)
        for key in ("version", "scope", "rssSource", "approximate", "sampleEveryMillis", "maxSamples", "complete", "samples"):
            profile = profile_fixture()
            del profile["resident"][key]
            self.rejected(m.validate_resident_samples, profile)

    def test_frozen_budget_requires_both_distinct_profiles_and_fixed_identity(self):
        for profiles in ([], budget_fixture()["profiles"][:1], budget_fixture()["profiles"] * 2,
                         [budget_fixture()["profiles"][0]] * 2, {}, None):
            budget = budget_fixture()
            budget["profiles"] = profiles
            self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)
        for key, value in (("version", 2), ("version", True), ("scope", "container"),
                           ("rssSource", "/proc/self/smaps_rollup"), ("rssSource", None),
                           ("workload", "secret/private"), ("sampleEveryMillis", 500)):
            budget = budget_fixture()
            budget[key] = value
            self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)
        for key in ("version", "scope", "rssSource", "workload", "sampleEveryMillis", "profiles"):
            budget = budget_fixture()
            del budget[key]
            self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)
        for index in (0, 1):  # Reject a malformed unselected profile too.
            for key in ("gogcPercent", "goMemoryLimitBytes", "processRssBudgetBytes", "baselinePeakBytes"):
                for invalid in (True, False, -1, 1.0, float("nan"), "0", None, 1 << 64):
                    budget = budget_fixture()
                    budget["profiles"][index][key] = invalid
                    self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)
            for key, value in (("gogcPercent", 75), ("goMemoryLimitBytes", 805306368),
                               ("processRssBudgetBytes", 0), ("baselinePeakBytes", 0),
                               ("baselinePeakBytes", 805306368)):
                budget = budget_fixture()
                budget["profiles"][index][key] = value
                self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)
            for key in budget_fixture()["profiles"][index]:
                budget = budget_fixture()
                del budget["profiles"][index][key]
                self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)
            for key in ("baselineSourceDigest", "baselineEvidenceSha256"):
                for invalid in ("", "f" * 63, "f" * 65, "Z" * 64, "secret/private", True, None, []):
                    budget = budget_fixture()
                    budget["profiles"][index][key] = invalid
                    self.rejected(m.validate_resident_budget, profile_fixture(), budget, 100)

    def test_budget_is_explicit_not_derived_from_soft_or_hard_limits(self):
        profile, budget = profile_fixture(), budget_fixture()
        budget["profiles"][0]["baselinePeakBytes"] = budget["profiles"][0]["processRssBudgetBytes"]
        self.assertTrue(m.validate_resident_budget(profile, budget, 100)["budgetPassed"])
        # These are synthetic numbers, not a recommended real deployment budget.
        profile["resident"]["samples"][1]["rssBytes"] = 600 * 1024 * 1024
        self.rejected(m.validate_resident_budget, profile, budget, 100)
        budget["profiles"][0].update(processRssBudgetBytes=768 * 1024 * 1024, baselinePeakBytes=600 * 1024 * 1024)
        self.assertTrue(m.validate_resident_budget(profile, budget, 100)["budgetPassed"])
        # No budget can be inferred from an otherwise complete profile.
        self.rejected(m.validate_resident_budget, profile, None, 100)

    def test_malformed_shapes_and_missing_profile_fields_have_fixed_errors(self):
        for malformed in (None, [], "secret/private", True, 123):
            self.rejected(m.validate_resident_samples, malformed)
            self.rejected(m.validate_resident_budget, profile_fixture(), malformed, 100)
            for key in ("resident", "runtime"):
                profile = profile_fixture()
                profile[key] = malformed
                self.rejected(m.validate_resident_budget, profile, budget_fixture(), 100)
            profile = profile_fixture()
            profile["resident"]["samples"][10] = malformed
            self.rejected(m.validate_resident_samples, profile)
            profile = profile_fixture()
            profile["resident"]["samples"] = malformed
            self.rejected(m.validate_resident_samples, profile)
        for key in ("version", "elapsedMillis", "resident", "runtime"):
            profile = profile_fixture()
            del profile[key]
            self.rejected(m.validate_resident_budget, profile, budget_fixture(), 100)

    def test_baseline_uses_one_evidence_digest_and_allows_shared_source(self):
        budget, baseline, _ = baseline_fixture()
        baseline["cases"][1]["sourceDigest"] = baseline["cases"][0]["sourceDigest"]
        budget["profiles"][1]["baselineSourceDigest"] = budget["profiles"][0]["baselineSourceDigest"]
        baseline["private"] = "PRIVATE_DSN=secret/private"
        raw = (json.dumps(baseline, indent=2) + "\n").encode()
        digest = hashlib.sha256(raw).hexdigest()
        for entry in budget["profiles"]:
            entry["baselineEvidenceSha256"] = digest
        original = copy.deepcopy((budget, baseline))
        result = m.validate_resident_baseline(budget, baseline, digest)
        self.assertTrue(result["baselineValidated"])
        self.assertNotIn("budgetPassed", result)
        self.assertEqual(result["evidenceSha256"], digest)
        self.assertEqual([p["gogcPercent"] for p in result["profiles"]], [50, 100])
        self.assertEqual([p["peakRssBytes"] for p in result["profiles"]], [159383552, 167772160])
        self.assertEqual(result["profiles"][0]["sourceDigest"], result["profiles"][1]["sourceDigest"])
        self.assertNotIn("secret", json.dumps(result))
        result["profiles"][0]["peakRssBytes"] = 1
        self.assertEqual((budget, baseline), original)
        # Byte-level changes, even whitespace, require the pinned digest to change.
        self.rejected(m.validate_resident_baseline, budget, baseline, hashlib.sha256(raw + b"\n").hexdigest())

    def test_baseline_rejects_missing_duplicate_or_mismatched_profiles(self):
        for mutation in ("missing", "duplicate", "extra", "case-gogc", "runtime-gogc", "runtime-soft",
                         "missing-runtime", "missing-profile", "source", "samples", "peak", "method", "version"):
            with self.subTest(mutation=mutation):
                budget, baseline, digest = baseline_fixture()
                case = baseline["cases"][0]
                if mutation == "missing":
                    baseline["cases"].pop()
                elif mutation == "duplicate":
                    baseline["cases"][1] = copy.deepcopy(case)
                elif mutation == "extra":
                    baseline["cases"].append(copy.deepcopy(case))
                elif mutation == "case-gogc":
                    case["gogcPercent"] = 50
                elif mutation == "runtime-gogc":
                    case["memoryProfile"]["runtime"]["gogcPercent"] = 50
                elif mutation == "runtime-soft":
                    case["memoryProfile"]["runtime"]["goMemoryLimitBytes"] = 805306368
                elif mutation == "missing-runtime":
                    del case["memoryProfile"]["runtime"]
                elif mutation == "missing-profile":
                    del case["memoryProfile"]
                elif mutation == "source":
                    case["sourceDigest"] = "e" * 64
                elif mutation == "samples":
                    case["memoryProfile"]["resident"]["samples"][10]["numGC"] = 0
                    case["memoryProfile"]["resident"]["samples"][9]["numGC"] = 1
                elif mutation == "peak":
                    case["memoryProfile"]["resident"]["samples"][1]["rssBytes"] += 1
                elif mutation == "method":
                    case["memoryProfile"]["resident"]["rssSource"] = "/proc/self/smaps_rollup"
                else:
                    baseline["version"] = 2
                self.rejected(m.validate_resident_baseline, budget, baseline, digest)
        for field in ("gogcPercent", "sourceDigest", "memoryProfile"):
            budget, baseline, digest = baseline_fixture()
            del baseline["cases"][0][field]
            self.rejected(m.validate_resident_baseline, budget, baseline, digest)

    def test_baseline_rejects_wrong_evidence_hash_even_for_the_unselected_profile(self):
        for index in (0, 1):
            budget, baseline, digest = baseline_fixture()
            budget["profiles"][index]["baselineEvidenceSha256"] = "0" * 64
            self.rejected(m.validate_resident_baseline, budget, baseline, digest)
        for invalid in ("", "f" * 63, "F" * 64, "secret/private", True, None, []):
            budget, baseline, _ = baseline_fixture()
            self.rejected(m.validate_resident_baseline, budget, baseline, invalid)
        for malformed in (None, [], "secret/private", True, 123):
            budget, baseline, digest = baseline_fixture()
            self.rejected(m.validate_resident_baseline, budget, malformed, digest)
            baseline["cases"] = malformed
            self.rejected(m.validate_resident_baseline, budget, baseline, digest)

    def test_results_recompute_two_clean_successes_against_frozen_budget(self):
        budget, _, _ = baseline_fixture()
        cases = results_fixture(budget)
        cases[0]["private"] = "secret/private"
        original = copy.deepcopy((cases, budget))
        result = m.validate_resident_results(cases, budget)
        self.assertTrue(result["resultsValidated"])
        self.assertTrue(result["budgetPassed"])
        self.assertEqual([p["gogcPercent"] for p in result["profiles"]], [50, 100])
        self.assertEqual([p["peakRssBytes"] for p in result["profiles"]], [150994944, 150994944])
        self.assertNotIn("secret", json.dumps(result))
        self.assertNotIn("samples", json.dumps(result))
        result["profiles"][0]["processRssBudgetBytes"] = 1
        self.assertEqual((cases, budget), original)
        self.assertNotEqual(cases[0]["sourceDigest"], budget["profiles"][0]["baselineSourceDigest"])
        self.assertTrue(m.validate_resident_results(list(reversed(cases)), budget)["budgetPassed"])

    def test_results_require_exactly_two_profiles_and_completed_controller_checks(self):
        budget, _, _ = baseline_fixture()
        cases = results_fixture(budget)
        for invalid in (None, {}, "secret/private", [], cases[:1], [cases[0], cases[0]], cases + [cases[0]]):
            self.rejected(m.validate_resident_results, invalid, budget)
        for key, invalid in (("result", "failed"), ("result", True), ("testArtifactsCleaned", False),
                             ("testArtifactsCleaned", 1), ("sourceUnchanged", False), ("sourceUnchanged", 1)):
            changed = copy.deepcopy(cases)
            changed[0][key] = invalid
            self.rejected(m.validate_resident_results, changed, budget)
        for key in ("result", "testArtifactsCleaned", "sourceUnchanged", "memoryProfile", "residentValidation"):
            changed = copy.deepcopy(cases)
            del changed[0][key]
            self.rejected(m.validate_resident_results, changed, budget)
        changed = copy.deepcopy(cases)
        changed[0]["gogcPercent"] = 50  # An optional repeated label cannot contradict the runtime.
        self.rejected(m.validate_resident_results, changed, budget)
        for invalid in (False, 1, None):
            changed = copy.deepcopy(cases)
            changed[0]["residentValidation"]["budgetPassed"] = invalid
            self.rejected(m.validate_resident_results, changed, budget)

    def test_results_reject_stale_success_flags_samples_and_frozen_budget_references(self):
        budget, _, _ = baseline_fixture()
        cases = results_fixture(budget)
        changed = copy.deepcopy(cases)
        changed[0]["memoryProfile"]["resident"]["samples"][1]["rssBytes"] = budget["profiles"][0]["processRssBudgetBytes"] + 1
        self.rejected(m.validate_resident_results, changed, budget)
        changed = copy.deepcopy(cases)
        changed[0]["memoryProfile"]["resident"]["samples"][20]["rssBytes"] += 1
        self.rejected(m.validate_resident_results, changed, budget)  # Below peak, but saved samples are stale.
        for field, value in (("peakRssBytes", 1), ("sampleCount", 307.0), ("approximate", 1)):
            changed = copy.deepcopy(cases)
            changed[0]["residentValidation"][field] = value
            self.rejected(m.validate_resident_results, changed, budget)
        for field, value in (("gogcPercent", 50), ("processRssBudgetBytes", 805306368),
                             ("baselineSourceDigest", "e" * 64), ("baselineEvidenceSha256", "e" * 64)):
            changed = copy.deepcopy(cases)
            changed[0]["residentValidation"]["budget"][field] = value
            self.rejected(m.validate_resident_results, changed, budget)
        changed_budget = copy.deepcopy(budget)
        changed_budget["profiles"][0]["processRssBudgetBytes"] += 1
        self.rejected(m.validate_resident_results, cases, changed_budget)
        changed = copy.deepcopy(cases)
        changed[0]["residentValidation"]["samples"][0]["elapsedNanos"] = False
        self.rejected(m.validate_resident_results, changed, budget)  # False must not alias integer zero.


if __name__ == "__main__":
    unittest.main()
