"""Replay the CPU-1 native and deciding-clock observations for the inversions study."""
import argparse
import base64
from collections import Counter, defaultdict
from copy import deepcopy
from fractions import Fraction
from functools import lru_cache
import hashlib
import importlib.util
import json
from pathlib import Path
import re
from statistics import median
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import guest
import plan as design

TRANSFER = HERE.parent / "gce-v13-judge-transfer"
sys.path.insert(0, str(TRANSFER))
spec = importlib.util.spec_from_file_location("inversion_transfer_checks", TRANSFER / "verify.py")
transfer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(transfer)
checks = transfer.V13
require = transfer.require
PHASES = ("primary", "repeat")
ARMS = ("shared", "candidate")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(path.read_bytes())


def compare_panel(rows):
    """Separate mixed trials, false passes and false timeouts before comparing clocks."""
    counts = {arm: Counter() for arm in ARMS}
    cells = {}
    for key, observations in sorted(rows.items()):
        native = [r["receipt"]["status"] for r in observations["native"]]
        cells[key] = {}
        for arm in ARMS:
            judged = [r["receipt"]["status"] for r in observations[arm]]
            outcome = transfer.classify(native, judged)
            cells[key][arm] = outcome
            counts[arm][outcome] += 1
    def comparable(arm):
        return sum(counts[arm][s] for s in ("agreement", "false_pass", "false_timeout", "wrong_completed_answer"))
    nonregression = all(counts["candidate"][s] <= counts["shared"][s] for s in
                        ("false_timeout", "false_pass", "wrong_completed_answer"))
    preserved = all(c["shared"] != "agreement" or c["candidate"] == "agreement" for c in cells.values())
    return dict(outcomes=cells, totals={arm: dict(v) for arm, v in counts.items()},
                comparable={arm: comparable(arm) for arm in ARMS},
                no_direction_increased=nonregression, no_agreements_lost=preserved,
                coverage_preserved=comparable("candidate") >= comparable("shared"))


def read_clock_plan():
    arm = read(TRANSFER / "registration.json")["plan"]["arms"]["v13"]
    pins = read(HERE.parents[2] / "third_party/durante-v13/UPSTREAM.json")
    require(pins["commit"] == arm["upstream"], "different Durante vendor commit")
    return {**arm, "patches": {
        "lifetime": pins["files"]["toolkit/patches/dynamorio-vfork-lifetime.patch"],
        "replay": pins["files"]["toolkit/patches/dynamorio-code-replay.patch"]}}


def thresholds_from_rows(plan, rows):
    result = {}
    for case_id, case in plan["cases"].items():
        if case["split"] != "new":
            continue
        sample = rows[case_id, "ample"]["native"]
        require(len(sample) == 5, "a prospective bracket requires five completed primary native runs")
        m = median(r["receipt"]["cpu_time_ns"] for r in sample)
        result[case_id] = dict(native_median_ns=m, **design.bracket([r["receipt"]["cpu_time_ns"] for r in sample]))
    return result


def completed_times(groups, case_id):
    rows = groups[case_id, "ample"]
    ns = median(r["receipt"]["cpu_time_ns"] for r in rows["native"])
    result = {"native_median_ns": ns}
    for arm in ARMS:
        samples = [r["receipt"]["work_measurement_execution"]["time_estimate"]["nanoseconds"] for r in rows[arm]]
        estimate = median(samples)
        result[arm] = {"estimated_median_ns": estimate, "signed_error_pct": round(100 * (estimate / ns - 1), 3)}
    return result


