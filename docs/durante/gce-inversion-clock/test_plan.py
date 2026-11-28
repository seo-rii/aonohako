import gzip
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from fractions import Fraction

import plan


class InversionPlanTest(unittest.TestCase):
    def test_preregistered_inputs_and_calibration(self):
        registered = json.loads((plan.HERE / "plan.json").read_bytes())
        self.assertEqual(plan.build_registration(), registered)
        cells = registered["cases"]
        self.assertEqual(len(cells), 16)
        prospective = [c for c in cells.values() if c["split"] == "new"]
        self.assertEqual(len(prospective), 8)
        self.assertEqual({c["role"] for c in prospective}, {"bottom_up", "reverse_bit", "fast", "wrong"})
        self.assertTrue(all(c["limits"] == {"ample": 30000} for c in prospective))
        self.assertTrue({c["stdin"] for c in prospective}.isdisjoint(
            {c["stdin"] for c in cells.values() if c["split"] != "new"}))
        self.assertEqual(registered["calibration"]["candidate"], {"numerator": 3991625, "denominator": 23464301})

    def test_calibration_requires_complete_independent_samples(self):
        archive = plan.PILOT / "evidence/primary/trials.jsonl.gz"
        with gzip.open(archive) as source:
            rows = [json.loads(line) for line in source]
        selected = [r for r in rows if r["problem"] == "inversions" and
                    r["boundary"] == "contest" and r["role"] in ("fast", "wrong")]
        self.assertEqual(len(selected), 10)
        self.assertEqual(plan.calibration(rows), plan.calibration(selected))
        for invalid in (selected[:-1], selected + [selected[0]]):
            with self.assertRaises(ValueError):
                plan.calibration(invalid)
        judge = next(r for r in selected if r["kind"] == "judge")
        judge["receipt"]["work_measurement"]["complete"] = False
        with self.assertRaises(ValueError):
            plan.calibration(selected)

    def test_brackets_use_exact_rounding_and_five_positive_times(self):
        self.assertEqual(plan.bracket([25_000_001, 1, 50_000_001, 2, 50_000_002]), {"low_ms": 20, "high_ms": 31})
        self.assertEqual(plan.bracket([25_000_000] * 5), {"low_ms": 20, "high_ms": 30})
        for sample in ([5_000_000] * 4, [True] * 5, [0] * 5, [25_000_000_000] * 5):
            with self.assertRaises(ValueError):
                plan.bracket(sample)

    def test_retrofit_on_prior_v13_completions_is_separate_from_training(self):
        # Use only archived *complete* work to infer bracket outcomes. The
        # actual candidate in-flight decision is measured on the new guests.
        cases = json.loads((plan.HERE / "plan.json").read_bytes())["cases"]
        k = Fraction(3991625, 23464301)
        for phase in ("primary", "repeat"):
            source = plan.TRANSFER / "evidence" / phase / "v13/trials.jsonl.gz"
            with gzip.open(source) as raw:
                rows = [json.loads(line) for line in raw]
            for case_id, case in cases.items():
                if not (case["split"] == "historical" or case_id == "original-reference"):
                    continue
                old = case_id.replace("historical-", "inversions-").replace("original-", "inversions-")
                full = old + ("-high" if case_id == "original-reference" else "-ample")
                complete = [row["receipt"]["work_measurement"] for row in rows if row["cell"] == full]
                self.assertEqual(len(complete), 2)
                for measurement in complete:
                    self.assertTrue(measurement["complete"])
                    estimate = (measurement["instructions"] + measurement["rep_bytes"]) * k
                    self.assertGreater(estimate, case["limits"]["low"] * 1_000_000)
                    self.assertLessEqual(estimate, case["limits"]["high"] * 1_000_000)


class AdditionalSolversTest(unittest.TestCase):
    def test_ties_odd_widths_and_suffix_order_against_quadratic_oracle(self):
        import sys
        sys.path.insert(0, str(plan.PILOT))
        self.addCleanup(sys.path.remove, str(plan.PILOT))
        import corpus

        with tempfile.TemporaryDirectory() as tmp:
            binaries = []
            for index in (1, 2):
                binary = Path(tmp) / str(index)
                subprocess.run(["g++", *corpus.FLAGS, f"-DSOLUTION={index}", plan.HERE / "alternatives.cpp",
                                "-o", binary], check=True)
                binaries.append(binary)
            for batches, n, alphabet, seed in ((1, 1, 1, 9), (2, 11, 1, 31), (4, 17, 3, 721),
                                               (3, 32, 16, 279), (6, 57, 137, 419)):
                params = (batches, n, alphabet, seed)
                random = corpus.numbers(seed)
                strict = inclusive = 0
                for _ in range(batches):
                    seq = [next(random) % alphabet for _ in range(n)]
                    for i, x in enumerate(seq):
                        for y in seq[i+1:]:
                            strict += x > y
                            inclusive += x >= y
                self.assertEqual(plan.outputs(params), (f"{strict}\n", f"{inclusive}\n"))
                for binary in binaries:
                    with self.subTest(binary=binary.name, parameters=params):
                        raw = (" ".join(map(str, params)) + "\n").encode()
                        self.assertEqual(subprocess.check_output([binary], input=raw), f"{strict}\n".encode())


if __name__ == "__main__":
    unittest.main()
