import gzip
import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from verify_durante_v10_native import ROOT, check_platform, collect_cases, estimate, median_ns, score_case, spread, summarize_language

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
        self.data = json.loads(gzip.decompress((path / "CPP20/dataset.json.gz").read_bytes()))

    def collect(self):
        return collect_cases(self.data, "CPP20", self.protocol, self.history)[0]

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
        self.assertEqual(list(result["cases"]), ["integer/1"])
        self.data["samples"][3]["repetition"] = 2
        with self.assertRaisesRegex(RuntimeError, "duplicate trials"):
            self.collect()


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
