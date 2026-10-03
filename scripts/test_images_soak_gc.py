"""Synthetic GC boundaries only; does not run a 24-hour workload."""

import unittest

import images_soak_gc as m


def boundaries():
    return [{"counters": {"totalAllocBytes": 1000 + i * 1000000, "numGC": i * 100,
                          "pauseTotalNs": i * 1000000},
             "countersStartedNanos": i * m.HOUR_NS + 10,
             "countersFinishedNanos": i * m.HOUR_NS + 20,
             "histogramStartedNanos": i * m.HOUR_NS + 30,
             "histogramFinishedNanos": i * m.HOUR_NS + 40,
             "histogram": {"metric": "/sched/pauses/total/gc:seconds",
                           "boundsNanos": ["-Inf", 0, 1000, 20000, "+Inf"],
                           "counts": [0, i * 100, 0, 0]}} for i in range(25)]


class HourlyGCTests(unittest.TestCase):
    def validate(self, value):
        return m.validate_hourly_gc(value, 0, 24 * m.HOUR_NS + 40, 24 * m.HOUR_NS + 100)

    def test_whole_and_each_hour(self):
        result = self.validate(boundaries())
        self.assertEqual(len(result["hours"]), 24)
        self.assertEqual(result["wholeWork"]["observedPauses"], 2400)
        self.assertTrue(all(h["observedPauses"] == 100 for h in result["hours"]))

    def test_hourly_spike_not_hidden_by_day_average(self):
        value = boundaries()
        for boundary in value[12:]:
            boundary["counters"]["pauseTotalNs"] += 37 * 1_000_000_000
        # The whole-day average passes, but the affected hour exceeds one percent.
        m.validate_gc_interval(value[0], value[-1], 24 * m.HOUR_NS + 100, m.BUDGET)
        with self.assertRaises(m.Rejected):
            self.validate(value)

    def test_missing_duplicate_reordered(self):
        for value in (boundaries()[:-1], boundaries() + [boundaries()[0]],
                      list(reversed(boundaries())), [boundaries()[0]] * 25):
            with self.assertRaises(m.Rejected):
                self.validate(value)

    def test_delayed_or_early_boundary(self):
        for delta in (-100, 5_000_000_001):
            value = boundaries()
            value[2]["countersStartedNanos"] += delta
            with self.assertRaises(m.Rejected):
                self.validate(value)

    def test_counter_regression_and_infinite_pause(self):
        for corrupt in ("counter", "histogram"):
            value = boundaries()
            if corrupt == "counter":
                value[10]["counters"]["numGC"] = 0
            else:
                for boundary in value[10:]:
                    boundary["histogram"]["counts"][-1] = 1
            with self.assertRaisesRegex(m.Rejected, "^images_soak_gc_invalid$"):
                self.validate(value)

    def test_work_duration_and_types(self):
        for start, finish, elapsed in ((True, 24*m.HOUR_NS+40, 24*m.HOUR_NS+100),
                                       (0, 23*m.HOUR_NS, 24*m.HOUR_NS),
                                       (0, 24*m.HOUR_NS+40, 24*m.HOUR_NS),
                                       (0, 24*m.HOUR_NS+40, 26*m.HOUR_NS)):
            with self.assertRaises(m.Rejected):
                m.validate_hourly_gc(boundaries(), start, finish, elapsed)


if __name__ == "__main__":
    unittest.main()
