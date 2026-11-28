"""Validate the sealed v12 worker's kernel traces, responses and repeatability."""
import argparse
from collections import Counter, defaultdict
import importlib.util
import json
from pathlib import Path
import re
from statistics import median
import sys

HERE = Path(__file__).resolve().parent


def load(label, path):
    spec = importlib.util.spec_from_file_location(label, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


design = load("v12_exact_input_design", HERE / "prepare.py")
fixed = load("v12_exact_input_schedule", HERE.parent / "guest.py")
# The transfer verifier's protocol is distinct from the same basename in the parent study.
modules = ("protocol", "audit_transfer_receipts", "corpus")
saved_modules = {k: sys.modules.pop(k) for k in modules if k in sys.modules}
saved_path = sys.path[:]
try:
    sys.path.insert(0, str(design.TRANSFER))
    transfer = load("v12_exact_transfer", design.TRANSFER / "verify.py")
finally:
    sys.path[:] = saved_path
    for key in modules:
        sys.modules.pop(key, None)
    sys.modules.update(saved_modules)
checks = transfer.V12
require = transfer.require
read, sha = design.read, design.sha
PHASES = ("primary", "repeat")
REGISTRATION = "cd0ab24c8a733997c892e5d9a6615e6ad19fc98594584483a086a04c3cf1134f"
COMMIT = "fe601e8110c2b731a2b5affd5dd71951e3cf5355"


def compare_pair(primary, repeat, controls):
    return [dict(case=name, direction=first) for name, first in sorted(primary.items())
            if controls and first in ("false_pass", "false_timeout") and repeat[name] == first]


def replay_phase(directory, phase, cloud, plan, reg, clock, installed, engine):
    names = ("trials.jsonl", "thresholds.json", "exec-trace.txt", "platform.json")
    data = {name: checks.evidence(directory, name) for name in names}
    require(all(sha(blob) == cloud["files"]["clock/" + name] for name, blob in data.items()), "changed guest evidence")
    host = json.loads(data["platform.json"])
    require(data["thresholds.json"] == b"{}\n" and host["thresholds_sha256"] == reg["payload"]["primary-thresholds.json"]
            and sha(data["thresholds.json"]) == host["thresholds_sha256"], "different fixed-time control")
    require(host["schema"] == 1 and host["phase"] == phase and host["boot_sha256"] == cloud["boot_sha256"]
            and host["affinity"] == [1] and host["cpuset"] == host["siblings"] == "1" and host["files"] == installed
            and host["image"] == plan["image"] and host["plan_sha256"] == reg["payload"]["plan.json"]
            and host["clocks"] == {"shared": clock} and 0 <= host["steal_ticks_before"] <= host["steal_ticks_after"],
            "unexpected v12 worker, placement or effective clock")
    rows = [json.loads(line) for line in data["trials.jsonl"].splitlines()]
    windows = checks.read_windows(data["exec-trace.txt"])
    schedule = list(fixed.schedule(plan, phase, {}))
    require(len(rows) == len(windows) == len(schedule) == host["trials"] == reg["expected_rows"]["clock"],
            "incomplete request schedule")
    groups = defaultdict(lambda: defaultdict(list))
    for i, (row, window, expected) in enumerate(zip(rows, windows, schedule, strict=True)):
        case_id, boundary, kind, rep, ms = expected
        case = plan["cases"][case_id]
        require(i == row["sequence"] and tuple(row[k] for k in ("case_id","boundary","kind","rep","time_ms")) == expected
                and row["binary_sha256"] == case["binary_sha256"] and row["stdin_sha256"] == plan["original_input_sha256"]
                and row["request_sha256"] == reg["requests"][f"{case_id}/{boundary}/{kind}"],
                "different HTTP body, participant or execution order")
        row.update(limit_ms=ms, stdout_sha256=sha(row["stdout"].encode()))
        checks.check_placement(row, window)
        oracle = sha(case["expected_stdout"].encode())
        receipt = row["receipt"]
        if kind == "native":
            checks.check_native(row, oracle)
        else:
            transfer.check_measured_cpu(receipt)
            transfer.check_install(receipt, engine)
            checks.check_judge(row, clock, {"installed_files": installed}, oracle)
        if receipt["status"] != checks.TLE:
            require(row["stdout"] == case["solver_stdout"], "different completed solver output")
        groups[case_id, boundary][kind].append(receipt)
    panel, controls = {}, True
    for key, case in sorted(plan["cases"].items()):
        group = groups[key, "contest"]
        native = [r["status"] for r in group["native"]]
        shared = [r["status"] for r in group["shared"]]
        outcome = transfer.classify(native, shared)
        stats = panel[key] = dict(native=dict(Counter(native)), clock=dict(Counter(shared)), outcome=outcome)
        if case["role"] in checks.EXPECTED:
            controls &= set(native + shared) == {checks.EXPECTED[case["role"]]}
            if case["role"] == "slow":
                controls &= all(r["work_measurement_execution"]["termination_source"] == "durante_work_clock"
                                for r in group["shared"])
        else:
            ample = groups[key, "ample"]
            controls &= all(r["status"] == checks.AC for kind in ("native","shared") for r in ample[kind])
            if all(r["status"] == checks.AC for r in ample["shared"]):
                estimates = [r["work_measurement_execution"]["time_estimate"]["nanoseconds"] for r in ample["shared"]]
                stats["ample_estimated_median_ns"] = median(estimates)
            stats["ample_native_median_ns"] = median(r["cpu_time_ns"] for r in ample["native"])
    return dict(controls=controls, panel=panel, cpu_steal_ticks=host["steal_ticks_after"] - host["steal_ticks_before"])


def runtime_contract(plan):
    previous = read(design.PILOT/"refit/freeze.json")["candidate"]
    clock = {k: previous[k] for k in ("policy","runtime_options_id","model_binding","time_models")}
    require(checks.model_binding(clock["policy"], clock["runtime_options_id"]) == clock["model_binding"]
            == plan["baseline_binding"], "wrong original v12 model binding")
    clock["judge_cases"] = dict(schema=1, cases=[dict(problem_id="inversions", source_language="CPP20",
        stdin_sha256=plan["original_input_sha256"], time_ms=ms, model_binding=clock["model_binding"]) for ms in (203, 30000)])
    installed = read(design.PILOT/"registration.json")["plan"]["installed_files"]
    engine = read(design.TRANSFER/"registration.json")["plan"]["arms"]["v12"]
    pins = read(design.TRANSFER.parents[2]/"third_party/durante-v12/UPSTREAM.json")["files"]
    engine["patches"] = dict(lifetime=pins["toolkit/patches/dynamorio-vfork-lifetime.patch"],
                             replay=pins["toolkit/patches/dynamorio-code-replay.patch"])
    return clock, installed, engine


def verify(directory, payload=None):
    reg = read(HERE / "registration.json")
    plan = read(HERE / "plan.json")
    require(sha((HERE/"registration.json").read_bytes()) == REGISTRATION and plan == design.plan()
            and sha((HERE/"plan.json").read_bytes()) == reg["payload"]["plan.json"]
            and sha((HERE/"guest.py").read_bytes()) == reg["payload"]["guest.py"], "different preregistration")
    require(all(c["binary_sha256"] == reg["payload"][c["binary"]] for c in plan["cases"].values()), "different ELF")
    require(set(reg["requests"]) == {f"{key}/{bound}/{kind}" for key, cs in plan["cases"].items()
                                     for bound in cs["limits"] for kind in ("native","shared")}, "different request set")
    if payload is not None:
        require(design.request_hashes(plan, payload) == reg["requests"], "HTTP request differs from original bytes")
    cloud = read(directory / "infrastructure.json")
    transfer.check_topology(cloud, reg)
    for prior in (design.TRANSFER/"evidence", HERE.parent/"evidence", design.V13.parent/"gce-inversion-clock/evidence"):
        earlier = read(prior/"infrastructure.json")
        for identity in ("instance_id_sha256","disk_id_sha256","boot_sha256"):
            require({v[identity] for v in cloud.values()}.isdisjoint(r[identity] for r in earlier.values()),
                    "reused previous experiment guest")
    for h in cloud.values():
        require(h["groups"] == {"clock":35} and h["trial_count"] == 35 and h["guest_exit"] == 0
                and h["temporary_key_removed"] is True and h["registration_commit"] == COMMIT
                and h["raw_archive_bytes"] > 0 and re.fullmatch("[a-f0-9]{64}", h["raw_archive_sha256"]),
                "incomplete v12 guest lifecycle")
    clock, installed, engine = runtime_contract(plan)
    guests = {p: replay_phase(directory/p, p, cloud[p], plan, reg, clock, installed, engine) for p in PHASES}
    def outcomes(phase):
        return {k: guests[phase]["panel"][k]["outcome"] for k,c in plan["cases"].items() if c["split"] == "variant"}
    controls = all(g["controls"] for g in guests.values())
    repeated = compare_pair(outcomes("primary"), outcomes("repeat"), controls)
    return dict(schema=1, controls_passed=controls, disqualified=bool(repeated), repeated_opposition=repeated,
                original_input_sha256=plan["original_input_sha256"], clock_binding=clock["model_binding"],
                requests_per_boot=35, guests=guests)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--evidence", type=Path, default=HERE/"evidence")
    cli.add_argument("--payload", type=Path)
    args = cli.parse_args()
    print(json.dumps(verify(args.evidence, args.payload), indent=2, sort_keys=True))
