"""Validate bounded worker-process RSS samples and a separately frozen budget.

The samples-only result is evidence for choosing a baseline, not a budget pass.
The caller must also validate the real workload, KDF, container limits, OOM
events and successful shutdown through the existing memory acceptance checks.
No I/O or environment lookup is performed here. Input text is never returned.
"""

INVALID_EVIDENCE = "resident_memory_evidence_invalid"
PHASES = ("startup", "cold", "warm", "changed", "sustained", "cancellation", "shutdown", "stopped")
_MAX_UINT64 = (1 << 64) - 1
_SECOND_NS = 1_000_000_000
_MILLISECOND_NS = 1_000_000
_MAX_GAP_NS = 5 * _SECOND_NS
_MAX_SAMPLES = 1024
_GO_MEMORY_LIMIT_BYTES = 512 * 1024 * 1024
_SAMPLE_NUMBERS = ("elapsedNanos", "rssBytes", "heapBytes", "totalAllocBytes",
                   "numGC", "pauseTotalNs", "goroutines")
_COUNTERS = ("totalAllocBytes", "numGC", "pauseTotalNs")


class Rejected(ValueError):
    """A fixed error code that never contains evidence or arbitrary text."""


def _need(condition):
    if not condition:
        raise Rejected(INVALID_EVIDENCE)


def _object(value):
    _need(type(value) is dict)
    return value


def _integer(value, expected=None):
    _need(type(value) is int and 0 <= value <= _MAX_UINT64)
    if expected is not None:
        _need(value == expected)
    return value


def _sha256(value):
    _need(type(value) is str and len(value) == 64 and all(c in "0123456789abcdef" for c in value))
    return value


def validate_resident_samples(profile):
    """Return an owned, safe projection of complete, ordered RSS evidence.

    Every phase is mandatory. The sustained interval ends at the cancellation
    transition, rather than at the previous periodic sample, so an ordinary
    one-second ticker does not shorten a completed five-minute workload.
    """
    profile = _object(profile)
    _integer(profile.get("version"), 1)
    elapsed_ms = _integer(profile.get("elapsedMillis"))
    _need(elapsed_ms > 0)
    resident = _object(profile.get("resident"))
    _integer(resident.get("version"), 1)
    _need(resident.get("scope") == "worker-process")
    _need(resident.get("rssSource") == "/proc/self/statm")
    _need(resident.get("approximate") is True)
    _integer(resident.get("sampleEveryMillis"), 1000)
    _integer(resident.get("maxSamples"), _MAX_SAMPLES)
    _need(resident.get("complete") is True)
    source = resident.get("samples")
    _need(type(source) is list and len(PHASES) <= len(source) <= _MAX_SAMPLES)

    samples, phases = [], []
    previous, phase_index = None, -1
    peak_rss, peak_heap = 0, 0
    for raw in source:
        raw = _object(raw)
        phase = raw.get("phase")
        _need(type(phase) is str and phase in PHASES)
        index = PHASES.index(phase)
        _need(index == phase_index or index == phase_index + 1)
        sample = {key: _integer(raw.get(key)) for key in _SAMPLE_NUMBERS}
        _need(sample["rssBytes"] > 0 and sample["goroutines"] > 0)
        sample["phase"] = phase
        at = sample["elapsedNanos"]
        if previous is None:
            _need(at <= _SECOND_NS)
        else:
            _need(0 < at - previous["elapsedNanos"] <= _MAX_GAP_NS)
            _need(all(sample[key] >= previous[key] for key in _COUNTERS))
        if index != phase_index:
            phases.append({"phase": phase, "sampleCount": 0, "firstElapsedNanos": at,
                           "lastElapsedNanos": at, "peakRssBytes": 0, "peakHeapBytes": 0})
            phase_index = index
        summary = phases[-1]
        summary["sampleCount"] += 1
        summary["lastElapsedNanos"] = at
        summary["peakRssBytes"] = max(summary["peakRssBytes"], sample["rssBytes"])
        summary["peakHeapBytes"] = max(summary["peakHeapBytes"], sample["heapBytes"])
        peak_rss = max(peak_rss, sample["rssBytes"])
        peak_heap = max(peak_heap, sample["heapBytes"])
        samples.append(sample)
        previous = sample

    _need(phase_index == len(PHASES) - 1)
    elapsed_ns = elapsed_ms * _MILLISECOND_NS
    last_ns = previous["elapsedNanos"]
    _need(elapsed_ns - _MAX_GAP_NS <= last_ns <= elapsed_ns + _MILLISECOND_NS)
    sustained_ns = phases[5]["firstElapsedNanos"] - phases[4]["firstElapsedNanos"]
    _need(sustained_ns >= 300 * _SECOND_NS)
    return {"samplesValidated": True, "version": 1, "scope": "worker-process",
            "rssSource": "/proc/self/statm", "approximate": True,
            "sampleEveryMillis": 1000, "maxSamples": _MAX_SAMPLES, "complete": True,
            "sampleCount": len(samples), "peakRssBytes": peak_rss, "peakHeapBytes": peak_heap,
            "sustainedNanos": sustained_ns, "phases": phases, "samples": samples}


