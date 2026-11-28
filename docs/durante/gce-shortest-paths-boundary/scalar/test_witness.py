from fractions import Fraction
import unittest

import witness


class ScalarTests(unittest.TestCase):
    def test_most_favorable_endpoint_for_each_native_verdict(self):
        self.assertEqual(witness.constraint(1, "Accepted", [11, 20]), ("upper_inclusive", Fraction(1_000_000, 11)))
        self.assertEqual(witness.constraint(1, "Time Limit Exceeded", [11, 20]),
                         ("lower_exclusive", Fraction(1_000_000, 20)))

    def test_exact_equality_cannot_satisfy_strict_timeout(self):
        def side(kind, num):
            return {"constraint": kind, "cell": "control", "ns_per_work": {"numerator": num, "denominator": 7}}
        lower = side("lower_exclusive", 2)
        self.assertFalse(witness.compare(lower, side("upper_inclusive", 2))["compatible"])
        self.assertTrue(witness.compare(lower, side("upper_inclusive", 3))["compatible"])

    def test_both_historical_replays_and_distinct_inputs(self):
        document = witness.run()
        self.assertEqual(document, witness.read(witness.HERE / "results.json"))
        for result in document["engines"].values():
            upper = result["upper"]
            self.assertEqual(upper["native_repetitions"], 6)
            for key, row in result["comparisons"].items():
                self.assertFalse(row["compatible"])
                self.assertEqual(row["lower"]["native_repetitions"], 6)
                self.assertEqual(row["lower"]["input_sha256"] == upper["input_sha256"], key == "same_input")


if __name__ == "__main__":
    unittest.main()
