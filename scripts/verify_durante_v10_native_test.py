import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from verify_durante_v10_native import estimate, median_ns, score_case, spread, summarize_language

GATES = {"min_native_ns": 20_000_000, "max_native_spread_ppm": 100_000,
         "max_work_spread_ppm": 1_000, "max_error_ppm": 200_000}
CLOCK = {"method": "weighted-rep-operations-nanoseconds-v2", "instruction_ns_numerator": 1,
         "movs_byte_ns_numerator": 2, "stos_byte_ns_numerator": 3, "ns_denominator": 4}


class NativeComparisonTests(unittest.TestCase):
    def test_typed_weights_round_once(self):
        self.assertEqual(estimate(CLOCK, (3, 3, 1)), 3)
        self.assertEqual(estimate(CLOCK, (3, 1, 3)), 4)
        aggregate = {"instruction_ns_numerator": 1, "rep_byte_ns_numerator": 3, "ns_denominator": 4}
        self.assertEqual(estimate(aggregate, (3, 4, 0)), estimate(aggregate, (3, 0, 4)))

    def test_native_spread_and_prediction_error_are_independent(self):
        work = [56_000_000, 0, 0]
        rows = [([20_000_000, 25_000_000], work)] * 3
        case = score_case(rows, CLOCK, GATES)
        self.assertEqual(case["native_median_ns"], 22_500_000)
        self.assertEqual(case["worst_signed_error_ns"], -8_500_000)
        self.assertEqual(case["failed_gates"], ["native_spread", "prediction_error"])

    def test_inclusive_gates_and_zero_spread(self):
        self.assertEqual(median_ns([20_000_001, 20_000_000]), 20_000_001)
        self.assertEqual(spread([0, 0, 0]), 0)
        self.assertEqual(spread([0, 0, 1]), (1 << 64) - 1)
        case = score_case([([20_000_000, 20_000_000], [96_000_000, 0, 0])] * 3, CLOCK, GATES)
        self.assertEqual(case["failed_gates"], [])

    def test_collection_failure_is_visible_and_cannot_be_successful(self):
        with TemporaryDirectory() as tmp:
            path = Path(tmp)
            failure = {"phase": "collect", "exit_code": 1, "reason": "compiler missing"}
            (path / "failure.json").write_text(json.dumps(failure))
            result = summarize_language(path, "RUST2024", {}, {}, None)
            self.assertEqual(result, {"status": "collection_failed", "failure": failure})
            (path / "evaluation.json").write_text("{}")
            with self.assertRaisesRegex(RuntimeError, "incomplete"):
                summarize_language(path, "RUST2024", {}, {}, None)


if __name__ == "__main__":
    unittest.main()
