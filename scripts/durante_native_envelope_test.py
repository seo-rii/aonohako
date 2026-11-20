from fractions import Fraction
import unittest

from durante_native_envelope import diagnose, pair_bound


def case(native, features):
    return {"native": native, "features": features}


class EnvelopeTests(unittest.TestCase):
    def test_ratio_uses_most_favorable_extremes_and_ceiling(self):
        slow = case([90, 100], [(1, 0, 2), (2, 0, 2)])
        fast = case([8, 10], [(1, 0, 1), (1, 0, 3)])
        bound, proof = pair_bound(slow, fast)
        self.assertEqual(bound, Fraction(69, 110))
        self.assertEqual(proof["max_feature_ratio"], "2")
        self.assertEqual(proof["minimum_possible_worst_error_ppm"], 627272)

    def test_unbounded_feature_and_overlapping_native_times(self):
        slow = case([18, 21], [(2, 5, 2)])
        self.assertIsNone(pair_bound(slow, case([10, 11], [(1, 0, 1)])))
        self.assertIsNone(pair_bound(slow, case([18, 19], [(2, 5, 2)])))

    def test_strict_twenty_percent_margin(self):
        rows = []
        for value in (79, 80, 120, 121):
            rows.append({"workload_id": str(value), "scale": 1, "native_before": {"cpu_time_ns": 100},
                         "native_after": {"cpu_time_ns": 100}, "metered": {"work_measurement": {
                             "instructions": value, "rep_breakdown": {"movs_bytes": 0, "stos_bytes": 0}}}})
        edges = diagnose(rows, (1, 1, 1, 1))["outside_native_range_with_20pct_margin"]
        self.assertEqual({row["workload"]: row["direction"] for row in edges}, {"79": "under", "121": "over"})


if __name__ == "__main__":
    unittest.main()