def scalar_constraints(groups):
    """Necessary scalar bounds, allowing any completed work count observed for each case."""
    lower, upper = [], []
    for (case_id, boundary), arms in groups.items():
        if boundary not in ("low", "high"):
            continue
        verdicts = {r["receipt"]["status"] for r in arms["native"]}
        if len(verdicts) != 1:
            continue
        timed_out = verdicts == {checks.TLE}
        work = []
        for arm in ARMS:
            for row in groups[case_id, "ample"][arm]:
                report = row["receipt"]["work_measurement"]
                work.append(report["instructions"] + report["rep_bytes"])
        # Give the scalar its most favorable observed count at each boundary.
        target = max(work) if timed_out else min(work)
        ms = arms["native"][0]["time_ms"]
        limit = Fraction(ms * checks.MILLION, target)
        (lower if timed_out else upper).append((limit, case_id, boundary, ms, target))
    def describe(item):
        k, case, boundary, ms, work = item
        return dict(case_id=case, boundary=boundary, time_ms=ms, work=work,
                    ns_per_work=float(k), numerator=k.numerator, denominator=k.denominator)
    lo, hi = max(lower), min(upper)
    return dict(lower_exclusive=describe(lo), upper_inclusive=describe(hi),
                interval_nonempty=lo[0] < hi[0])


def score(plan, guests):
    panels, times = {}, {}
    gates = {"original_contest_and_stress": True, "reference_boundaries": True,
             "historical_no_regression": True, "historical_strict_improvement": False,
             "prospective_no_regression": True}
    for phase, groups in guests.items():
        panels[phase], times[phase] = {}, {}
        split_rows = defaultdict(dict)
        for (case_id, boundary), answers in groups.items():
            case = plan["cases"][case_id]
            if boundary in ("low", "high"):
                split_rows[case["split"]][case_id + "/" + boundary] = answers
            elif boundary == "ample":
                times[phase][case_id] = completed_times(groups, case_id)
            elif boundary in ("contest", "stress"):
                status = checks.TLE if boundary == "stress" or case["role"] == "slow" else checks.EXPECTED[case["role"]]
                for arm in ("native", *ARMS):
                    for record in answers[arm]:
                        r = record["receipt"]
                        gates["original_contest_and_stress"] &= (r["status"] == status and
                            (status != checks.TLE or arm == "native" or
                             r["work_measurement_execution"]["termination_source"] == "durante_work_clock"))
        for split in ("original", "historical", "new"):
            panel = panels[phase][split] = compare_panel(split_rows[split])
            gate = {"original":"reference_boundaries", "historical":"historical_no_regression",
                    "new":"prospective_no_regression"}[split]
            gates[gate] &= panel["no_direction_increased"] and panel["coverage_preserved"]
            if split == "new":
                gates[gate] &= panel["no_agreements_lost"]
    def historical_errors(arm):
        return sum(panels[p]["historical"]["totals"][arm].get(s, 0) for p in PHASES for s in
                   ("false_pass", "false_timeout", "wrong_completed_answer"))
    gates["historical_strict_improvement"] = historical_errors("candidate") < historical_errors("shared")
    return dict(gates=gates, candidate_passed=all(gates.values()), panels=panels, ample_times=times)


def bound_clocks(plan, thresholds):
    original = read(transfer.PILOT / "refit/freeze.json")["candidate"]
    clocks = {}
    for arm in ARMS:
        policy = guest.clock_policy(plan, original["policy"], arm)
        options = original["runtime_options_id"]
        binding = checks.model_binding(policy, options)
        require((binding == plan["baseline_binding"]) == (arm == "shared"), "clock not bound to its own coefficients")
        registry = deepcopy(original["time_models"])
        profile, = registry["profiles"]
        model, = registry["models"]
        profile.update(runtime_image=plan["image"], engine_profile=policy["engine_profile"], model_id=policy["model_id"])
        model.update(model_id=policy["model_id"], reference_id=policy["reference_id"],
                     coefficients={k: policy[k] for k in model["coefficients"]})
        cases = sorted({(plan["cases"][key]["problem"], sha(plan["cases"][key]["stdin"].encode()), ms)
                        for key, _, ms in guest.cells(plan, thresholds)})
        scope = {"schema": 1, "cases": [dict(problem_id=name, source_language="CPP20", stdin_sha256=stdin,
                                            time_ms=ms, model_binding=binding) for name, stdin, ms in cases]}
        clocks[arm] = dict(policy=policy, runtime_options_id=options, model_binding=binding,
                           time_models=registry, judge_cases=scope)
    return clocks


