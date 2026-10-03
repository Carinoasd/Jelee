"""Constant-space validation of soak sample blocks, not whole-run acceptance."""

PHASES = frozenset(("startup", "login", "scan", "cold", "warm", "idle", "rotate",
                    "negative", "cancellation", "shutdown", "stopped"))
COUNTERS = ("totalAllocBytes", "numGC", "pauseTotalNs")
NUMBERS = ("elapsedNanos", "rssBytes", "heapBytes", "goroutines", *COUNTERS)
LIMITS = {"maxActive": 2, "maxReservedBytes": 192 << 20,
          "maxEstimatedImageBytes": 96 << 20, "maxCacheEntries": 128,
          "maxCacheBytes": 32 << 20}


class Rejected(ValueError):
    """Fixed safe error without source data."""


def _need(condition):
    if not condition:
        raise Rejected("images_soak_samples_invalid")


def _uint(value):
    _need(type(value) is int and 0 <= value < (1 << 64))
    return value


class SampleBlocks:
    """Keep only the last sample and aggregate maxima; latch any failure.

    The caller must separately verify event identity/sequence, durations, GC
    boundaries and workload results. This object never returns a passed flag.
    """

    def __init__(self):
        self.count = 0
        self.blocks = 0
        self.last = None
        self.first_nanos = None
        self.maxima = dict.fromkeys(LIMITS, 0)
        self.rss_peak = 0
        self.heap_peak = 0
        self.failed = False

    def add(self, block, event_nanos):
        try:
            _need(not self.failed)
            self._add(block, event_nanos)
        except Rejected:
            self.failed = True
            raise

    def _add(self, block, event_nanos):
        _uint(event_nanos)
        _need(type(block) is dict and block.keys() ==
              {"firstSampleIndex", "samples", "processorMaxima"})
        _need(_uint(block["firstSampleIndex"]) == self.count)
        samples = block["samples"]
        _need(type(samples) is list and 1 <= len(samples) <= 60)
        _need(self.count + len(samples) <= 90000)
        maxima = block["processorMaxima"]
        _need(type(maxima) is dict and maxima.keys() == LIMITS.keys() | {"observations"})
        _need(_uint(maxima["observations"]) == len(samples))
        for key, limit in LIMITS.items():
            _need(_uint(maxima[key]) <= limit)
        for sample in samples:
            _need(type(sample) is dict and sample.keys() == set(NUMBERS) | {"phase"})
            for key in NUMBERS:
                _uint(sample[key])
            _need(type(sample["phase"]) is str and sample["phase"] in PHASES)
            elapsed = sample["elapsedNanos"]
            _need(elapsed <= event_nanos <= 25 * 3600 * 1_000_000_000)
            _need(0 < sample["rssBytes"] <= 464 << 20 and sample["goroutines"] > 0)
            _need(sample["numGC"] < (1 << 32))
            if self.last is None:
                _need(elapsed <= 1_000_000_000)
                self.first_nanos = elapsed
            else:
                _need(0 < elapsed - self.last["elapsedNanos"] <= 5_000_000_000)
                for key in COUNTERS:
                    _need(sample[key] >= self.last[key])
            self.last = dict(sample)
            self.count += 1
            self.rss_peak = max(self.rss_peak, sample["rssBytes"])
            self.heap_peak = max(self.heap_peak, sample["heapBytes"])
        _need(event_nanos - self.last["elapsedNanos"] <= 5_000_000_000)
        for key in LIMITS:
            self.maxima[key] = max(self.maxima[key], maxima[key])
        self.blocks += 1

    def summary(self):
        _need(not self.failed and self.last is not None)
        return {"sampleCount": self.count, "blockCount": self.blocks,
                "firstSampleNanos": self.first_nanos,
                "lastSampleNanos": self.last["elapsedNanos"],
                "rssPeakBytes": self.rss_peak, "heapPeakBytes": self.heap_peak,
                "processorMaxima": dict(self.maxima)}
