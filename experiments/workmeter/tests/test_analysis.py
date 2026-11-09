import base64
import copy
import gzip
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import analyze
import protocol
from pypy import meter


def packed(value):
    raw = json.dumps(value).encode()
    return {"encoding": "gzip+base64", "data": base64.b64encode(gzip.compress(raw)).decode(),
            "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}


def jit_report():
    before = [{"kind": "e", "number": 1, "count": 2}]
    after = [{"kind": "e", "number": 1, "count": 12}]
    traces = [{"index": 0, "type": "loop", "loop_no": 1, "bridge_no": None,
               "source": ["/fixtures/Main.py", 1, "main", 0], "ops": [{"name": "int_add"}]}]
    counts = {("e", 1): 10}
    return {"schema": 1, "pid": 7, "mode": "mapped", "valid": True,
            "target_ok": True, "logging_enabled": True,
            "baseline_counts": before, "end_counts": after,
            "delta_counts": meter.encode_counts(counts), "delta_error": None,
            "traces": traces, "hook_errors": [], "trace_capture_capped": False,
            "metric": meter.summarize(traces, counts, set()), "counter_snapshot_nonempty": True}


class AnalysisTests(unittest.TestCase):
    def test_unavailable_and_single_observation_are_not_zero_cv(self):
        self.assertIsNone(analyze.work_distribution([None, 0])["cv_pct"])
        self.assertIsNone(analyze.work_distribution([10, None])["cv_pct"])
        self.assertIsNone(analyze.work_distribution([10])["cv_pct"])
        self.assertEqual(analyze.work_distribution([10, 10])["cv_pct"], 0)
        self.assertFalse(analyze.work_distribution([None, 10])["complete_series"])

    def test_raw_digest_and_decompression_limit_are_enforced(self):
        p = packed({"a": "b" * 100})
        self.assertEqual(json.loads(analyze.unpack(p, 1000)), {"a": "b" * 100})
        with self.assertRaises(ValueError):
            analyze.unpack(p, 8)
        p["sha256"] = "0" * 64
        with self.assertRaises(ValueError):
            analyze.unpack(p, 1000)

    def test_jit_mapping_is_recomputed_not_just_trusted(self):
        report = jit_report()
        analyze.verify_pypy(report)
        changed = copy.deepcopy(report)
        changed["metric"]["value"] += 1
        with self.assertRaises(ValueError):
            analyze.verify_pypy(changed)
        changed = copy.deepcopy(report)
        changed["delta_counts"][0]["count"] += 1
        with self.assertRaises(ValueError):
            analyze.verify_pypy(changed)
        changed = copy.deepcopy(report)
        changed["end_counts"].append(changed["end_counts"][0])
        with self.assertRaises(ValueError):
            analyze.verify_pypy(changed)
        changed = copy.deepcopy(report)
        changed["target_ok"] = False
        with self.assertRaises(ValueError):
            analyze.verify_pypy(changed)

    def test_cpu_component_mismatch_is_rejected(self):
        row = {"cpu_ms": 2, "user_cpu_ms": 1, "system_cpu_ms": 0, "wall_ms": 3,
               "raw_report": None, "metric": None}
        with self.assertRaises(ValueError):
            analyze.verify_row(row, "unused")

    def test_actual_seed_and_isolation_are_independently_verified(self):
        hashes = list(range(8))
        row = {"language": "python", "family": "original", "pid": 7,
               "cpu_ms": 2, "user_cpu_ms": 1, "system_cpu_ms": 1, "wall_ms": 3,
               "raw_report": None, "metric": None,
               "attestation": {"pid": 7, "hashes": hashes, "isolated": 1,
                               "ignore_environment": 1, "no_site": 1, "safe_path": True}}
        analyze.verify_row(row, "unused", hashes)
        for field, value in (("pid", 8), ("hashes", list(range(1, 9))), ("isolated", 0)):
            changed = copy.deepcopy(row)
            changed["attestation"][field] = value
            with self.assertRaises(ValueError):
                analyze.verify_row(changed, "unused", hashes)

    def test_selected_case_max_preserves_case_and_iteration_boundaries(self):
        workloads = [{"kind": "submission", "submission_id": 1, "id": case} for case in ("a", "b")]
        rows = []
        for iteration, a, b in ((0, 1, 10), (1, 10, 1)):
            for case, value in (("a", a), ("b", b)):
                rows.append({"submission_id": 1, "workload_id": case, "service": "s", "iteration": iteration,
                             "parallelism": 1, "copy_index": 0, "arm": "ir_count", "cpu_ms": value,
                             "work_units": value})
        failures = []
        result = analyze.selected_case_max(rows, workloads, failures)
        self.assertFalse(failures)
        self.assertEqual(result["1"]["1"]["ir_count"]["work_units"]["median"], 10)
        analyze.selected_case_max(rows[:-1], workloads, failures)
        self.assertEqual(len(failures), 1)

    def test_flat_work_coverage_signal_uses_cpu_growth(self):
        workloads = [{"kind": "diagnostic", "language": "python", "family": "hashlib_native",
                      "id": str(scale), "scale": scale} for scale in (1, 2, 4)]
        summary = {str(scale): {"1": {"ir_count": {"cpu_ms": {"median": 100 * scale},
                   "work_units": {"median": 10, "complete_series": True}}}} for scale in (1, 2, 4)}
        result = analyze.scaling_summary(workloads, summary)
        meter_result = next(r for r in result if r["arm"] == "ir_count")
        self.assertTrue(meter_result["ratios_to_scale1"]["4"]["flat_work_while_cpu_grows"])
        summary["4"]["1"]["ir_count"]["work_units"]["complete_series"] = False
        result = analyze.scaling_summary(workloads, summary)
        meter_result = next(r for r in result if r["arm"] == "ir_count")
        self.assertIsNone(meter_result["ratios_to_scale1"]["4"]["work_ratio"])

    def test_complete_cohort_and_missing_batch_fail_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            native = root / "native"
            (native / "metadata").mkdir(parents=True)
            (native / "provenance").mkdir()
            (native / "provenance/hashseed-smoke.json").write_text(json.dumps({
                "exit_code": 0, "rows": [{"fixed": {"1": {"hashes": list(range(8))}}}] * 2}))
            name = "a" * 32 + ".json"
            raw = json.dumps({"blocks": [[0, 1, "f", 0]]}).encode()
            (native / "metadata" / name).write_bytes(raw)
            manifest = json.dumps({"schema": 1, "files": {name: analyze.sha(raw)}}).encode()
            (native / "metadata-manifest.json").write_bytes(manifest)
            runtime = json.dumps({"metadata_manifest_sha256": analyze.sha(manifest)}).encode()
            (root / "runtime-manifest.json").write_bytes(runtime)
            workload = {"id": "one", "submission_id": 1, "kind": "submission", "language": "pypy"}
            fixtures = json.dumps({"workloads": [workload]}).encode()
            (root / "fixtures").mkdir()
            (root / "fixtures/manifest.json").write_bytes(fixtures)
            (root / "config.json").write_text(json.dumps({"prefix": "test", "services": ["test-01"]}))
            env = {"instance_id": "instance", "runtime_manifest_sha256": analyze.sha(runtime),
                   "fixture_manifest_sha256": analyze.sha(fixtures)}
            records = []
            for iteration in range(3):
                for parallelism in (1, 2):
                    request = {"workload_id": "one", "iteration": iteration, "parallelism": parallelism,
                               "arms": ["direct", "jit_mapped"]}
                    rows = []
                    for arm in request["arms"]:
                        for copy_index in range(parallelism):
                            report = jit_report() if arm == "jit_mapped" else None
                            rows.append({"workload_id": "one", "arm": arm, "copy_index": copy_index,
                                "pid": 7, "exit_code": 0, "termination": "exit", "timed_out": False,
                                "stdout_matches_tokens": True, "output_truncated": False, "descendants_reaped": [],
                                "metric_error": None, "authoritative_time": False, "cpu_ms": 2,
                                "user_cpu_ms": 1, "system_cpu_ms": 1, "wall_ms": 3,
                                "raw_report": packed(report) if report else None,
                                "metric": protocol.summarize_metric(report, arm, 7) if report else None})
                    records.append({"service": "test-01", "request": request, **request, "rows": rows, "environment": env})
            path = root / "test-01-responses.ndjson"
            path.write_text("".join(json.dumps(r) + "\n" for r in records))
            result = analyze.analyze(root, native)
            self.assertEqual(result["exit_code"], 0, result["failures"])
            self.assertEqual(result["validated_executions"], 18)
            path.write_text("".join(json.dumps(r) + "\n" for r in records[:-1]))
            result = analyze.analyze(root, native)
            self.assertEqual(result["exit_code"], 1)
            self.assertEqual(result["failures"][0]["stage"], "cohort_completeness")

    def test_fanout_latency_is_separate_from_target_cpu_cost(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            workload = {"id": "one", "submission_id": 1, "kind": "submission", "language": "python"}
            services = ["service-" + str(i) for i in range(4)]
            instances = {service: "instance-" + service for service in services}
            records = []
            for iteration in range(3):
                for arm, cpu, wall in (("source_plain", 2, 10), ("ir_count", 4, 12)):
                    request = {"workload_id": "one", "iteration": iteration, "parallelism": 1, "arms": [arm]}
                    results = []
                    for service in services:
                        row = {"workload_id": "one", "arm": arm, "copy_index": 0, "pid": 7,
                               "termination": "exit", "exit_code": 0, "stdout_matches_tokens": True,
                               "metric": {"pid": 7, "valid": True}, "authoritative_time": False,
                               "attestation": {"pid": 7},
                               "cpu_ms": cpu, "wall_ms": cpu + 1}
                        results.append({"service": service, **request, "rows": [row], "client_wall_ms": wall - 1,
                            "environment": {"instance_id": instances[service],
                                            "runtime_manifest_sha256": "runtime", "fixture_manifest_sha256": "fixture"}})
                    records.append({"request": request, "results": results,
                                    "client_batch_wall_ms": wall, "sum_target_cpu_ms": 4 * cpu})
            (root / "parallel-fanout.ndjson").write_text("".join(json.dumps(r) + "\n" for r in records))
            failures = []
            # Raw verification has independent tests above; isolate aggregation here.
            with patch.object(analyze, "verify_row") as verifier:
                result = analyze.analyze_fanout(root, [workload], {"services": services}, instances,
                                               "runtime", "fixture", "metadata", 3, failures)
                self.assertEqual(verifier.call_count, 24)
            self.assertFalse(failures)
            self.assertEqual(result["observed_executions"], 24)
            self.assertFalse(result["included_in_main_cv"])
            self.assertEqual(result["paired_cost"]["one"]["batch_wall_ratio"]["median"], 1.2)
            self.assertEqual(result["paired_cost"]["one"]["target_cpu_ratio"]["median"], 2)


if __name__ == "__main__":
    unittest.main()
