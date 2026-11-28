import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("v12_boundary_checks", HERE / "replay.py")
replay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(replay)


class Boundary(unittest.TestCase):
    def test_registered_work(self):
        plan = replay.design.plan()
        reg = replay.read(HERE / "registration.json")
        self.assertEqual(replay.read(HERE / "plan.json"), plan)
        self.assertEqual(len(reg["requests"]), 14)
        for phase in replay.PHASES:
            rows = list(replay.fixed.schedule(plan, phase, {}))
            self.assertEqual(len(set(rows)), 35)
            self.assertEqual(sum(r[2] == "native" for r in rows), 21)
            self.assertEqual({r[-1] for r in rows}, {203, 30000})

    def test_runtime_scope_and_registrations(self):
        plan = replay.design.plan()
        clock, installed, engine = replay.runtime_contract(plan)
        self.assertEqual(len(installed), 16)
        self.assertEqual(engine["engine"], "dynamorio-11.3-durante-v12-vm32m")
        self.assertEqual(clock["model_binding"], engine["binding"])
        scopes = clock["judge_cases"]["cases"]
        self.assertEqual([s["time_ms"] for s in scopes], [203, 30000])
        self.assertEqual({s["stdin_sha256"] for s in scopes}, {plan["original_input_sha256"]})
        for name, case in plan["cases"].items():
            previous = replay.read(HERE.parent / "plan.json")["cases"][name]
            self.assertEqual(case["binary_sha256"], previous["binary_sha256"])
            self.assertEqual(case["expected_stdout"], previous["expected_stdout"])

    def test_receipts_match_published_result_and_original_http_digests(self):
        root = HERE / "evidence"
        self.assertEqual(replay.verify(root), replay.read(HERE / "results.json"))
        reg = replay.read(HERE / "registration.json")
        plan = replay.read(HERE / "plan.json")
        cloud = replay.read(root / "infrastructure.json")["primary"]
        reg["requests"]["original-hybrid1024/contest/native"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "different HTTP body"):
            replay.replay_phase(root / "primary", "primary", cloud, plan, reg, *replay.runtime_contract(plan))

    def test_opposite_direction_mixed_and_failed_controls(self):
        both = {"a": "false_pass", "b": "false_timeout"}
        self.assertEqual(len(replay.compare_pair(both, both, True)), 2)
        self.assertFalse(replay.compare_pair(both, both, False))
        self.assertFalse(replay.compare_pair(both, {"a": "false_timeout", "b": "mixed_native"}, True))


if __name__ == "__main__":
    unittest.main()
