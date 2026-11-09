import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("workmeter_suite", ROOT / "suite.py")
suite = importlib.util.module_from_spec(spec)
spec.loader.exec_module(suite)


class SuiteTests(unittest.TestCase):
    def test_affine_reference_matches_small_independent_recurrence(self):
        for count in (0, 1, 2, 31, 256, 999):
            value = 1
            for _ in range(count):
                value = (value * 1664525 + 1013904223) % (1 << 32)
            self.assertEqual(suite.lcg_after(count), value)

    def test_generated_corpus_is_complete_and_integrity_checked(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "corpus"
            result = suite.generate(output)
            self.assertEqual(result, json.loads((output / "manifest.json").read_text()))
            self.assertEqual(len(result["workloads"]), 58)
            self.assertEqual(len({row["id"] for row in result["workloads"]}), 58)
            for relative, digest in result["files"].items():
                self.assertEqual(hashlib.sha256((output / relative).read_bytes()).hexdigest(), digest)
                self.assertEqual((output / relative).stat().st_mode & 0o777, 0o600)
            for row in result["workloads"]:
                self.assertEqual(row["argv"][1:3], ["-I", "-S"])
                self.assertTrue((output / row["primary"]).is_file())
                self.assertEqual((output / row["expected"]).stat().st_size, row["expected_bytes"])

    def test_every_scaling_fixture_matches_independent_expected_output(self):
        for family in suite.FAMILIES:
            for scale in suite.SCALES:
                with self.subTest(family=family, scale=scale):
                    result = subprocess.run(
                        [sys.executable, "-I", "-S", str(ROOT / "fixtures" / (family + ".py"))],
                        input=f"{scale}\n".encode(), capture_output=True, timeout=30,
                    )
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, suite.expected_output(family, scale))
                    self.assertFalse(result.stderr)

    def test_boundary_exits_and_output_are_not_all_success(self):
        for name in ("normal", "system_exit_zero", "system_exit_nonzero", "exception", "immediate_exit"):
            with self.subTest(boundary=name):
                result = subprocess.run(
                    [sys.executable, "-I", "-S", str(ROOT / "fixtures/boundary.py")],
                    input=(name + "\n").encode(), capture_output=True, timeout=10,
                )
                contract = suite.boundary_contract(name)
                self.assertEqual(result.returncode, contract["expected_execution"]["code"])
                self.assertEqual(result.stdout, contract["expected_output"])
        self.assertEqual(suite.boundary_contract("immediate_exit")["expected_metric"]["ir_count"], "missing")

    def test_threads_have_explicit_runtime_specific_failure_contract(self):
        native = suite.boundary_contract("thread", "python")
        pypy = suite.boundary_contract("thread", "pypy")
        self.assertEqual(native["expected_execution"], {"kind": "exit", "code": 1})
        self.assertEqual(native["expected_output"], b"")
        self.assertEqual(native["expected_metric"]["ir_count"], "unsupported_threads")
        self.assertEqual(pypy["expected_execution"], {"kind": "exit", "code": 0})
        self.assertEqual(pypy["expected_output"], b"done\n")

    def test_timeouts_have_distinct_limits_and_no_report_assumption(self):
        wall = suite.boundary_contract("wall_timeout")
        cpu = suite.boundary_contract("cpu_timeout")
        self.assertEqual(wall["expected_execution"]["kind"], "wall_limit")
        self.assertEqual(cpu["expected_execution"]["kind"], "cpu_limit")
        self.assertLess(wall["wall_timeout_seconds"], wall["cpu_limit_seconds"])
        self.assertLess(cpu["cpu_limit_seconds"], cpu["wall_timeout_seconds"])
        self.assertEqual(wall["expected_metric"]["ir_count"], "missing")
        self.assertEqual(cpu["expected_metric"]["ir_count"], "missing")

    def test_unknown_configuration_and_existing_output_are_rejected(self):
        for family, scale in (("../x", 1), ("pure_python", 0), ("pure_python", True)):
            with self.assertRaises(ValueError):
                suite.expected_output(family, scale)
        with self.assertRaises(ValueError):
            suite.boundary_contract("arbitrary")
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "corpus"
            for languages in ((), ("python", "python"), ("ruby",)):
                with self.assertRaises(ValueError):
                    suite.generate(output, languages)
            output.mkdir()
            (output / "keep").write_text("preserved")
            with self.assertRaises(ValueError):
                suite.generate(output)
            self.assertEqual((output / "keep").read_text(), "preserved")

    def test_source_targets_reject_unknown_inputs(self):
        for family in suite.FAMILIES:
            result = subprocess.run(
                [sys.executable, "-I", "-S", str(ROOT / "fixtures" / (family + ".py"))],
                input=b"8\n", capture_output=True, timeout=10,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(result.stdout)


if __name__ == "__main__":
    unittest.main()