def _budget_profiles(budget):
    budget = _object(budget)
    _integer(budget.get("version"), 1)
    _need(budget.get("scope") == "worker-process")
    _need(budget.get("rssSource") == "/proc/self/statm")
    _need(budget.get("workload") == "mixed-1000-default-kdf-v1")
    _integer(budget.get("sampleEveryMillis"), 1000)
    entries = budget.get("profiles")
    _need(type(entries) is list and len(entries) == 2)
    result = {}
    for raw in entries:
        raw = _object(raw)
        gogc = _integer(raw.get("gogcPercent"))
        _need(gogc in (50, 100) and gogc not in result)
        soft = _integer(raw.get("goMemoryLimitBytes"), _GO_MEMORY_LIMIT_BYTES)
        limit = _integer(raw.get("processRssBudgetBytes"))
        baseline = _integer(raw.get("baselinePeakBytes"))
        _need(0 < baseline <= limit)
        result[gogc] = {"gogcPercent": gogc, "goMemoryLimitBytes": soft,
                        "processRssBudgetBytes": limit, "baselinePeakBytes": baseline,
                        "baselineSourceDigest": _sha256(raw.get("baselineSourceDigest")),
                        "baselineEvidenceSha256": _sha256(raw.get("baselineEvidenceSha256"))}
    return result


def validate_resident_budget(profile, budget, expected_gogc):
    """Apply an explicit budget; missing or excessive evidence always fails.

    Baseline digests identify historical evidence. Future source versions are
    intentionally allowed to differ; a benchmark must detect their regressions.
    The Go soft limit and cgroup hard limit do not define the RSS budget.
    """
    _need(type(expected_gogc) is int and expected_gogc in (50, 100))
    evidence = validate_resident_samples(profile)
    runtime = _object(profile.get("runtime"))
    _integer(runtime.get("gogcPercent"), expected_gogc)
    _integer(runtime.get("goMemoryLimitBytes"), _GO_MEMORY_LIMIT_BYTES)
    selected = _budget_profiles(budget)[expected_gogc]
    _need(evidence["peakRssBytes"] <= selected["processRssBudgetBytes"])
    return {**evidence, "budgetPassed": True, "budget": selected}


def validate_resident_baseline(budget, baseline, evidence_sha256):
    """Verify two measured baseline profiles against their frozen references.

    The caller must compute evidence_sha256 from the actual baseline file bytes
    it parsed, rather than trusting a digest declared inside that document.
    This validates the RSS baseline; workload and container evidence remain
    separate acceptance requirements.
    """
    digest = _sha256(evidence_sha256)
    profiles = _budget_profiles(budget)
    _need(all(profile["baselineEvidenceSha256"] == digest for profile in profiles.values()))
    baseline = _object(baseline)
    _integer(baseline.get("version"), 1)
    cases = baseline.get("cases")
    _need(type(cases) is list and len(cases) == 2)
    validated = {}
    for case in cases:
        case = _object(case)
        gogc = _integer(case.get("gogcPercent"))
        _need(gogc in (50, 100) and gogc not in validated)
        source = _sha256(case.get("sourceDigest"))
        selected = profiles[gogc]
        _need(source == selected["baselineSourceDigest"])
        result = validate_resident_budget(case.get("memoryProfile"), budget, gogc)
        _need(result["peakRssBytes"] == selected["baselinePeakBytes"])
        validated[gogc] = {"gogcPercent": gogc, "sourceDigest": source,
                           "peakRssBytes": result["peakRssBytes"], "sampleCount": result["sampleCount"]}
    return {"baselineValidated": True, "version": 1, "scope": "worker-process",
            "rssSource": "/proc/self/statm", "approximate": True,
            "evidenceSha256": digest, "profiles": [validated[gogc] for gogc in (50, 100)]}


def _same_evidence(actual, expected):
    """Compare a bounded validated shape without accepting bool/int aliases."""
    if type(actual) is not type(expected):
        return False
    if type(expected) is dict:
        return actual.keys() == expected.keys() and all(_same_evidence(actual[key], value) for key, value in expected.items())
    if type(expected) is list:
        return len(actual) == len(expected) and all(_same_evidence(a, e) for a, e in zip(actual, expected))
    return actual == expected


def validate_resident_results(cases, budget):
    """Recompute both successful runs against the current frozen budget.

    A previously saved success flag or summary cannot authorize changed raw
    samples. The controller must have finished cleanup and source verification
    before this aggregate check can pass.
    """
    _need(type(cases) is list and len(cases) == 2)
    _budget_profiles(budget)
    validated = {}
    for case in cases:
        case = _object(case)
        _need(case.get("result") == "passed")
        _need(case.get("testArtifactsCleaned") is True and case.get("sourceUnchanged") is True)
        profile = _object(case.get("memoryProfile"))
        runtime = _object(profile.get("runtime"))
        gogc = _integer(runtime.get("gogcPercent"))
        _need(gogc in (50, 100) and gogc not in validated)
        if "gogcPercent" in case:
            _integer(case["gogcPercent"], gogc)
        recorded = _object(case.get("residentValidation"))
        _need(recorded.get("budgetPassed") is True)
        result = validate_resident_budget(profile, budget, gogc)
        _need(_same_evidence(recorded, result))
        selected = result["budget"]
        validated[gogc] = {"gogcPercent": gogc, "peakRssBytes": result["peakRssBytes"],
                           "sampleCount": result["sampleCount"],
                           "processRssBudgetBytes": selected["processRssBudgetBytes"],
                           "baselineSourceDigest": selected["baselineSourceDigest"],
                           "baselineEvidenceSha256": selected["baselineEvidenceSha256"]}
    return {"resultsValidated": True, "budgetPassed": True, "scope": "worker-process",
            "rssSource": "/proc/self/statm", "approximate": True,
            "profiles": [validated[gogc] for gogc in (50, 100)]}
