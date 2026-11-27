"""Exercise the evidence verifier with synthetic counters, placements and decisions."""
from copy import deepcopy
from fractions import Fraction
import unittest

import corpus
import verify as v


ORACLE = v.sha(b"24\n")
OTHER = v.sha(b"23\n")
PLAN = {"installed_files": {"dynamorio/bin64/drrun": "a"*64, "lib/libdurante.so": "b"*64,
                            "dynamorio/lib64/release/libdynamorio.so": "c"*64}}
POLICY = {"instruction_ns_numerator": 3, "rep_byte_ns_numerator": 3, "ns_denominator": 2,
          "dispatch_profile": "default", "method": "weighted-work-nanoseconds-v1"}
FROZEN = {"policy": POLICY, "model_binding": "work-clock-v1:sha256:" + "e"*64, "runtime_options_id": "fixture"}


def report(completed, instructions):
    obj = dict(complete=completed, family="cpp", authority="diagnostic_only", integrity_verified=False,
               engine_profile=v.PROFILE, dispatch_profile="default", installation=dict(
                   unchanged_before_after=True, launcher_sha256="a"*64, client_sha256="b"*64, release_core_sha256="c"*64))
    if completed:
        obj.update(cpuid_profile="x86-64-v2", quality="uninterrupted", processes=1, images=1, signals_observed=0,
                   unsupported_rep_entries=0, rep_telemetry_status="movs_stos_only", instructions=instructions,
                   rep_bytes=4, rep_breakdown=dict(movs_bytes=3, stos_bytes=1))
    return obj


