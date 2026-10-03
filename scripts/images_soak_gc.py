"""Fixed hourly and whole-work GC checks; no runtime or workload claim."""

from scan_memory_acceptance import Rejected as GCRejected, validate_gc_interval

HOUR_NS = 3600 * 1_000_000_000
BUDGET = {"maxElapsedNanos": 25 * HOUR_NS, "maxPauseUpperNanos": 50_000_000,
          "pauseRatioNumerator": 1, "pauseRatioDenominator": 100,
          "gcMetric": "/sched/pauses/total/gc:seconds"}


class Rejected(ValueError):
    """Fixed code; raw exceptions never include private input."""


def _need(condition):
    if not condition:
        raise Rejected("images_soak_gc_invalid")


def _uint(value):
    _need(type(value) is int and 0 <= value < (1 << 64))
    return value


def validate_hourly_gc(boundaries, work_started_nanos, work_finished_nanos, elapsed_nanos):
    """Require 25 shared boundaries for exactly 24 contiguous hourly intervals.

    Readers may take at most five seconds after each scheduled boundary. Each
    ratio uses the underlying validator's shortest counter/histogram interval;
    scheduling tolerance never increases its denominator.
    """
    start, finish, elapsed = map(_uint, (work_started_nanos, work_finished_nanos, elapsed_nanos))
    _need(start + 24 * HOUR_NS <= finish <= elapsed <= BUDGET["maxElapsedNanos"])
    _need(type(boundaries) is list and len(boundaries) == 25)
    for index, boundary in enumerate(boundaries):
        _need(type(boundary) is dict)
        scheduled = start + index * HOUR_NS
        for key in ("countersStartedNanos", "countersFinishedNanos", "histogramStartedNanos", "histogramFinishedNanos"):
            _need(key in boundary)
            value = _uint(boundary[key])
            _need(scheduled <= value <= scheduled + 5_000_000_000)
        _need(boundary["histogramFinishedNanos"] <= finish)
    try:
        hours = [validate_gc_interval(before, after, elapsed, BUDGET)
                 for before, after in zip(boundaries, boundaries[1:])]
        whole = validate_gc_interval(boundaries[0], boundaries[-1], elapsed, BUDGET)
    except GCRejected:
        raise Rejected("images_soak_gc_invalid") from None
    return {"hours": hours, "wholeWork": whole}
