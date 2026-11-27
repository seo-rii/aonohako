"""Check frozen calibration, exact live representation and matched release gates."""
from copy import deepcopy
from fractions import Fraction
import importlib.util
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("refit_audit", HERE/"verify.py")
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)
freeze = audit.freeze


def comparison(n, b, c):
    def rows(stops, total):
        return [{"receipt":{"status":audit.pilot.TLE if i<stops else audit.pilot.AC}} for i in range(total)]
    return {"native":rows(n,3),"baseline":rows(b,2),"candidate":rows(c,2)}


class RefitTest(unittest.TestCase):
    def test_wire_precision(self):
        value = Fraction(1139371945900000,4752214584908649)
        self.assertEqual(freeze.wire_coefficient(value),value)
        value = Fraction(2**77, 3*2**77+1)
        rounded = freeze.wire_coefficient(value)
        self.assertEqual(rounded, Fraction(333333333,10**9))
        self.assertLessEqual(abs(rounded-value),Fraction(1,2*10**9))

    def test_reference_only_fit_and_independent_deadline_labels(self):
        cases = json.loads((HERE/"case_manifest.json").read_bytes())["cases"]
        rows = []
        for i,c in enumerate(cases,1):
            is_reference = c["split"] == "calibration"
            for _ in range(5):
                rows.append(dict(case_id=c["case_id"],kind="native",receipt={"cpu_time_ns":10_000_001*i}))
            for _ in range(2):
                rows.append(dict(case_id=c["case_id"],kind="counters",receipt={"work_measurement":{
                    "instructions":i*100 if is_reference else i*300,"rep_bytes":0}}))
        coefficient,deadlines = freeze.derive(cases,rows)
        self.assertEqual(coefficient, Fraction(10_000_001,100))
        test_id = next(c["case_id"] for c in cases if c["split"] == "held_out")
        for r in rows:
            if r["case_id"] == test_id and r["kind"] == "native":
                r["receipt"]["cpu_time_ns"] *= 100
        unchanged,different = freeze.derive(cases,rows)
        self.assertEqual(unchanged,coefficient)
        self.assertLess(deadlines[test_id]["high_ms"],different[test_id]["low_ms"])

    def test_complete_unique_schedule_and_frozen_scope(self):
        frozen = json.loads((HERE/"freeze.json").read_bytes())
        held = [c for c in json.loads((HERE/"case_manifest.json").read_bytes())["cases"] if c["split"] == "held_out"]
        rows = list(audit.schedule(frozen,held))
        self.assertEqual(len(set(rows)),258)
        self.assertEqual([sum(r[0] == mode for r in rows) for mode in ("original","held_out")],[90,168])
        by_id={c["case_id"]:c for c in held}
        for arm in audit.ARMS[1:]:
            scope = frozen[arm]["judge_cases"]["cases"]
            keys = {(r["problem_id"],r["stdin_sha256"],r["time_ms"],r["model_binding"]) for r in scope}
            self.assertEqual(len(keys),len(scope))
            for mode,case,problem,_,_,kind,_,ms in rows:
                if mode=="held_out" and kind==arm:
                    self.assertIn((problem,by_id[case]["stdin_sha256"],ms,frozen[arm]["model_binding"]),keys)

    def test_release_needs_directional_improvement_without_hiding_disagreements(self):
        prior = dict(qualified_contest_cases=3,reference_near_limits={"unanimous_opposition":4})
        groups = {("primary","one","high"):comparison(0,2,0), ("primary","two","low"):comparison(3,0,2),
                  ("repeat","one","high"):comparison(0,0,0), ("repeat","two","low"):comparison(1,0,2)}
        good=audit.tight_summary(groups)
        self.assertEqual(good["all_groups"]["candidate"]["mixed_native"],1)
        self.assertEqual(good["paired_unanimous"]["candidate"]["comparable"],3)
        self.assertTrue(all(audit.release_gates(prior,prior,good).values()))
        groups["repeat","one","high"] = comparison(3,2,0)
        groups["primary","two","low"] = comparison(0,0,0)
        worse=audit.release_gates(prior,prior,audit.tight_summary(groups))
        self.assertFalse(worse["no_more_false_passes"])
        groups["repeat","one","high"] = comparison(3,2,1)
        mixed=audit.release_gates(prior,prior,audit.tight_summary(groups))
        self.assertFalse(mixed["at_least_as_many_comparable_groups"])
        self.assertFalse(mixed["no_more_mixed_judge_groups"])
        regression=deepcopy(prior)
        regression["reference_near_limits"]["unanimous_opposition"] += 1
        regression["qualified_contest_cases"] = 2
        gates=audit.release_gates(regression,prior,good)
        self.assertFalse(gates["original_contest_and_stress"])
        self.assertFalse(gates["no_more_reference_boundary_opposition"])


if __name__ == "__main__":
    unittest.main()
