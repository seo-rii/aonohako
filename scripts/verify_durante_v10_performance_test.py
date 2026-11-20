"""Mutation checks for the published CPP20 v9/v10 benchmark evidence."""
import json
from pathlib import Path
import unittest

from verify_durante_v10_performance import verify


RECEIPT = Path(__file__).resolve().parents[1] / "docs/durante/cloud-run-cpp20-v10-performance.json"


class PerformanceReceiptTests(unittest.TestCase):
    def setUp(self):
        self.data = json.loads(RECEIPT.read_bytes())

    def test_complete_receipt(self):
        verify(self.data)

    def test_block_direction_and_schedule(self):
        case = self.data["cases"]["short-stream"]
        case["counterbalanced_cpu"]["faster_blocks"] += 1
        with self.assertRaisesRegex(RuntimeError, "block effect"):
            verify(self.data)
        case["counterbalanced_cpu"]["faster_blocks"] -= 1
        case["full"][0][0], case["full"][1][0] = case["full"][1][0], case["full"][0][0]
        with self.assertRaisesRegex(RuntimeError, "order"):
            verify(self.data)

    def test_cutoff_needs_crossed_weighted_progress(self):
        row = self.data["cases"]["rep_copy-2"]["cutoff"][0]
        row[6:9] = [0, 0, 0]
        with self.assertRaisesRegex(RuntimeError, "cutoff"):
            verify(self.data)

    def test_worker_identity_and_placement(self):
        candidate = self.data["arms"]["candidate"]
        worker = candidate["worker_sha256"]
        candidate["worker_sha256"] = self.data["arms"]["baseline"]["worker_sha256"]
        with self.assertRaisesRegex(RuntimeError, "worker"):
            verify(self.data)
        candidate["worker_sha256"] = worker
        self.data["placement"]["distinct_instances"] = 2
        with self.assertRaisesRegex(RuntimeError, "placement"):
            verify(self.data)


if __name__ == "__main__":
    unittest.main()
