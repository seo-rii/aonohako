"""Exercise the pre-registered two-boot counterexample rule and preserved receipts."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import sys
from types import ModuleType
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("boundary_replay", HERE / "replay.py")
replay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(replay)
AC, TLE = replay.base.checks.AC, replay.base.checks.TLE


def groups(native, clock):
    return {"native": [{"receipt": {"status": v}} for v in native],
            "shared": [{"receipt": {"status": v, "work_measurement_execution": {
                "termination_source": "durante_work_clock" if v == TLE else "stdout"}}} for v in clock]}


class DisqualificationTests(unittest.TestCase):
    def setUp(self):
        self.plan = {"cases": {"new": {"split": "variant", "role": "sqrt512", "limits": {"contest": 203}},
                               "control": {"split": "original", "role": "slow", "limits": {"contest": 203}}}}
        self.guests = {p: {("new", "contest"): groups([AC] * 3, [TLE] * 2),
                           ("control", "contest"): groups([TLE] * 3, [TLE] * 2)} for p in replay.base.PHASES}

    def test_same_direction_and_same_variant_on_both_boots(self):
        result = replay.summarize(self.plan, self.guests)
        self.assertTrue(result["disqualified"])
        self.assertEqual(result["repeated_opposition"], [{"case": "new", "direction": "false_timeout"}])
        for native, shared in (([AC] * 3, [AC] * 2), ([TLE] * 3, [AC] * 2), ([AC, TLE, AC], [TLE] * 2),
                               ([AC] * 3, [AC, TLE])):
            with self.subTest(native=native, shared=shared):
                guests = deepcopy(self.guests)
                guests["repeat"]["new", "contest"] = groups(native, shared)
                result = replay.summarize(self.plan, guests)
                self.assertFalse(result["disqualified"])
                self.assertEqual(result["repeated_opposition"], [])

    def test_inflight_original_slow_control_is_required(self):
        slow = self.guests["repeat"]["control", "contest"]["shared"][0]["receipt"]
        slow["work_measurement_execution"]["termination_source"] = "durante_work_clock_final"
        result = replay.summarize(self.plan, self.guests)
        self.assertTrue(result["repeated_opposition"])
        self.assertFalse(result["controls_passed"])
        self.assertFalse(result["disqualified"])

    def test_shared_verifier_preserves_foreign_imports_and_uses_fixed_schedule(self):
        foreign = ModuleType("protocol")
        with patch.dict(sys.modules, protocol=foreign, guest=ModuleType("guest"), plan=ModuleType("plan")):
            checks = replay.load_checks()
            self.assertIs(sys.modules["protocol"], foreign)
            plan = checks.read(HERE / "plan.json")
            self.assertEqual(checks.ARMS, ("shared",))
            self.assertEqual(len(list(checks.guest.schedule(plan, "primary", {}))), 115)
            self.assertEqual(set(checks.bound_clocks(plan, {})), {"shared"})


class EvidenceTests(unittest.TestCase):
    def test_rehashed_wrong_binding_cannot_change_deciding_clock(self):
        base = replay.base
        root = HERE / "evidence"
        plan, reg = (base.read(HERE / path) for path in ("plan.json", "registration.json"))
        infra = base.read(root / "infrastructure.json")
        hashes = base.read(root / "request_hashes.json")
        files = {name: base.checks.evidence(root / "repeat", name)
                 for name in ("platform.json", "trials.jsonl", "thresholds.json", "exec-trace.txt")}
        base.verify_guest(root, "repeat", plan, reg, {}, infra, hashes)
        rows = [json.loads(line) for line in files["trials.jsonl"].splitlines()]
        target = next(r for r in rows if r["kind"] == "shared" and r["boundary"] == "contest" and
                      r["case_id"] == "original-slow")
        target["receipt"]["work_measurement_execution"]["clock"]["model_binding"] = "0" * 64
        files["trials.jsonl"] = b"\n".join(json.dumps(r).encode() for r in rows)
        infra["repeat"]["files"]["clock/trials.jsonl"] = base.sha(files["trials.jsonl"])
        with patch.object(base.checks, "evidence", side_effect=lambda root, name: files[name]):
            with self.assertRaisesRegex(ValueError, "wrong weighted model or budget"):
                base.verify_guest(root, "repeat", plan, reg, {}, infra, hashes)


if __name__ == "__main__":
    unittest.main()
