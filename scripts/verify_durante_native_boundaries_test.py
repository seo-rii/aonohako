"""Offline checks for the native/Durante comparison."""

import json
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch

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

    def test_live_targets_use_all_observations_and_keep_integer_ties(self):
        # All low/high verdicts agree for this case, but 47 ms separates the clocks.
        policy = {"instruction_ns_numerator": 1, "rep_byte_ns_numerator": 2, "ns_denominator": 3}
        native_ns = [50_000_000] * 5 + [51_000_000]
        reports = [{"instructions": 120_000_000, "rep_bytes": n} for n in (0, 4_000_000, 7_500_000)]
        rows, exclusions = boundaries.plan_limits(native_ns, reports, policy, 100)
        self.assertFalse(exclusions)
        self.assertEqual([r["nominal_ms"] for r in rows], [40, 60, 47])
        self.assertEqual(rows[2]["observed_deadline_disagreement"],
                         {"direction": "false_accept", "min_time_ms": 45, "max_time_ms": 49})
        reports[2]["instructions"] += 1
        rows, _ = boundaries.plan_limits(native_ns, reports, policy, 100)
        self.assertEqual(rows[2]["observed_deadline_disagreement"]["min_time_ms"], 46)
        reports[2]["instructions"] += 15_000_000
        rows, _ = boundaries.plan_limits(native_ns, reports, policy, 100)
        self.assertEqual(rows, [{"boundary": "low", "nominal_ms": 40}, {"boundary": "high", "nominal_ms": 60}])

    def test_split_weights_and_coincident_live_targets(self):
        policy = {"method": boundaries.SPLIT, "instruction_ns_numerator": 1, "movs_byte_ns_numerator": 2,
                  "stos_byte_ns_numerator": 5, "ns_denominator": 1}
        for expected_direction, (instructions, movs, stos), label, limit in (
                ("false_accept", (21_000_000, 0, 2_000_000), "low", 40),
                ("false_timeout", (45_000_000, 3_000_000, 4_000_000), "high", 60)):
            with self.subTest(label=label):
                report = {"instructions": instructions, "rep_bytes": movs + stos,
                          "rep_breakdown": {"movs_bytes": movs, "stos_bytes": stos}}
                rows, exclusions = boundaries.plan_limits([50_000_000] * 6, [report] * 3, policy, 100)
                self.assertFalse(exclusions)
                self.assertEqual(len(rows), 2)
                (selected,) = [r for r in rows if "observed_deadline_disagreement" in r]
                self.assertEqual((selected["boundary"], selected["nominal_ms"]), (label, limit))
                self.assertEqual(selected["observed_deadline_disagreement"]["direction"], expected_direction)

    def test_watchdog_headroom_and_unrepresentable_estimate(self):
        policy = {"instruction_ns_numerator": 1, "rep_byte_ns_numerator": 1, "ns_denominator": 1}
        native_ns = [50_000_000] * 6
        reports = [{"instructions": 150_000_000, "rep_bytes": 0}] * 3
        rows, excluded = boundaries.plan_limits(native_ns, reports, policy, 60)
        self.assertFalse(excluded)
        self.assertEqual(rows[-1], {"boundary": "observed_disagreement", "nominal_ms": 55,
                                    "observed_deadline_disagreement": {
                                        "direction": "false_timeout", "min_time_ms": 50, "max_time_ms": 149}})
        rows, excluded = boundaries.plan_limits(native_ns, reports, policy, 49)
        self.assertEqual(rows, [{"boundary": "low", "nominal_ms": 40}])
        self.assertEqual([r["boundary"] for r in excluded], ["high", "observed_disagreement"])
        reports[1] = {"instructions": 2**63, "rep_bytes": 0}
        rows, excluded = boundaries.plan_limits(native_ns, reports, policy, 60)
        self.assertEqual([r["nominal_ms"] for r in rows], [40, 60])
        self.assertIn("exceeds int64", excluded[0]["error"])

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

    def test_boundary_replay_preserves_dataset_counters(self):
        limits = {"memory_mb": 256, "wall_time_ms": 500, "cpu_time_ms": 300}
        policy = {"model_id": "test", "model_version": 1, "reference_id": "dataset",
                  "family": "cpp", "engine_profile": "test-engine", "dispatch_profile": "default"}
        role = {"role": "contestant", "source_language": "CPP20", "runtime_options": {}, "runtime_options_id": "id"}
        description = subprocess.CompletedProcess([], 0, json.dumps({"runtime_options": [role]}).encode(), b"")
        for latency, memory in ((False, False), (True, False), (True, True)):
            with self.subTest(latency=latency, memory=memory), TemporaryDirectory() as directory:
                args = SimpleNamespace(output=Path(directory), reference="reference", runtime_image="test-image")
                dataset = {"metered_limits": limits, "native_limits": limits, "dispatch_profile": "default"}
                report = {"complete": True}
                if latency:
                    dataset["latency_bound_version"] = 1
                    report["latency_bound"] = {"version": 1, "cycles_x16": 64}
                if memory:
                    dataset["memory_operand_bytes_version"] = 1
                    report["memory_operand_bytes"] = {"version": 1, "bytes": 0}
                with patch.object(boundaries.subprocess, "run", return_value=description) as descriptor:
                    native_env, judge_env, _ = boundaries.judge_config(args, dataset, policy)
                for env in (native_env, judge_env, descriptor.call_args.kwargs["env"]):
                    self.assertEqual(env["AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES"], "v1" if memory else "off")
                    self.assertEqual(env["AONOHAKO_DURANTE_LATENCY_BOUND"], "v1" if latency else "off")
                boundaries.check_optional_counters(report, dataset)
                boundaries.check_optional_counters({"complete": False}, dataset)
                if memory:
                    del report["memory_operand_bytes"]
                else:
                    report["memory_operand_bytes"] = {"version": 1, "bytes": 0}
                with self.assertRaises(RuntimeError):
                    boundaries.check_optional_counters(report, dataset)

    def test_boundary_receipt_rejects_partial_or_malformed_operands(self):
        dataset = {"latency_bound_version": 1, "memory_operand_bytes_version": 1}
        report = {"complete": True, "latency_bound": {"version": 1, "cycles_x16": 64}}
        for mem in (None, {"version": 2, "bytes": 0}, {"version": 1},
                    {"version": 1, "bytes": True}, {"version": 1, "bytes": 2**64}):
            with self.subTest(mem=mem), self.assertRaises(RuntimeError):
                boundaries.check_optional_counters({**report, "memory_operand_bytes": mem}, dataset)
        with self.assertRaises(RuntimeError):
            boundaries.check_optional_counters({"complete": False, "memory_operand_bytes": {"version": 1, "bytes": 0}}, dataset)


if __name__ == "__main__":
    unittest.main()
