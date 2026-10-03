"""Synthetic rejection tests, never evidence of a real 24-hour run."""

import unittest

import images_soak_trend as m


def fixture():
    return [{"round": i, "elapsedNanos": i * m.ROUND_NANOS + 1,
             "heapBytes": 128 * m.MIB, "rssBytes": 256 * m.MIB,
             "goroutines": 30} for i in range(m.ROUNDS)]


class TrendTests(unittest.TestCase):
    def test_stable(self):
        result = m.validate_quiescent_trend(fixture(), 0)
        self.assertEqual(len(result["hours"]), 24)
        self.assertEqual(result["heap"]["spanTwiceBytes"], 0)

    def test_half_byte_median(self):
        points = fixture()
        for i, point in enumerate(points):
            point["heapBytes"] += i % 2
        result = m.validate_quiescent_trend(points, 0)
        self.assertEqual(result["heap"]["referenceMedianTwiceBytes"], 256 * m.MIB + 1)

    def test_reject_bad_checkpoint(self):
        for key, value in (("round", 1), ("elapsedNanos", m.ROUND_NANOS),
                           ("rssBytes", m.RSS_LIMIT + 1), ("goroutines", 0),
                           ("heapBytes", True), ("heapBytes", -1),
                           ("heapBytes", float("nan")), ("heapBytes", 1 << 64)):
            with self.subTest(key=key, value=value):
                points = fixture()
                points[0][key] = value
                with self.assertRaisesRegex(m.Rejected, "^images_soak_trend_invalid$"):
                    m.validate_quiescent_trend(points, 0)

    def test_missing_duplicate_extra_and_order(self):
        for points in (fixture()[:-1], fixture() + [fixture()[0]],
                       list(reversed(fixture())), [fixture()[0]] * 288):
            with self.assertRaises(m.Rejected):
                m.validate_quiescent_trend(points, 0)

    def test_unknown_and_missing_keys(self):
        for extra in (True, False):
            points = fixture()
            if extra:
                points[0]["private"] = "must not appear in error"
            else:
                del points[0]["heapBytes"]
            with self.assertRaisesRegex(m.Rejected, "^images_soak_trend_invalid$"):
                m.validate_quiescent_trend(points, 0)

    def test_first_hour_excluded_only_from_trend(self):
        points = fixture()
        for point in points[:12]:
            point["heapBytes"] = 400 * m.MIB
        m.validate_quiescent_trend(points, 0)
        points[0]["rssBytes"] = m.RSS_LIMIT + 1
        with self.assertRaises(m.Rejected):
            m.validate_quiescent_trend(points, 0)

    def test_tolerance_boundary_and_transient_growth(self):
        for metric, limit in (("heapBytes", 32), ("rssBytes", 64)):
            for hour in (10, 21):
                points = fixture()
                for point in points[hour * 12:(hour + 1) * 12]:
                    point[metric] += limit * m.MIB
                m.validate_quiescent_trend(points, 0)
                for point in points[hour * 12:(hour + 1) * 12]:
                    point[metric] += 1
                with self.assertRaises(m.Rejected):
                    m.validate_quiescent_trend(points, 0)

    def test_monotonic_growth_inside_absolute_budget(self):
        for metric in ("heapBytes", "rssBytes"):
            points = fixture()
            for hour in range(18, 24):
                for point in points[hour * 12:(hour + 1) * 12]:
                    point[metric] += (hour - 18) * m.MIB
            with self.assertRaises(m.Rejected):
                m.validate_quiescent_trend(points, 0)
            # A plateau interrupts the prescribed five consecutive increases.
            for point in points[-12:]:
                point[metric] -= m.MIB
            m.validate_quiescent_trend(points, 0)

    def test_start_offset_and_overflow(self):
        points = fixture()
        for point in points:
            point["elapsedNanos"] += 123
        m.validate_quiescent_trend(points, 123)
        for start in (True, -1, m.U64_MAX):
            with self.assertRaises(m.Rejected):
                m.validate_quiescent_trend(points, start)


if __name__ == "__main__":
    unittest.main()