def judged(status):
    live = status == v.TLE
    limit = 1
    count = 800_000 if live else 500_000
    progress = dict(instructions=count, movs_bytes=3, stos_bytes=1)
    clock = dict(version=1, state="observed", metric="model_weighted_nanoseconds_v1",
                 model_binding=FROZEN["model_binding"], budget=limit * v.MILLION, progress=progress)
    cause = "durante_work_clock" if live else "stdout"
    state = "budget_exceeded" if live else "complete"
    env = dict(authority="diagnostic_only", state=state, termination_source=cause, nominal_limits={**v.NOMINAL, "time_ms": limit},
               raw_limits=v.HARD, clock=clock, time_estimate_eligibility={"eligible": not live})
    judgment = dict(nominal_time_ms=limit, integrity_mode="off", authority=env["authority"], verdict=status,
                    termination_source=cause, state=state)
    receipt = dict(status=status, verdict_source="durante_judge", work_measurement_execution=env, work_measurement_judgment=judgment,
                   work_measurement=report(not live, count), cpu_time_ns=1_600_000, wall_time_ms=2, memory_kb=64)
    if not live:
        receipt["exit_code"] = 0
        if status == v.AC:
            receipt["score"] = 1
        env.update(time_estimate={**POLICY, "runtime_options_id": "fixture", "authority": "diagnostic_only",
                   "nanoseconds": (count+4)*3//2, "milliseconds": 1}, nominal_time_exceeded=False, output_matches=status == v.AC)
    return dict(kind="judge", limit_ms=limit, stdout_sha256=ORACLE if status == v.AC else OTHER, receipt=receipt)


def native(ns, status=v.AC):
    return dict(status=status, verdict_source="stdout", cpu_time_ns=ns, cpu_time_ms=-(-ns//v.MILLION), exit_code=0,
                cpu_accounting=dict(selected_source="wait_rusage", process_clock_peak_ns=ns-10, wait_target_ns=ns,
                                    wait_user_ns=ns+10, wait_system_ns=0, rusage_baseline_ns=10))


class EvidenceTest(unittest.TestCase):
    def test_infrastructure_requires_three_private_pinned_instances_and_clean_disks(self):
        observed, infra = {}, {}
        registration = {"archive_sha256": "e"*64}
        for i, phase in enumerate(("calibrate", "primary", "repeat")):
            observed[phase] = dict(boot_sha256=v.sha(f"boot{i}".encode()), trial_count=24 if i == 0 else 90,
                remote_frozen_sha256="b"*64, trials_sha256=v.sha(f"trials{i}".encode()), exec_trace_sha256=v.sha(f"trace{i}".encode()))
            infra[phase] = dict(**observed[phase], zone="asia-northeast3-a", cpu_platform="AMD Milan", threads_per_core=1,
                image="debian-12-bookworm-v20261117", machine_type="n2d-standard-4", disk_auto_delete=True, external_ip=False,
                service_account=False, cleanup_http={"instance":404,"boot_disk":404}, archive_sha256="e"*64,
                instance_id_sha256=v.sha(f"instance{i}".encode()), disk_id_sha256=v.sha(f"disk{i}".encode()))
        v.check_infrastructure(infra, observed, registration)
        for key, value in {"external_ip": True, "service_account": True, "threads_per_core": 2, "cpu_platform": "other",
                           "image": "other", "trials_sha256":"0"*64, "remote_frozen_sha256": "0"*64,
                           "exec_trace_sha256": "0"*64, "boot_sha256": "0"*64, "archive_sha256": "0"*64,
                           "disk_id_sha256": infra["primary"]["disk_id_sha256"],
                           "instance_id_sha256": infra["primary"]["instance_id_sha256"],
                           "cleanup_http":{"instance":404,"boot_disk":200}}.items():
            altered = deepcopy(infra)
            altered["repeat"][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                v.check_infrastructure(altered, observed, registration)

    def test_clock_answers_and_strict_live_stop(self):
        for status in (v.AC, v.WA, v.TLE):
            with self.subTest(status=status):
                v.check_judge(judged(status), FROZEN, PLAN, ORACLE)
        modifications = {
            "wrong oracle": lambda r,e: r.update(stdout_sha256=ORACLE),
            "forged time": lambda r,e: e["time_estimate"].update(nanoseconds=4),
            "different clock": lambda r,e: e["clock"].update(model_binding="other"),
            "different limit": lambda r,e: e["clock"].update(budget=4),
            "forged terminal progress": lambda r,e: e["clock"]["progress"].update(movs_bytes=2),
        }
        for name, edit in modifications.items():
            row = judged(v.WA)
            edit(row, row["receipt"]["work_measurement_execution"])
            with self.subTest(name=name), self.assertRaises(ValueError):
                v.check_judge(row, FROZEN, PLAN, ORACLE)
        for cause in ("wall_time", "cpu_time", "durante_work_clock_final"):
            row = judged(v.TLE)
            row["receipt"]["work_measurement_execution"]["termination_source"] = cause
            row["receipt"]["work_measurement_judgment"]["termination_source"] = cause
            with self.subTest(cause=cause), self.assertRaises(ValueError):
                v.check_judge(row, FROZEN, PLAN, ORACLE)
        row = judged(v.TLE)
        row["receipt"]["work_measurement_execution"]["clock"]["progress"]["instructions"] = 1
        with self.assertRaisesRegex(ValueError, "weighted budget"):
            v.check_judge(row, FROZEN, PLAN, ORACLE)

    def test_native_cpu_and_output(self):
        row = dict(receipt=native(500_000, v.WA), limit_ms=1, stdout_sha256=OTHER)
        self.assertEqual(v.check_native(row, ORACLE), 500_000)
        with self.assertRaisesRegex(ValueError, "verdict or output"):
            v.check_native(row, OTHER)
        row["receipt"]["cpu_accounting"]["wait_target_ns"] -= 1000
        with self.assertRaisesRegex(ValueError, "wait accounting"):
            v.check_native(row, ORACLE)

    def test_schedule_and_kernel_targets(self):
        calibration = v.expected_trials(True)
        self.assertEqual(len(calibration), 24)
        self.assertEqual(len(v.expected_trials(False)), 90)
        rows, windows = [], []
        manifest = {p: {"elf_sha256": {"reference": "0"*64}, "stdin_sha256": "1"*64} for p in corpus.PROBLEMS}
        for i, (p, role, boundary, kind, rep) in enumerate(sorted(calibration)):
            window = [dict(cpu=1, path="/box/Main" if kind == "native" else "/opt/durante/dynamorio/bin64/drrun", pid=301+i)]
            windows.append(window)
            rows.append(dict(sequence=i, problem=p, role=role, kind=kind, boundary=boundary, rep=rep, execs=window,
                             binary_sha256="0"*64, stdin_sha256="1"*64, request_sha256="2"*64, stdout_sha256=ORACLE))
        self.assertEqual(v.index_trials(rows, windows, manifest, True).keys(), calibration)
        with self.assertRaisesRegex(ValueError, "incomplete"):
            v.index_trials(rows[:-1], windows[:-1], manifest, True)
        duplicate = [*rows[:-1], {**rows[-2], "sequence": len(rows)-1}]
        with self.assertRaisesRegex(ValueError, "duplicate"):
            v.index_trials(duplicate, windows, manifest, True)
        altered = deepcopy(windows)
        altered[0][0]["cpu"] = 2
        rows[0]["execs"] = altered[0]
        with self.assertRaisesRegex(ValueError, "CPU 1"):
            v.index_trials(rows, altered, manifest, True)
        with self.assertRaisesRegex(ValueError, "target"):
            v.check_placement(dict(kind="judge", execs=windows[0]), windows[0])

    def test_reference_fit_and_rounding(self):
        trials = {}
        manifest = {p: {"stdout_sha256": ORACLE} for p in corpus.PROBLEMS}
        for i,p in enumerate(corpus.PROBLEMS, start=1):
            for rep in range(1, 6):
                r = native(i*2_000_001 + (rep-3)*50)
                trials[p, "reference", "calibration", "native", rep] = dict(receipt=r, limit_ms=30000, stdout_sha256=ORACLE)
            for rep in range(1, 4):
                m = report(True, 2_000_000-4+rep-2)
                env = dict(state="complete", termination_source="stdout", output_matches=True, nominal_limits=v.NOMINAL,
                           raw_limits=v.HARD, clock=dict(version=1, state="observed", metric="instructions_plus_movs_stos_bytes_v1",
                               budget=(1<<63)-1, progress={"instructions": m["instructions"], "movs_bytes": 3, "stos_bytes": 1}))
                receipt = dict(status="Container Initialization Failed", exit_code=0, work_measurement=m, work_measurement_execution=env)
                trials[p, "reference", "calibration", "raw", rep] = dict(receipt=receipt, limit_ms=30000, stdout_sha256=ORACLE)
        coefficient, cases = v.calibration_fit(trials, manifest, PLAN)
        self.assertEqual(coefficient, Fraction(4_000_002, 2_000_000))
        self.assertEqual([cases[p]["contest_ms"] for p in corpus.PROBLEMS], [10, 17, 25])
        self.assertEqual(cases[corpus.PROBLEMS[1]]["low_ms"], 3)
        self.assertEqual(cases[corpus.PROBLEMS[1]]["high_ms"], 5)
        first = next(k for k in trials if k[3] == "raw")
        trials[first]["receipt"]["work_measurement_execution"]["clock"]["progress"]["instructions"] = 17
        with self.assertRaisesRegex(ValueError, "terminal counts"):
            v.calibration_fit(trials, manifest, PLAN)

    def test_qualification_requires_each_guest_and_each_held_out_class(self):
        cases = {p: dict(contest_ms=100, low_ms=20, high_ms=30) for p in corpus.PROBLEMS}
        frozen = {"cases": cases, "judge_cases": {"cases": [{"problem_id": p, "time_ms": 100} for p in cases]}}
        trials = {}
        for k in v.expected_trials(False):
            p, role, boundary, kind, _ = k
            status = v.EXPECTED[role] if boundary == "contest" else v.AC if boundary == "high" else v.TLE
            if kind == "judge":
                trials[k] = judged(status)
            else:
                ns = 600_000
                r = native(ns, status)
                if status == v.TLE:
                    r.update(verdict_source="cpu_time", exit_code=None)
                trials[k] = dict(kind="native", receipt=r)
        guests = {"primary": trials, "repeat": deepcopy(trials)}
        result = v.summarize(guests, frozen)
        self.assertEqual(result["qualified_contest_cases"], 3)
        p = corpus.PROBLEMS[0]
        self.assertAlmostEqual(result["results"][p]["guests"]["primary"]["fast"]["completed"]["signed_error_pct"], 25.0, places=1)
        guests["repeat"][p,"fast","stress","judge",1] = judged(v.AC)
        self.assertNotIn(p, [r["problem_id"] for r in v.summarize(guests, frozen)["judge_cases"]["cases"]])
        guests["repeat"] = deepcopy(trials)
        for i in (1,2):
            guests["repeat"][p,"slow","contest","judge",i]["receipt"]["work_measurement_execution"]["termination_source"] = "durante_work_clock_final"
        self.assertFalse(v.summarize(guests,frozen)["results"][p]["qualifies"])
        guests["repeat"] = deepcopy(trials)
        guests["repeat"][p,"wrong","contest","native",3]["receipt"]["status"] = v.AC
        self.assertEqual(v.summarize(guests, frozen)["qualified_contest_cases"], 2)


if __name__ == "__main__":
    unittest.main()