def request_hashes(plan, thresholds, payload):
    """Build request digests independently from the authenticated contestant binaries."""
    frozen = bound_clocks(plan, thresholds)
    @lru_cache(maxsize=None)
    def executable(name, expected):
        data = (payload / name).read_bytes()
        require(sha(data) == expected, "contestant executable differs from registration")
        return base64.b64encode(data).decode()
    result = {}
    for case_id, boundary, ms in guest.cells(plan, thresholds):
        c = plan["cases"][case_id]
        req = dict(problem_id=c["problem"], lang="CPP20",
                   binaries=[dict(name="Main", mode="exec", data_b64=executable(c["binary"], c["binary_sha256"]))],
                   stdin=c["stdin"], expected_stdout=c["expected_stdout"],
                   limits={**checks.NOMINAL, "time_ms":ms}, emit_logs=True)
        for arm in ("native", *ARMS):
            body = dict(req)
            if arm != "native":
                body.update(work_measurement=True, work_measurement_limits=checks.HARD,
                            work_measurement_time=True, work_measurement_judge=True, work_measurement_judge_case_required=True,
                            work_measurement_judge_clock_binding=frozen[arm]["model_binding"])
            result["/".join((case_id, boundary, arm))] = sha(json.dumps(body).encode())
    return result


def verify_guest(root, phase, plan, reg, thresholds, infrastructure, hashes):
    files = {n: checks.evidence(root/phase, n) for n in
             ("platform.json", "trials.jsonl", "thresholds.json", "exec-trace.txt")}
    cloud = infrastructure[phase]
    require(all(sha(v) == cloud["files"]["clock/" + k] for k, v in files.items()), "changed saved clock evidence")
    require(json.loads(files["thresholds.json"]) == thresholds, "different deadlines on the repeat boot")
    host = json.loads(files["platform.json"])
    installation = read_clock_plan()
    clocks = bound_clocks(plan, thresholds)
    require(host["schema"] == 1 and host["phase"] == phase and host["affinity"] == [1]
            and host["cpuset"] == host["siblings"] == "1" and host["boot_sha256"] == cloud["boot_sha256"]
            and host["plan_sha256"] == reg["payload"]["plan.json"] and host["image"] == plan["image"]
            and host["thresholds_sha256"] == sha(files["thresholds.json"])
            and host["clocks"] == clocks and 0 <= host["steal_ticks_before"] <= host["steal_ticks_after"],
            "guest placement, effective model or thresholds differ")
    installed = read(TRANSFER/"evidence/builder.json")["installed_sha256"]
    require(host["files"] == installed, "different installed runtime")
    groups = defaultdict(lambda: defaultdict(list))
    rows = [json.loads(line) for line in files["trials.jsonl"].splitlines()]
    windows = checks.read_windows(files["exec-trace.txt"])
    schedule = list(guest.schedule(plan, phase, thresholds))
    require(len(rows) == len(schedule) == len(windows) == host["trials"] == reg["expected_rows"]["clock"],
            "incomplete HTTP or kernel trace schedule")
    for i, (r, window, key) in enumerate(zip(rows, windows, schedule, strict=True)):
        case_id, boundary, arm, rep, ms = key
        c = plan["cases"][case_id]
        require(r["sequence"] == i and (r["case_id"],r["boundary"],r["kind"],r["rep"],r["time_ms"]) == key
                and r["binary_sha256"] == c["binary_sha256"] and r["stdin_sha256"] == sha(c["stdin"].encode())
                and r["request_sha256"] == hashes["/".join((case_id,boundary,arm))], "unexpected request or order")
        row = {**r, "limit_ms":ms, "stdout_sha256":sha(r["stdout"].encode())}
        checks.check_placement(row, window)
        oracle = sha(c["expected_stdout"].encode())
        receipt = r["receipt"]
        if arm == "native":
            checks.check_native(row, oracle)
        else:
            transfer.check_measured_cpu(receipt)
            transfer.check_install(receipt, installation)
            checks.check_judge(row, clocks[arm], {"installed_files": installation["files"]}, oracle)
        if receipt["status"] != checks.TLE:
            require(r["stdout"] == c["solver_stdout"], "completed program returned a different output")
        if boundary == "ample":
            require(receipt["status"] == (checks.AC if c["solver_stdout"] == c["expected_stdout"] else checks.WA),
                    "ample run failed to complete with the expected verdict")
        groups[case_id,boundary][arm].append(r)
    return groups


