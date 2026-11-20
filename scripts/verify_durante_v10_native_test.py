import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from verify_durante_v10_native import ROOT, check_platform, estimate, median_ns, score_case, spread, summarize_language

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
                               binaries=hashes, requests=[self.request], cleanup=dict(service_http=404, revision_http=404,
                                                                                      package_http=404))
        self.results = {"CPP20": dict(status="collected", dataset_sha256="7" * 64, image=image, recorded_triplets=45)}

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
