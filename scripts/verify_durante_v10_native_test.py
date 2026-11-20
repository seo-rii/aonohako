import gzip
import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from verify_durante_v10_native import ROOT, check_fit, check_platform, collect_cases, estimate, median_ns, score_case, spread, summarize_language

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

    def test_aggregate_and_typed_rep_spreads(self):
        rows = [([20_000_000, 20_000_000], [80_000_000, movs, stos]) for movs, stos in
                ((100, 100), (100, 101), (100, 102))]
        case = score_case(rows, CLOCK, GATES)
        self.assertEqual(case["failed_gates"], ["rep_spread", "stos_spread"])
        rows[-1][1][1:] = [102, 98]
        rows[1][1][1:] = [101, 99]
        self.assertEqual(score_case(rows, CLOCK, GATES)["failed_gates"], ["movs_spread", "stos_spread"])

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


class PublishedTrialsTests(unittest.TestCase):
    def setUp(self):
        path = ROOT / "docs/durante"
        self.history = json.loads((path / "cloud-run-cpp20-v10-performance.json").read_bytes())
        path = path / "v10-native"
        self.protocol = json.loads((path / "protocol.json").read_bytes())
        self.raw = gzip.decompress((path / "CPP20/dataset.json.gz").read_bytes())
        self.data = json.loads(self.raw)
        self.receipt = json.loads((path / "CPP20/evaluation.json").read_bytes())

    def collect(self):
        return collect_cases(self.data, "CPP20", self.protocol, self.history)[0]

    def test_reported_fit_pass_must_match_measured_gates(self):
        result, groups = collect_cases(self.data, "CPP20", self.protocol, self.history)
        self.assertFalse(check_fit(self.raw, self.data, self.receipt, result, groups,
                                   self.protocol, self.history)["passed"])
        self.receipt["evaluation"].update(passed=True, failures=[])
        with self.assertRaisesRegex(RuntimeError, "measured gates"):
            check_fit(self.raw, self.data, self.receipt, result, groups, self.protocol, self.history)

    def test_real_output_and_v10_provenance_are_required(self):
        sample = self.data["samples"][0]
        diag = sample["metered"]["work_measurement_execution"]
        diag["output_matches"] = False
        with self.assertRaisesRegex(RuntimeError, "diagnostic receipt"):
            self.collect()
        diag["output_matches"] = True
        sample["metered"]["work_measurement"]["installation"]["client_sha256"] = "0" * 64
        with self.assertRaisesRegex(RuntimeError, "v10 work report"):
            self.collect()

    def test_partial_collection_scores_only_completed_cases(self):
        self.data["complete"] = False
        self.data["samples"] = self.data["samples"][:5]
        self.data["samples"][-1]["native_after"] = {}
        result = self.collect()
        self.assertEqual(result["recorded_triplets"], 5)
        self.assertEqual(result["completed_triplets"], 4)
        completed = self.data["samples"][:4]
        self.assertEqual(result["native_control_cpu_sum_ns"], sum(
            s[phase]["cpu_time_ns"] for s in completed for phase in ("native_before", "native_after")))
        self.assertEqual(result["diagnostic_cpu_sum_ns"], sum(s["metered"]["cpu_time_ns"] for s in completed))
        self.assertEqual(list(result["cases"]), ["integer/1"])
        self.data["failures"] = ["native-after interrupted"]
        with TemporaryDirectory() as tmp:
            directory = Path(tmp)
            (directory / "dataset.json.gz").write_bytes(gzip.compress(json.dumps(self.data).encode()))
            failure = {"phase": "collect", "exit_code": 1, "reason": self.data["failures"][0]}
            (directory / "failure.json").write_text(json.dumps(failure))
            summary = summarize_language(directory, "CPP20", self.protocol, self.history, None)
            self.assertEqual(summary["status"], "collection_failed")
            self.assertEqual(summary["cases"], result["cases"])
            self.assertNotIn("fit_passed", summary)
        self.data["samples"][3]["repetition"] = 2
        with self.assertRaisesRegex(RuntimeError, "duplicate trials"):
            self.collect()

    def test_diagnostic_cost_requires_reported_process_cpu(self):
        run = self.data["samples"][0]["metered"]
        run["cpu_time_ns"] = run["cpu_accounting"]["wait_target_ns"] + 1
        with self.assertRaisesRegex(RuntimeError, "diagnostic process CPU"):
            self.collect()

    def test_partial_collection_binds_the_original_fixture_and_binary(self):
        self.data["complete"] = False
        self.data["samples"] = self.data["samples"][:5]
        sample = self.data["samples"][-1]
        sample["native_after"] = {}
        for field in ("source_sha256", "input_sha256", "expected_sha256", "binary_sha256"):
            with self.subTest(field=field):
                original = sample[field]
                sample[field] = "0" * 64
                with self.assertRaisesRegex(RuntimeError, "fixture|binary"):
                    self.collect()
                sample[field] = original


