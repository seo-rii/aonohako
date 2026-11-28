from copy import deepcopy
import json
from pathlib import Path
import unittest
from unittest.mock import patch

import verify as transfer
from protocol import (ORIGINAL_BINDING, ORIGINAL_IMAGE, TIMES, clock_for, digest, panel, read, schedule)


class TransferTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        previous=Path(__file__).resolve().parent.parent/"gce-qualified-judge"
        cls.frozen=read(previous/"refit/freeze.json")
        cls.cells=panel(read(previous/"registration.json")["corpus"],read(previous/"refit/case_manifest.json"),cls.frozen)

    def test_panel_binding_and_counter_control(self):
        self.assertEqual(len(self.cells),54)
        self.assertEqual([len(list(schedule(arm,"primary",self.cells))) for arm in ("v12","v13")],[270,108])
        self.assertNotEqual(list(schedule("v13","primary",self.cells)),list(schedule("v13","repeat",self.cells)))
        saved=deepcopy(self.frozen)
        before=clock_for("v12",ORIGINAL_IMAGE,self.cells,self.frozen,transfer.V12.model_binding)
        after=clock_for("v13",transfer.CANDIDATE_IMAGE,self.cells,self.frozen,transfer.V13.model_binding)
        self.assertEqual(before["model_binding"],ORIGINAL_BINDING)
        self.assertNotEqual(before["model_binding"],after["model_binding"])
        self.assertEqual(before["time_models"]["models"],after["time_models"]["models"])
        self.assertEqual(saved,self.frozen)
        self.assertIn("transfer-counter-probe",{v["problem_id"] for v in after["judge_cases"]["cases"]})

    def test_counter_spread_and_exact_rep(self):
        self.assertEqual(transfer.check_counts([(501,0,9),(629,0,9)]),(128,True))
        self.assertEqual(transfer.check_counts([(501,0,9),(630,0,10)]),(129,False))

    def test_deadline_labels(self):
        native=["Accepted"]*3
        tle=["Time Limit Exceeded"]*2
        self.assertEqual(transfer.classify(native,tle),"false_timeout")
        self.assertEqual(transfer.classify(tle,native),"false_pass")
        self.assertEqual(transfer.classify(tle,["Accepted","Time Limit Exceeded"]),"mixed_judge")

    def test_gate_does_not_treat_counter_drift_as_a_timing_observation(self):
        case=self.cells["range_min-fast-contest"]
        groups={}
        row={"stdout":"42", "receipt":{"status":"Accepted", "work_measurement":{
             "complete":True,"instructions":1000,"rep_breakdown":{"movs_bytes":0,"stos_bytes":8}}}}
        native={"stdout":"42","receipt":{"status":"Accepted"}}
        for phase in transfer.PHASES:
            groups[phase,"cell"]={"native":[native]*3,"v12":[deepcopy(row) for _ in range(2)],
                                  "v13":[deepcopy(row) for _ in range(2)]}
        counts=[(60018,4096,4096)]*8
        eligible,metrics=transfer.summarize({"cell":case},groups,counts)
        self.assertTrue(all(eligible.values()))
        self.assertEqual(metrics["exact_paired_completions"],4)
        groups["repeat","cell"]["v13"][0]["receipt"]["work_measurement"]["instructions"]+=129
        counts[-1]=(60017,4096,4096)
        gates,_=transfer.summarize({"cell":case},groups,counts)
        self.assertFalse(gates["terminal_instruction_spread_at_most_128"])
        self.assertFalse(gates["freestanding_counters_exact"])

    def test_unknown_or_reused_binding(self):
        modified=deepcopy(self.frozen)
        modified["candidate"]["policy"]["ns_denominator"]+=1
        with self.assertRaisesRegex(ValueError,"unrecognized frozen clock"):
            clock_for("v13",transfer.CANDIDATE_IMAGE,self.cells,modified,transfer.V13.model_binding)
        with self.assertRaisesRegex(ValueError,"segregate"):
            clock_for("v13",transfer.CANDIDATE_IMAGE,self.cells,self.frozen,lambda *_: ORIGINAL_BINDING)

    def test_release_requires_all_gates_and_strips_diagnostics(self):
        with self.assertRaisesRegex(ValueError,"criteria failed"):
            transfer.candidate_environment({"eligible":False,"gates":{"count":False}}, {})
        c=clock_for("v13",transfer.CANDIDATE_IMAGE,self.cells,self.frozen,transfer.V13.model_binding)
        baseline={"AONOHAKO_DURANTE_JUDGE_CASES":json.dumps({"schema":1,"cases":[
            {"problem_id":p,"source_language":"CPP20","time_ms":ms,
             "stdin_sha256":digest(self.cells[p+"-fast-contest"]["stdin"].encode()),"model_binding":ORIGINAL_BINDING}
            for p,ms in TIMES.items()]})}
        good = {"eligible":True, "gates":dict.fromkeys(transfer.RELEASE_GATES,True),
                "bindings":{"v12":ORIGINAL_BINDING,"v13":c["model_binding"]}}
        for malformed in ({}, {"unknown":True}, {**good["gates"],"unknown":True},
                          {**good["gates"],"ample_held_out_answers":1}):
            with self.subTest(gates=malformed), self.assertRaisesRegex(ValueError,"criteria failed"):
                transfer.candidate_environment({**good,"gates":malformed},c)
        with self.assertRaisesRegex(ValueError,"binding differs"):
            transfer.candidate_environment({**good,"bindings":{"v12":ORIGINAL_BINDING,"v13":ORIGINAL_BINDING}},c)
        with patch.object(transfer.subprocess,"check_output",return_value=json.dumps(baseline).encode()):
            env=transfer.candidate_environment(good,c)
        cases=json.loads(env["AONOHAKO_DURANTE_JUDGE_CASES"])["cases"]
        self.assertEqual({(r["problem_id"],r["time_ms"],r["model_binding"]) for r in cases},
                         {(p,ms,c["model_binding"]) for p,ms in TIMES.items()})

    def test_attached_runtime_patch_must_match(self):
        p={"upstream":"expected","patches":{"lifetime":"patch","replay":"replay"}}
        r={"work_measurement":{"installation":{"source_commit":"expected","runtime_source_commit":"6847bb210bea0ca11d1bf47704383d6a075da490",
                                               "runtime_patch_sha256":"different","runtime_code_replay_patch_sha256":"replay"}}}
        with self.assertRaisesRegex(ValueError,"engine source/patches"):
            transfer.check_install(r,p)


class AutomaticRoutingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.evidence = transfer.HERE / "evidence"
        cls.registration = read(transfer.HERE / "registration.json")
        cls.result, cls.clock = transfer.verify(cls.evidence, cls.registration)
        cls.env = transfer.candidate_environment(cls.result, cls.clock)
        cls.document = json.loads(transfer.READ(cls.evidence, "automatic-routing.json"))

    def replay(self, document):
        transfer.check_automatic_routing(document, self.evidence, self.registration, self.clock, self.env)

    def test_all_recorded_responses_replay(self):
        self.replay(self.document)

    def test_export_requires_stored_route_and_matching_published_results(self):
        with patch.object(transfer, "READ", side_effect=FileNotFoundError):
            with self.assertRaises(FileNotFoundError):
                transfer.operator_environment(self.result, self.clock, self.evidence, self.registration)
        with self.assertRaisesRegex(ValueError, "published transfer results"):
            transfer.operator_environment({**self.result, "panel_http_requests": 7}, self.clock,
                                          self.evidence, self.registration)

    def test_environment_identity_and_complete_request_set(self):
        for field in ("image", "worker_sha256", "model_binding", "env_sha256"):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "worker, clock or environment"):
                self.replay({**self.document, field: "stale"})
        trials = list(self.document["trials"])
        self.assertEqual(trials[-1]["role"], "unregistered")
        with self.assertRaisesRegex(ValueError, "incomplete"):
            self.replay({**self.document, "trials": trials[:-1]})
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.replay({**self.document, "trials": trials[:-1] + [trials[0]]})
        for role in ("wrong", "unregistered"):
            document = deepcopy(self.document)
            row = next(r for r in document["trials"] if r["role"] == role)
            row["request_sha256"] = trials[0]["request_sha256"]
            with self.subTest(role=role), self.assertRaisesRegex(ValueError, "routing request"):
                self.replay(document)

    def test_actual_stdout_and_clock_sources(self):
        document = deepcopy(self.document)
        wrong = next(r for r in document["trials"] if r["role"] == "wrong")
        wrong["stdout"] = "another incorrect answer\n"
        with self.assertRaisesRegex(ValueError, "output differs from the native control"):
            self.replay(document)
        document = deepcopy(self.document)
        slow = next(r for r in document["trials"] if r["role"] == "slow")
        slow["receipt"]["work_measurement_execution"]["termination_source"] = "durante_work_clock_final"
        with self.assertRaisesRegex(ValueError, "did not stop the running submission"):
            self.replay(document)
        document = deepcopy(self.document)
        fallback = next(r for r in document["trials"] if r["role"] == "unregistered")
        fallback["receipt"] = next(r for r in document["trials"] if r["role"] == "fast")["receipt"]
        with self.assertRaisesRegex(ValueError, "native CPU or output"):
            self.replay(document)


if __name__ == "__main__":
    unittest.main()
