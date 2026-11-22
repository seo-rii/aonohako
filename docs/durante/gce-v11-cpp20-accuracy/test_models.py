"""Regression tests for the frozen v11 CPP20 accuracy evaluator."""
from fractions import Fraction
import unittest

from models import boundary, fit, score


class AccuracyTest(unittest.TestCase):
    def fit_cases(self, rows, targets):
        columns = ["instructions", "movs_bytes", "stos_bytes"]
        cases = [{"center": dict(zip(columns, map(Fraction, row), strict=True)), "target": Fraction(target)}
                 for row, target in zip(rows, targets, strict=True)]
        weights, rank = fit(cases, columns)
        return [weights[column] for column in columns], rank

    def test_full_rank_relative_fit(self):
        rows = [(2, 3, 1), (3, 4, 2), (3, 2, 7), (5, 8, 2)]
        self.assertEqual(self.fit_cases(rows, [19, 30, 48, 46]), ([6, 1, 4], 3))

    def test_rank_deficient_fit_prefers_first_supported_column(self):
        # The second column is exactly twice the first. The third was never observed.
        self.assertEqual(self.fit_cases([(1, 2, 0), (2, 4, 0), (4, 8, 0)], [4, 8, 16]),
                         ([4, 0, 0], 1))

    def test_sparse_tie_can_prefer_a_later_column(self):
        # The first two columns together tie the third column alone.
        self.assertEqual(self.fit_cases([(1, 0, 1), (0, 1, 1), (1, 1, 2)], [2, 2, 4]),
                         ([0, 0, 2], 2))

    def test_equality_and_mixed_boundary(self):
        limit = 1_000_000
        self.assertEqual(boundary([limit, limit-1], [limit, limit], 1)["classification"], "agreement")
        mixed = boundary([limit, limit+1], [limit, limit+1], 1)
        self.assertEqual(mixed["classification"], "mixed_native")
        self.assertTrue(mixed["mixed_predictions"])
        self.assertEqual(boundary([limit], [limit, limit+1], 1)["classification"], "false_tle")
        self.assertEqual(boundary([limit+1], [limit], 1)["classification"], "false_pass")

    def test_minimum_sensitivity_uses_the_same_predictions(self):
        case = {"native": [20_000_000, 25_000_000, 26_000_000], "target": Fraction(25_000_000),
                "rows": [{"instructions": 25_000_000}, {"instructions": 30_000_000}]}
        result = score(case, {"instructions": Fraction(1)})
        self.assertEqual(result["prediction_ns"], [25_000_000, 30_000_000])
        self.assertEqual(result["median"], {"within_20pct": True, "signed_error_ppm": [0, 200000]})
        self.assertEqual(result["minimum"], {"within_20pct": False, "signed_error_ppm": [250000, 500000]})
        self.assertEqual([b["limit_ms"] for b in result["boundaries"]], [20, 30])


if __name__ == "__main__":
    unittest.main()