class DeploymentChecks(unittest.TestCase):
    def setUp(self):
        self.history = json.loads((ROOT / "docs/durante/cloud-run-cpp20-v10-performance.json").read_bytes())
        arm = self.history["arms"]["candidate"]
        hashes = {k: arm[k] for k in ("reference_sha256", "worker_sha256")}
        hashes.update(client_sha256=arm["installation"]["client_sha256"], commit=arm["installation"]["source_commit"])
        self.request = dict(language="CPP20", status=200, request_id_hash="9" * 64, instance_id_sha256="8" * 64,
                            collector_exit=0, complete=True, dataset_sha256="7" * 64, triplets=45)
        image = "sha256:" + "1" * 64
        self.deployment = dict(revision="trial-revision", image_digest=image, base_image="base@sha256:" + "0" * 64,
                               region="asia-northeast3", gen=2, cpu=1, concurrency=1, memory_mib=2048, private=True,
                               receipt_get_status_counts={}, receipt_get_count=0, cpu_always_allocated=False,
                               binaries=hashes, requests=[self.request], cleanup=dict(service_http=404, revision_http=404,
                                                                                      package_http=404))
        self.results = {"CPP20": dict(status="collected", dataset_sha256="7" * 64, image=image, recorded_triplets=45)}

    def test_async_acceptance_needs_matching_retrieval(self):
        self.request.update(status=202, response_http=200, receipt_request_id_hash="a" * 64)
        self.deployment.update(cpu_always_allocated=True, receipt_get_count=3,
                               receipt_get_status_counts={"202": 2, "200": 1})
        self.verify()
        self.request["response_http"] = 202
        with self.assertRaisesRegex(RuntimeError, "completed receipt"):
            self.verify()
        self.request["response_http"] = 200
        self.deployment["receipt_get_status_counts"]["200"] = 2
        with self.assertRaisesRegex(RuntimeError, "retrieval counts"):
            self.verify()

    def verify(self):
        check_platform({"deployments": [self.deployment]}, self.results, self.history)

    def test_dataset_and_image_must_correspond(self):
        self.verify()
        self.request["dataset_sha256"] = "6" * 64
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            self.verify()
        self.request["dataset_sha256"] = self.results["CPP20"]["dataset_sha256"]
        self.deployment["image_digest"] = "sha256:" + "2" * 64
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            self.verify()

    def test_partial_archive_matches_receipt_and_failure_exit(self):
        result = self.results["CPP20"]
        result.update(status="collection_failed", recorded_triplets=17, failure={"exit_code": 2})
        self.request.update(complete=False, collector_exit=2, triplets=17)
        self.verify()
        for field, value in (("collector_exit", 1), ("triplets", 0), ("dataset_sha256", "0" * 64)):
            with self.subTest(field=field):
                original = self.request[field]
                self.request[field] = value
                with self.assertRaisesRegex(RuntimeError, "does not match"):
                    self.verify()
                self.request[field] = original
        del result["dataset_sha256"]
        del result["recorded_triplets"]
        del result["image"]
        with self.assertRaisesRegex(RuntimeError, "missing partial archive"):
            self.verify()
        self.request["triplets"] = 0
        with self.assertRaisesRegex(RuntimeError, "missing partial archive"):
            self.verify()
        del self.request["dataset_sha256"]
        self.verify()
        self.request["collector_exit"] = 1
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            self.verify()

    def test_unrecovered_response_is_preserved_without_becoming_a_result(self):
        lost = dict(language="CPP20", status=200, request_id_hash="5" * 64, instance_id_sha256="8" * 64,
                    result_available=False, transport_failure="response not recovered")
        self.deployment["requests"].insert(0, lost)
        self.verify()
        for field, value in (("complete", False), ("collector_exit", 1), ("dataset_sha256", "0" * 64), ("triplets", 0)):
            with self.subTest(field=field):
                lost[field] = value
                with self.assertRaisesRegex(RuntimeError, "unrecovered"):
                    self.verify()
                del lost[field]
        self.deployment["requests"].pop()
        with self.assertRaisesRegex(RuntimeError, "absent or duplicate successful"):
            self.verify()

    def test_duplicated_request_and_incomplete_deletion(self):
        self.deployment["requests"].append(dict(self.request))
        with self.assertRaisesRegex(RuntimeError, "duplicate request"):
            self.verify()
        self.deployment["requests"].pop()
        self.deployment["cleanup"]["revision_http"] = 200
        with self.assertRaisesRegex(RuntimeError, "cleanup"):
            self.verify()


if __name__ == "__main__":
    unittest.main()
