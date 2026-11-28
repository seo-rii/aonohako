"""Replay both sealed shortest-paths panels against the registered verdict gate."""
import argparse
from collections import Counter, defaultdict
from fractions import Fraction
from statistics import median
import json
from pathlib import Path
import re

from prepare import ENGINES, HERE, PILOT, PREVIOUS, load, prior, read, sha

v12 = load("paths_sealed_v12", PREVIOUS / "v12/replay.py")
v13 = load("paths_sealed_v13", PREVIOUS / "replay.py")
require = v12.require
PHASES = v12.PHASES
FROZEN = "5e6cdfbef314943b588ff33406b1902c902591a3"
DIGEST = "14ffbd39e58719d859260d1d38e8531d0f219471dcff4f53e2bcd390f8e14eb6"


def engine_registration(reg, version):
    prefix = version + "/"
    return dict(payload={name[len(prefix):]: digest for name, digest in reg["payload"].items()
                         if name.startswith(prefix)}, expected_rows={"clock": reg["expected_rows"][version]},
                requests=reg["requests"][version])


def engine_cloud(cloud, version):
    # The inherited verifiers consume one sealed runtime under output/clock.
    return {phase: {**receipt, "files": {"clock/" + name.split("/", 1)[1]: digest
                                         for name, digest in receipt["files"].items() if name.startswith(version + "/")}}
            for phase, receipt in cloud.items()}


def score_v12(root, plan, reg, cloud):
    clock, installed, engine = v12.runtime_contract(plan)
    clock["judge_cases"] = {"schema": 1, "cases": [
        dict(problem_id="shortest_paths", source_language="CPP20", stdin_sha256=plan["original_input_sha256"],
             time_ms=ms, model_binding=clock["model_binding"]) for ms in (65, 30000)]}
    guests = {p: v12.replay_phase(root / p, p, cloud[p], plan, reg, clock, installed, engine) for p in PHASES}
    controls = all(panel["controls"] for panel in guests.values())
    outcomes = [{key: panel["panel"][key]["outcome"] for key, case in plan["cases"].items()
                 if case["split"] == "variant"} for panel in guests.values()]
    opposed = v12.compare_pair(*outcomes, controls)
    return dict(controls_passed=controls, repeated_opposition=opposed, disqualified=bool(opposed),
                panels={p: guests[p]["panel"] for p in PHASES})


def score_v13(root, plan, reg, cloud):
    guests = {p: v13.base.verify_guest(root, p, plan, reg, {}, cloud, reg["requests"]) for p in PHASES}
    return v13.summarize(plan, guests)


def completed_work(directory, plan, result):
    """Summarize all paired readings and test necessary bounds for one scalar."""
    records = defaultdict(list)
    for phase in PHASES:
        data = v12.checks.evidence(directory / phase, "trials.jsonl")
        for line in data.splitlines():
            row = json.loads(line)
            records[row["case_id"]].append(row)
    lower, upper, cases = [], [], {}
    for name, case in plan["cases"].items():
        if case["split"] != "variant":
            continue
        rows = records[name]
        ample = [r for r in rows if r["boundary"] == "ample"]
        native = [r["receipt"]["cpu_time_ns"] for r in ample if r["kind"] == "native"]
        judged = [r["receipt"] for r in ample if r["kind"] == "shared"]
        estimated = [r["work_measurement_execution"]["time_estimate"]["nanoseconds"] for r in judged]
        counts = [sum(v12.transfer.count_tuple(r["work_measurement"])) for r in judged]
        require(len(native) == 6 and len(counts) == 4 and min(native + counts) > 0, "incomplete ample pair")
        actual = [r["receipt"]["status"] for r in rows if r["boundary"] == "contest" and r["kind"] == "native"]
        sources = Counter(r["receipt"]["work_measurement_execution"]["termination_source"] for r in rows
                          if r["boundary"] == "contest" and r["kind"] == "shared")
        cases[name] = dict(native_median_ns=median(native), native_ns_range=[min(native), max(native)],
                           estimated_median_ns=median(estimated), estimated_ns_range=[min(estimated), max(estimated)],
                           full_work_range=[min(counts), max(counts)], contest_clock_endings=dict(sources))
        # Even if this scalar can use the most favorable observed full work,
        # a unanimous native TLE needs a strict crossing and AC must complete.
        budget_ns = case["limits"]["contest"] * 1_000_000
        if set(actual) == {v12.checks.TLE}:
            lower.append((Fraction(budget_ns, max(counts)), name))
        if set(actual) == {v12.checks.AC}:
            upper.append((Fraction(budget_ns, min(counts)), name))
    result["ample"] = cases
    if lower and upper:
        lo, hi = max(lower), min(upper)
        result["necessary_scalar_bounds"] = {"lower_exclusive": {"case": lo[1], "ns_per_work": float(lo[0])},
                                             "upper_inclusive": {"case": hi[1], "ns_per_work": float(hi[0])},
                                             "interval_nonempty": lo[0] < hi[0]}


def verify(directory, payload=None):
    registration = HERE / "registration.json"
    require(sha(registration.read_bytes()) == DIGEST, "registration differs from the frozen commit")
    reg = read(registration)
    require(all(sha((HERE.parent / name).read_bytes()) == digest for name, digest in reg["sources"].items()),
            "registered sources or protocol differ")
    cloud = read(directory / "infrastructure.json")
    v12.transfer.check_topology(cloud, reg)
    for receipt in cloud.values():
        require(receipt["registration_commit"] == FROZEN and receipt["guest_exit"] == 0
                and receipt["groups"] == reg["expected_rows"] == {"v12": 135, "v13": 135}
                and receipt["trial_count"] == sum(receipt["groups"].values()) and receipt["temporary_key_removed"] is True
                and receipt["raw_archive_bytes"] > 0 and re.fullmatch("[a-f0-9]{64}", receipt["raw_archive_sha256"]),
                "incomplete guest lifecycle")
    for historic in (PREVIOUS, PREVIOUS / "v12", HERE.parent / "gce-v13-judge-transfer"):
        before = read(historic / "evidence/infrastructure.json")
        for name in ("boot_sha256", "instance_id_sha256", "disk_id_sha256"):
            require({r[name] for r in cloud.values()}.isdisjoint(b[name] for b in before.values()),
                    "reused earlier instance, disk or boot")
    combined = {}
    for version in ENGINES:
        plan = reg["plans"][version]
        model = engine_registration(reg, version)
        frozen_plan = (json.dumps(plan, indent=2, sort_keys=True) + "\n").encode()
        require(sha(frozen_plan) == model["payload"]["plan.json"], "different registered plan")
        require(all(model["payload"][p["binary"]] == p["binary_sha256"] for p in plan["cases"].values()),
                "different registered submission bytes")
        if payload is not None:
            require(prior.request_hashes(plan, payload / version) == model["requests"], "different HTTP body")
        scorer = score_v12 if version == "v12" else score_v13
        result = scorer(directory / version, plan, model, engine_cloud(cloud, version))
        combined[version] = {k: result[k] for k in ("controls_passed", "disqualified", "repeated_opposition", "panels")}
        completed_work(directory / version, plan, combined[version])
    return dict(schema=1, requests_per_boot=sum(reg["expected_rows"].values()), engines=combined)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--evidence", type=Path, default=HERE / "evidence")
    cli.add_argument("--payload", type=Path)
    args = cli.parse_args()
    print(json.dumps(verify(args.evidence, args.payload), indent=2, sort_keys=True))