def verify(root, payload=None):
    plan = read(HERE/"plan.json")
    reg = read(HERE/"registration.json")
    require(plan == design.build_registration(), "different coefficient, inputs, oracles or promotion criteria")
    for name in ("plan.json", "plan.py", "guest.py", "guest.sh", "memory_probe.py", "table.cpp"):
        require(reg["payload"][name] == sha((HERE/name).read_bytes()), "different registered source " + name)
    thresholds = json.loads(checks.evidence(root/"primary", "thresholds.json"))
    hashes = read(root/"request_hashes.json")
    expected_keys = {"/".join((key, boundary, arm)) for key, boundary, _ in guest.cells(plan, thresholds)
                     for arm in ("native", *ARMS)}
    require(set(hashes) == expected_keys and all(re.fullmatch("[0-9a-f]{64}", v) for v in hashes.values()),
            "different request digest index")
    if payload is not None:
        require(hashes == request_hashes(plan, thresholds, payload), "request digest index differs from actual payload")
    infra = read(root/"infrastructure.json")
    transfer.check_topology(infra, reg)
    threshold_digest = sha(checks.evidence(root/"primary", "thresholds.json"))
    for phase, cloud in infra.items():
        require(cloud["groups"] == reg["expected_rows"] and cloud["trial_count"] == sum(reg["expected_rows"].values())
                and cloud["guest_exit"] == 0 and cloud["temporary_key_removed"] is True
                and cloud["raw_archive_bytes"] > 0 and re.fullmatch("[0-9a-f]{64}", cloud["raw_archive_sha256"])
                and cloud["registration_commit"] == "a5a98052e36d7b0dd5e88d3e5fb2c230e51c8695"
                and cloud["files"]["clock/thresholds.json"] == threshold_digest
                and cloud["threshold_source_sha256"] == (threshold_digest if phase == "repeat" else None),
                "guest lifecycle or frozen deadlines differ")
    historical = read(TRANSFER/"evidence/infrastructure.json")
    for field in ("instance_id_sha256","disk_id_sha256","boot_sha256"):
        require({a[field] for a in infra.values()}.isdisjoint(h[field] for h in historical.values()), "reused v13 guest")
    groups = {p:verify_guest(root, p, plan, reg, thresholds, infra, hashes) for p in PHASES}
    require(thresholds == thresholds_from_rows(plan, groups["primary"]), "deadlines do not follow primary controls")
    result = score(plan, groups)
    result.update(schema=1, scalar_bounds={p: scalar_constraints(groups[p]) for p in PHASES},
                  requests_per_boot=reg["expected_rows"]["clock"], prospective_brackets=thresholds,
                  model_bindings={arm:c["model_binding"] for arm,c in bound_clocks(plan, thresholds).items()})
    return result


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--evidence", type=Path, default=HERE/"evidence")
    cli.add_argument("--payload", type=Path, help="also reconstruct HTTP bodies from registered ELFs")
    args = cli.parse_args()
    print(json.dumps(verify(args.evidence, args.payload), indent=2, sort_keys=True, allow_nan=False))
