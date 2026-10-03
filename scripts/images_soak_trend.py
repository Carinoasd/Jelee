"""Bounded quiescent trend check for the fixed 24-hour image soak.

This checks only checkpoint trends. It does not prove runtime duration, sample
continuity, GC limits, workload completion, or cleanup; the controller must
independently verify those before accepting a run.
"""

MIB = 1 << 20
ROUND_NANOS = 300 * 1_000_000_000
ROUNDS = 288
RSS_LIMIT = 464 * MIB
U64_MAX = (1 << 64) - 1


class Rejected(ValueError):
    """Safe fixed code; never include source values."""


def _need(condition):
    if not condition:
        raise Rejected("images_soak_trend_invalid")


def _uint(value):
    _need(type(value) is int and 0 <= value <= U64_MAX)
    return value


def _median_twice(values):
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return 2 * ordered[middle]
    return ordered[middle - 1] + ordered[middle]


def validate_quiescent_trend(checkpoints, work_started_nanos):
    """Validate exactly 288 ordered, independently observed checkpoints.

    Input is a bounded projection with round, elapsedNanos, heapBytes, rssBytes,
    and goroutines. Medians are stored multiplied by two to retain half-byte
    precision. Goroutine counts are reported without inventing a hard cap.
    """
    start = _uint(work_started_nanos)
    _need(start <= U64_MAX - ROUNDS * ROUND_NANOS)
    _need(type(checkpoints) is list and len(checkpoints) == ROUNDS)
    keys = {"round", "elapsedNanos", "heapBytes", "rssBytes", "goroutines"}
    hours = []
    for hour in range(24):
        block = checkpoints[hour * 12:(hour + 1) * 12]
        for offset, point in enumerate(block):
            _need(type(point) is dict and point.keys() == keys)
            for value in point.values():
                _uint(value)
            index = hour * 12 + offset
            _need(point["round"] == index)
            _need(start + index * ROUND_NANOS <= point["elapsedNanos"]
                  < start + (index + 1) * ROUND_NANOS)
            _need(0 < point["rssBytes"] <= RSS_LIMIT)
            _need(point["goroutines"] > 0)
        hours.append({
            "index": hour,
            "heapMedianTwiceBytes": _median_twice([p["heapBytes"] for p in block]),
            "rssMedianTwiceBytes": _median_twice([p["rssBytes"] for p in block]),
            "goroutinesMin": min(p["goroutines"] for p in block),
            "goroutinesMax": max(p["goroutines"] for p in block),
        })
    result = {"hours": hours}
    for metric, tolerance in (("heap", 32 * MIB), ("rss", 64 * MIB)):
        medians = [h[metric + "MedianTwiceBytes"] for h in hours]
        # Both sets have three members; their median stays in twice-byte units.
        reference = sorted(medians[1:4])[1]
        final = sorted(medians[-3:])[1]
        span = max(medians[1:]) - min(medians[1:])
        _need(final - reference <= 2 * tolerance and span <= 2 * tolerance)
        _need(not all(right - left >= 2 * MIB
                      for left, right in zip(medians[-6:-1], medians[-5:])))
        result[metric] = {"referenceMedianTwiceBytes": reference,
                          "finalMedianTwiceBytes": final,
                          "spanTwiceBytes": span}
    return result
