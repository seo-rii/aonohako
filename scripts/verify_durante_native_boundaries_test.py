"""Offline checks for the native/Durante comparison."""

import json
from pathlib import Path
import unittest

import verify_durante_native_boundaries as boundaries


AC = "Accepted"
TLE = "Time Limit Exceeded"


def native(status, source, ns):
    receipt = {
        "status": status,
        "verdict_source": source,
        "cpu_time_ns": ns,
        "cpu_time_ms": (ns + 999_999) // 1_000_000,
        "cpu_accounting": {"selected_source": "wait_rusage", "wait_target_ns": ns},
        "wall_time_ms": 50,
    }
    if status == AC:
        receipt["exit_code"] = 0
    return receipt


class NativeBoundaryTests(unittest.TestCase):
    def test_thresholds_keep_the_half_nanosecond_median_until_rounding(self):
        controls = [40_000_003, 39_999_998, 40_000_001, 39_999_999, 40_000_002, 40_000_000]
        self.assertEqual(boundaries.thresholds(controls), (32, 49))
        self.assertEqual(boundaries.thresholds([40_000_000] * 6), (32, 48))
        self.assertEqual(boundaries.thresholds([40_000_000] * 8), (32, 48))

    def test_time_limits_must_bracket_the_native_median(self):
        # For 0.5 ms the cutoffs coincide. At 0.9 and 1 ms they are different,
        # but a 1 ms low cutoff still cannot exercise the lower side of the median.
        for ns in (500_000, 900_000, 1_000_000):
            with self.subTest(ns=ns), self.assertRaises(RuntimeError):
                boundaries.thresholds([ns] * 6)
        self.assertEqual(boundaries.thresholds([1_000_001] * 6), (1, 2))

    def test_native_cpu_cutoff_is_strict(self):
        boundaries.check_native(native(AC, "stdout", 40_000_000), 40)
        for source in ("cpu_time", "cpu_time_final", "cpu_time_cgroup", "cpu_time_cgroup_final"):
            with self.subTest(source=source):
                boundaries.check_native(native(TLE, source, 40_000_001), 40)
        for response in (native(TLE, "cpu_time", 40_000_000), native(AC, "stdout", 40_000_001)):
            with self.subTest(response=response), self.assertRaises(RuntimeError):
                boundaries.check_native(response, 40)

    def test_unattributed_or_instrumented_timeouts_are_excluded(self):
        for source in ("wall_time", "cpu_rlimit", "durante_work_clock"):
            with self.subTest(source=source), self.assertRaises(RuntimeError):
                boundaries.check_native(native(TLE, source, 40_000_001), 40)
        for field in ("cpu_time_sampling", "cpu_time_normalization", "work_measurement"):
            response = native(AC, "stdout", 40_000_000)
            response[field] = {}
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                boundaries.check_native(response, 40)

    def test_native_receipt_must_have_consistent_accounting_and_no_acceptance_score(self):
        accepted = native(AC, "stdout", 39_000_001)
        for change in ({"cpu_time_ms": 39}, {"score": 0.25}, {"cpu_accounting": {"selected_source": "unknown"}}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                boundaries.check_native({**accepted, **change}, 40)

    def test_mixed_natives_remain_visible_in_both_directions(self):
        for live, opposite in ((AC, TLE), (TLE, AC)):
            with self.subTest(live=live):
                self.assertEqual(boundaries.classify(live, [live] * 3), "unanimous_agreement")
                self.assertEqual(boundaries.classify(live, [opposite] * 3), "unanimous_opposition")
                self.assertEqual(boundaries.classify(live, [opposite, live, live]), "mixed")

    def test_binding_matches_recorded_go_clock_and_includes_coefficients(self):
        path = Path(__file__).resolve().parents[1] / "docs/durante/gce-cpp20-v7-qualification.json"
        experiment = json.loads(path.read_bytes())
        policy = experiment["frozen_external_rounds"]["split-gce"]["policy"]
        row = experiment["online_native_median_boundaries"]["rows"][0]
        expected = row["clock_model_binding"]
        options = row["runtime_options_id"]
        self.assertEqual(boundaries.model_binding(policy, options), expected)
        changed = {**policy, "instruction_ns_numerator": policy["instruction_ns_numerator"] + 1}
        self.assertNotEqual(boundaries.model_binding(changed, options), expected)


if __name__ == "__main__":
    unittest.main()
