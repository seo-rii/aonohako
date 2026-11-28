"""Regression tests for promotion and evidence replay of the inversion clock."""
from copy import deepcopy
import json
from pathlib import Path
import sys
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import replay

AC, TLE, WA = replay.checks.AC, replay.checks.TLE, replay.checks.WA
EVIDENCE = HERE / "evidence"


def observations(native, shared, candidate):
    return {arm: [{"receipt": {"status": s}} for s in outcomes]
            for arm, outcomes in (("native", native), ("shared", shared), ("candidate", candidate))}


class PromotionTests(unittest.TestCase):
    def setUp(self):
        self.plan = {"cases": {
            "known": {"split": "historical"}, "new0": {"split": "new"},
            "new1": {"split": "new"}, "new2": {"split": "new"},
        }}
        gains = observations([AC] * 3, [TLE] * 2, [AC] * 2)
        correct = observations([AC] * 3, [AC] * 2, [AC] * 2)
        groups = {("known", "high"): gains, ("new0", "high"): gains,
                  ("new1", "high"): gains, ("new2", "low"): correct}
        self.guests = {phase: deepcopy(groups) for phase in replay.PHASES}
        self.assertTrue(replay.score(self.plan, self.guests)["candidate_passed"])

    def test_one_lost_prospective_cell_blocks_even_if_aggregate_improves(self):
        for phase in replay.PHASES:
            with self.subTest(phase=phase):
                guests = deepcopy(self.guests)
                guests[phase]["new2", "low"] = observations([AC] * 3, [AC] * 2, [TLE] * 2)
                result = replay.score(self.plan, guests)
                panel = result["panels"][phase]["new"]
                self.assertEqual((panel["totals"]["shared"]["false_timeout"],
                                  panel["totals"]["candidate"]["false_timeout"]), (2, 1))
                self.assertTrue(panel["no_direction_increased"] and panel["coverage_preserved"])
                self.assertFalse(panel["no_agreements_lost"])
                self.assertFalse(result["gates"]["prospective_no_regression"])
                self.assertTrue(result["gates"]["historical_strict_improvement"])
                self.assertFalse(result["candidate_passed"])

    def test_mixed_candidate_cannot_erase_an_agreement(self):
        # Recovering the other previously mixed call preserves aggregate coverage.
        groups = self.guests["repeat"]
        groups["new1", "high"] = observations([AC] * 3, [AC, TLE], [AC] * 2)
        groups["new2", "low"] = observations([AC] * 3, [AC] * 2, [AC, TLE])
        result = replay.score(self.plan, self.guests)
        panel = result["panels"]["repeat"]["new"]
        self.assertTrue(panel["coverage_preserved"])
        self.assertEqual(panel["outcomes"]["new2/low"]["candidate"], "mixed_judge")
        self.assertFalse(result["candidate_passed"])

    def test_directional_failure_on_one_boot_survives_overall_improvement(self):
        groups = self.guests["repeat"]
        groups["known", "low"] = observations([TLE] * 3, [TLE] * 2, [WA] * 2)
        result = replay.score(self.plan, self.guests)
        self.assertTrue(result["gates"]["historical_strict_improvement"])
        self.assertFalse(result["gates"]["historical_no_regression"])
        self.assertFalse(result["candidate_passed"])

    def test_original_timeout_requires_an_actual_live_stop(self):
        self.plan["cases"]["slow"] = {"split": "original", "role": "slow"}
        group = observations([TLE] * 3, [TLE] * 2, [TLE] * 2)
        for arm in replay.ARMS:
            for row in group[arm]:
                row["receipt"]["work_measurement_execution"] = {"termination_source": "durante_work_clock"}
        self.guests["primary"]["slow", "contest"] = group
        self.assertTrue(replay.score(self.plan, self.guests)["candidate_passed"])
        group["candidate"][0]["receipt"]["work_measurement_execution"]["termination_source"] = "durante_work_clock_final"
        result = replay.score(self.plan, self.guests)
        self.assertFalse(result["gates"]["original_contest_and_stress"])
        self.assertFalse(result["candidate_passed"])


class EvidenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.plan = replay.read(HERE / "plan.json")
        cls.registration = replay.read(HERE / "registration.json")
        cls.thresholds = {k: {"low_ms": 10, "high_ms": 16, "native_median_ns": 13_000_000}
                          for k, c in cls.plan["cases"].items() if c["split"] == "new"}

    def test_best_possible_scalar_has_strict_lower_and_inclusive_upper_bounds(self):
        def ample(a, b):
            return {arm: [{"receipt": {"work_measurement": {"instructions": work - 3, "rep_bytes": 3}}}]
                    for arm, work in zip(replay.ARMS, (a, b), strict=True)}

        def controls(ms, successes):
            return {"native": [{"time_ms": ms, "receipt": {"status": value}} for value in successes]}

        groups = {
            ("slow", "low"): controls(10, [TLE, TLE, TLE]),
            ("slow", "ample"): ample(7_000_000, 10_000_000),
            ("fast", "high"): controls(20, [WA, WA, WA]),
            ("fast", "ample"): ample(20_000_000, 25_000_000),
            ("noisy", "low"): controls(1_000, [AC, TLE, TLE]),
        }
        limits = replay.scalar_constraints(groups)
        self.assertEqual(limits["lower_exclusive"]["work"], 10_000_000)
        self.assertEqual(limits["upper_inclusive"]["work"], 20_000_000)
        for side in ("lower_exclusive", "upper_inclusive"):
            self.assertEqual((limits[side]["numerator"], limits[side]["denominator"]), (1, 1))
        self.assertFalse(limits["interval_nonempty"])
        groups["fast", "high"] = controls(21, [AC, AC, AC])
        limits = replay.scalar_constraints(groups)
        self.assertEqual((limits["upper_inclusive"]["numerator"], limits["upper_inclusive"]["denominator"]), (21, 20))
        self.assertTrue(limits["interval_nonempty"])

    def test_thresholds_reconstructed_from_five_native_runs_only(self):
        sample_ns = [13_010_000, 9_000_000, 51_000_000, 14_000_000, 11_000_000]
        rows = {(key, "ample"): {"native": [{"receipt": {"cpu_time_ns": ns}} for ns in sample_ns]}
                for key in self.thresholds}
        bounds = replay.thresholds_from_rows(self.plan, rows)
        self.assertEqual(bounds, {key: {"low_ms": 10, "high_ms": 16, "native_median_ns": 13_010_000}
                                  for key in self.thresholds})
        rows[next(iter(rows))]["native"].pop()
        with self.assertRaisesRegex(ValueError, "five"):
            replay.thresholds_from_rows(self.plan, rows)

    def test_reconstructed_runtime_bindings_use_the_separate_rational_weights(self):
        clocks = replay.bound_clocks(self.plan, self.thresholds)
        self.assertEqual(clocks["shared"]["model_binding"], self.plan["baseline_binding"])
        self.assertNotEqual(clocks["candidate"]["model_binding"], clocks["shared"]["model_binding"])
        for arm, clock in clocks.items():
            self.assertEqual({c["model_binding"] for c in clock["judge_cases"]["cases"]}, {clock["model_binding"]})
            self.assertEqual(replay.checks.model_binding(clock["policy"], clock["runtime_options_id"]),
                             clock["model_binding"])
        policy = clocks["candidate"]["policy"]
        self.assertEqual((policy["instruction_ns_numerator"], policy["rep_byte_ns_numerator"], policy["ns_denominator"]),
                         (3_991_625, 3_991_625, 23_464_301))
        changed = deepcopy(self.plan)
        changed["calibration"]["candidate"]["numerator"] += 1
        self.assertNotEqual(replay.bound_clocks(changed, self.thresholds)["candidate"]["model_binding"],
                            clocks["candidate"]["model_binding"])

    def test_changed_artifact_and_rehashed_repeat_thresholds_are_rejected(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "repeat"
            target.mkdir()
            original = {"platform.json": b"{}", "trials.jsonl": b"", "exec-trace.txt": b"",
                        "thresholds.json": json.dumps(self.thresholds).encode()}
            for key, data in original.items():
                (target / key).write_bytes(data)
            digests = {"clock/" + k: replay.sha(v) for k, v in original.items()}
            tampered = deepcopy(self.thresholds)
            tampered[next(iter(tampered))]["high_ms"] += 1
            changed = json.dumps(tampered).encode()
            (target / "thresholds.json").write_bytes(changed)
            cloud = {"repeat": {"files": digests}}
            with self.assertRaisesRegex(ValueError, "changed saved"):
                replay.verify_guest(root, "repeat", self.plan, self.registration, self.thresholds, cloud, {})
            digests["clock/thresholds.json"] = replay.sha(changed)
            with self.assertRaisesRegex(ValueError, "different deadlines"):
                replay.verify_guest(root, "repeat", self.plan, self.registration, self.thresholds, cloud, {})

    def test_registered_payload_two_fresh_boots_and_both_cleanup_receipts(self):
        # The inversion study reuses the historical transfer's topology verifier.
        root = replay.TRANSFER
        base = replay.read(root / "evidence/infrastructure.json")
        registration = replay.read(root / "registration.json")
        replay.transfer.check_topology(base, registration)
        bad = deepcopy(base)
        bad["repeat"]["archive_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "payload or cleanup"):
            replay.transfer.check_topology(bad, registration)
        for resource in ("instance", "boot_disk"):
            with self.subTest(resource=resource):
                bad = deepcopy(base)
                bad["primary"]["cleanup_http"][resource] = 200
                with self.assertRaisesRegex(ValueError, "payload or cleanup"):
                    replay.transfer.check_topology(bad, registration)
        for field in ("instance_id_sha256", "disk_id_sha256", "boot_sha256"):
            with self.subTest(field=field):
                bad = deepcopy(base)
                bad["repeat"][field] = bad["primary"][field]
                with self.assertRaisesRegex(ValueError, "reused identity"):
                    replay.transfer.check_topology(bad, registration)


class CapturedEvidenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.plan = replay.read(HERE / "plan.json")
        cls.registration = replay.read(HERE / "registration.json")
        cls.infra = replay.read(EVIDENCE / "infrastructure.json")
        cls.hashes = replay.read(EVIDENCE / "request_hashes.json")
        cls.files = {name: replay.checks.evidence(EVIDENCE / "primary", name)
                     for name in ("platform.json", "trials.jsonl", "thresholds.json", "exec-trace.txt")}
        cls.thresholds = json.loads(cls.files["thresholds.json"])
        # Replaying the intact capture prevents a malformed fixture from satisfying negative tests.
        replay.verify_guest(EVIDENCE, "primary", cls.plan, cls.registration, cls.thresholds, cls.infra, cls.hashes)

    def check_rehashed(self, name, changed, expected):
        # Update the outer digest too, so the test exercises independent receipt checks.
        files = {**self.files, name: changed}
        cloud = deepcopy(self.infra)
        cloud["primary"]["files"]["clock/" + name] = replay.sha(changed)
        with patch.object(replay.checks, "evidence", side_effect=lambda root, item: files[item]):
            with self.assertRaisesRegex(ValueError, expected):
                replay.verify_guest(EVIDENCE, "primary", self.plan, self.registration, self.thresholds,
                                    cloud, self.hashes)

    def test_host_threshold_hash_and_full_installation(self):
        host = json.loads(self.files["platform.json"])
        host["thresholds_sha256"] = "0" * 64
        self.check_rehashed("platform.json", json.dumps(host).encode(), "thresholds differ")
        host = json.loads(self.files["platform.json"])
        host["files"] = replay.read_clock_plan()["files"]
        self.check_rehashed("platform.json", json.dumps(host).encode(), "different installed runtime")

    def test_live_receipt_cannot_claim_the_shared_model_for_a_candidate_stop(self):
        rows = [json.loads(line) for line in self.files["trials.jsonl"].splitlines()]
        stop = next(row["receipt"]["work_measurement_execution"] for row in rows
                    if row["kind"] == "candidate" and
                    row["receipt"]["work_measurement_execution"]["termination_source"] == "durante_work_clock")
        self.assertNotEqual(stop["clock"]["model_binding"], self.plan["baseline_binding"])
        stop["clock"]["model_binding"] = self.plan["baseline_binding"]
        changed = b"\n".join(json.dumps(row).encode() for row in rows)
        self.check_rehashed("trials.jsonl", changed, "wrong weighted model or budget")

    def test_rehashed_request_must_match_registered_schedule_and_digest(self):
        lines = self.files["trials.jsonl"].splitlines()
        row = json.loads(lines[0])
        row["request_sha256"] = "0" * 64
        lines[0] = json.dumps(row).encode()
        self.check_rehashed("trials.jsonl", b"\n".join(lines), "unexpected request or order")


if __name__ == "__main__":
    unittest.main()
