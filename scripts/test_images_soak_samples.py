"""Synthetic stream checks; no measured long-run evidence."""

import unittest

import images_soak_samples as m


def block(start=0, count=60):
    return {"firstSampleIndex": start,
            "samples": [{"elapsedNanos": i * 1_000_000_000, "phase": "cold",
                         "rssBytes": 200 << 20, "heapBytes": 100 << 20,
                         "goroutines": 20, "totalAllocBytes": i * 100,
                         "numGC": i, "pauseTotalNs": i}
                        for i in range(start, start + count)],
            "processorMaxima": {**m.LIMITS, "observations": count}}


class SampleTests(unittest.TestCase):
    def test_full_day_constant_retained_state(self):
        state = m.SampleBlocks()
        for start in range(0, 86400, 60):
            state.add(block(start), (start + 59) * 1_000_000_000)
        report = state.summary()
        self.assertEqual(report["sampleCount"], 86400)
        self.assertEqual(report["blockCount"], 1440)
        self.assertFalse(any(isinstance(v, list) for v in vars(state).values()))
        report["processorMaxima"]["maxActive"] = 999
        self.assertEqual(state.summary()["processorMaxima"]["maxActive"], 2)

    def test_partial_flush(self):
        state = m.SampleBlocks()
        state.add(block(0, 1), 0)
        state.add(block(1, 2), 2_000_000_000)
        self.assertEqual(state.summary()["sampleCount"], 3)

    def test_failed_block_latches(self):
        state = m.SampleBlocks()
        bad = block(0, 2)
        bad["samples"][1]["rssBytes"] = (464 << 20) + 1
        with self.assertRaises(m.Rejected):
            state.add(bad, 1_000_000_000)
        for operation in (lambda: state.add(block(), 59_000_000_000), state.summary):
            with self.assertRaises(m.Rejected):
                operation()

    def test_bad_samples(self):
        for key, value in (("phase", []), ("phase", "unknown"), ("rssBytes", 0),
                           ("heapBytes", True), ("numGC", 1 << 32),
                           ("elapsedNanos", 2_000_000_000), ("goroutines", -1)):
            with self.subTest(key=key):
                bad = block(0, 1)
                bad["samples"][0][key] = value
                with self.assertRaises(m.Rejected):
                    m.SampleBlocks().add(bad, 2_000_000_000)

    def test_missing_duplicate_gap_and_counter_regression(self):
        for change in ("index", "duplicate", "gap", *m.COUNTERS):
            state = m.SampleBlocks()
            state.add(block(), 59_000_000_000)
            bad = block(60, 1)
            if change == "index":
                bad["firstSampleIndex"] = 61
            elif change == "duplicate":
                bad["samples"][0]["elapsedNanos"] = 59_000_000_000
            elif change == "gap":
                bad["samples"][0]["elapsedNanos"] = 65_000_000_000
            else:
                bad["samples"][0][change] = 0
            with self.assertRaises(m.Rejected):
                state.add(bad, 65_000_000_000)

    def test_limits(self):
        for key in m.LIMITS:
            bad = block(0, 1)
            bad["processorMaxima"][key] += 1
            with self.assertRaises(m.Rejected):
                m.SampleBlocks().add(bad, 0)
        for count in (0, 61):
            with self.assertRaises(m.Rejected):
                m.SampleBlocks().add(block(0, count), 60_000_000_000)

    def test_unobserved_or_stale(self):
        with self.assertRaises(m.Rejected):
            m.SampleBlocks().summary()
        with self.assertRaises(m.Rejected):
            m.SampleBlocks().add(block(0, 1), 5_000_000_001)


if __name__ == "__main__":
    unittest.main()
