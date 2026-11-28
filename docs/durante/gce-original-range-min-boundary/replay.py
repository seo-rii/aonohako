"""Replay both original RMQ clock challenges from the frozen submissions and HTTP receipts."""
import argparse
import json
from pathlib import Path
import re

import prepare

HERE, read, sha = prepare.HERE, prepare.read, prepare.sha
older = prepare.load("rmq_sealed_replay", prepare.ORIGINAL / "v12/replay.py")
newer = prepare.load("rmq_v13_replay", prepare.ORIGINAL / "replay.py")
shared = newer.base
require = older.require
VERSIONS = ("v12", "v13")
FROZEN = "b7a05b55be8259d8519d47eb3ede4c7913584134"
REGISTRATIONS = {
    "v12": "5e4b23059053becd8a3596cccd1bec3b84020370d3e6cfec60c57850ff94c520",
    "v13": "b9e0bc796dc3b978a702eeba446e2e8f60e8c62cbc83f4c717cc2c056ce2ba35",
}


def contract(version, plan):
    if version == "v13":
        clock = shared.bound_clocks(plan, {})["shared"]
        engine = shared.read_clock_plan()
        installed = read(prepare.ORIGINAL / "evidence/primary/platform.json")["files"]
    else:
        clock, installed, engine = older.runtime_contract(plan)
        clock["judge_cases"]["cases"] = [
            dict(problem_id="range_min", source_language="CPP20", stdin_sha256=plan["original_input_sha256"],
                 time_ms=ms, model_binding=clock["model_binding"]) for ms in (183, 30000)
        ]
    require(clock["model_binding"] == plan["baseline_binding"] and engine["image"] == plan["image"]
            and all(installed[k] == v for k, v in engine["files"].items()), "different installed clock")
    return clock, installed, engine


def scale_comparison(version, evidence):
    """Bound a multiplier using the most favorable individual sample from either boot."""
    original = prepare.ORIGINAL / ("v12/evidence" if version == "v12" else "evidence")
    (older if version == "v12" else newer).verify(original)
    bounds, ranges = {}, {}
    for label, root, key in (("range_min/cached64", evidence, "cached64"),
                             ("inversions/hybrid1024", original, "original-hybrid1024")):
        native, estimated = [], []
        for phase in older.PHASES:
            for line in older.checks.evidence(root / phase, "trials.jsonl").splitlines():
                row = json.loads(line)
                if row["case_id"] != key or row["boundary"] != "ample":
                    continue
                receipt = row["receipt"]
                require(receipt["status"] == older.checks.AC, "incomplete scaling observation")
                if row["kind"] == "native":
                    native.append(receipt["cpu_time_ns"])
                elif row["kind"] == "shared":
                    estimated.append(receipt["work_measurement_execution"]["time_estimate"]["nanoseconds"])
        require(len(native) == 6 and len(estimated) == 4, "incomplete scaling panel")
        ranges[label] = dict(native_ns=[min(native), max(native)], estimated_ns=[min(estimated), max(estimated)])
        bounds[label] = [0.8 * min(native) / max(estimated), 1.2 * max(native) / min(estimated)]
    return dict(relative_tolerance=0.2, individual_sample_ranges=ranges, multiplier_intervals=bounds,
                overlap=max(low for low, high in bounds.values()) <= min(high for low, high in bounds.values()))


def verify(version, evidence, payload=None):
    path = HERE / version
    reg = read(path / "registration.json")
    require(sha((path / "registration.json").read_bytes()) == REGISTRATIONS[version], "different registration")
    plan = read(path / "plan.json")
    require(sha((path / "plan.json").read_bytes()) == reg["payload"]["plan.json"]
            and sha((HERE / "variants.cpp").read_bytes()) == plan["build"]["source_sha256"]
            and sha((prepare.PILOT / "corpus/generator.hpp").read_bytes()) == plan["build"]["generator_sha256"],
            "different experiment source")
    parent = prepare.ORIGINAL if version == "v13" else prepare.ORIGINAL / "v12"
    previous = read(parent / "registration.json")["payload"]
    for name, expected in previous.items():
        if name != "plan.json" and not name.startswith("pilot/corpus/"):
            require(reg["payload"][name] == expected, "different inherited payload file " + name)
    for key, c in plan["cases"].items():
        require(reg["payload"][c["binary"]] == c["binary_sha256"] and c["role"] == key
                and c["limits"] == ({"contest":183} if c["split"] == "original" else {"contest":183, "ample":30000})
                and c["problem"] == "range_min" and sha(c["stdin"].encode()) == plan["original_input_sha256"],
                "different registered challenge")
    require(len(plan["cases"]) == 12 and set(plan["cases"]) == {"fast","wrong","slow", *dict(prepare.variants())}
            and len(reg["requests"]) == 42 and reg["expected_rows"] == {"clock":105}, "different matrix")
    if payload is not None:
        require(prepare.prior.request_hashes(plan, payload) == reg["requests"], "different HTTP request hashes")
    infra = read(evidence / "infrastructure.json")
    older.transfer.check_topology(infra, reg)
    for record in infra.values():
        require(record["registration_commit"] == FROZEN and record["guest_exit"] == 0 and
                record["temporary_key_removed"] is True and record["groups"] == reg["expected_rows"]
                and record["trial_count"] == 105 and record["raw_archive_bytes"] > 0
                and re.fullmatch("[0-9a-f]{64}", record["raw_archive_sha256"]), "incomplete GCE lifecycle")
    clock, installed, engine = contract(version, plan)
    if version == "v12":
        phases = {p: older.replay_phase(evidence / p, p, infra[p], plan, reg, clock, installed, engine)
                  for p in older.PHASES}
        controls = all(p["controls"] for p in phases.values())
        outcomes = {p: {k: d["outcome"] for k, d in phases[p]["panel"].items()
                        if plan["cases"][k]["split"] == "variant"} for p in phases}
        failures = older.compare_pair(outcomes["primary"], outcomes["repeat"], controls)
    else:
        groups = {p: shared.verify_guest(evidence, p, plan, reg, {}, infra, reg["requests"]) for p in shared.PHASES}
        summary = newer.summarize(plan, groups)
        controls = summary["controls_passed"]
        failures = summary["repeated_opposition"] if controls else []
        phases = {p: {"panel": panel} for p, panel in summary["panels"].items()}
    return dict(engine=version, clock_binding=clock["model_binding"], original_input_sha256=plan["original_input_sha256"],
                requests_per_boot=105, controls_passed=controls, disqualified=bool(failures), repeated_opposition=failures,
                guests=phases, uniform_rescaling=scale_comparison(version, evidence))


def check_four_boots():
    clouds = [g for version in VERSIONS for g in read(HERE / version / "evidence/infrastructure.json").values()]
    earlier = [g for p in (prepare.ORIGINAL, prepare.ORIGINAL / "v12", prepare.TRANSFER)
               for g in read(p / "evidence/infrastructure.json").values()]
    for field in ("boot_sha256", "instance_id_sha256", "disk_id_sha256"):
        values = {g[field] for g in clouds}
        require(len(values) == 4 and values.isdisjoint(g[field] for g in earlier), "reused guest " + field)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("version", choices=VERSIONS)
    cli.add_argument("--payload", type=Path)
    args = cli.parse_args()
    check_four_boots()
    print(json.dumps(verify(args.version, HERE / args.version / "evidence", args.payload), indent=2, sort_keys=True))
