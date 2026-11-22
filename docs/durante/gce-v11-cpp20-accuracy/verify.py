#!/usr/bin/env python3
"""Verify frozen CPP20 inputs, placement and original receipts, then replay the v11 accuracy study."""
import argparse
from collections import Counter
from fractions import Fraction
import gzip
import hashlib
from itertools import permutations
import json
from pathlib import Path
import sys
import tarfile

from models import evaluate, load, require, verify_inputs

STUDY = Path(__file__).resolve().parent
ROOT = STUDY.parents[2]
# Reuse the repository's native-timing bound, including its rounding allowance.
sys.path.insert(0, str(ROOT / "scripts"))
from durante_native_envelope import pair_bound

PROTOCOL_SHA256 = "a934f7f00ba3212be94018a10b37f503449b94bbb37faafd332e7c0c5ed2989e"


def sha(data):
    return hashlib.sha256(data).hexdigest()


def archive_files(path):
    # Read regular members directly. Duplicate names cannot replace evidence.
    with tarfile.open(path, "r:xz") as archive:
        members = {}
        for entry in archive:
            if entry.isdir():
                continue
            require(entry.isfile() and entry.name not in members, f"{path.name}: invalid member {entry.name}")
            members[entry.name] = archive.extractfile(entry).read()
    return members


def verify_fixtures(study, protocol):
    artifacts = protocol["artifacts"]
    build = json.loads((study / "corpus/build.json").read_bytes())
    cases = json.loads((study / "corpus/cases.json").read_bytes())
    for name in ("analysis-plan.json", "witness-plan.json", "bench.py", "guest.sh", "corpus/build.json", "corpus/cases.json"):
        require(sha((study / name).read_bytes()) == artifacts[name], f"{name}: frozen file differs")
    binaries = archive_files(study / "corpus/elfs.tar.xz")
    require(set(binaries) == {"bin/" + p for p in build["executables"]}, "different benchmark ELF set")
    for program, digest in build["executables"].items():
        member = "bin/" + program
        require(sha(binaries[member]) == digest == artifacts["corpus/" + member], f"{program}: ELF differs")
        filename = program + ".cpp"
        source = (ROOT / build["reference_source_directory"] if program in build["reference_programs"]
                  else study / "corpus/sources") / filename
        require(sha(source.read_bytes()) == build["sources"][program] == artifacts["corpus/sources/" + filename],
                f"{program}: source differs")
    for row, frozen in zip(cases, protocol["cases"], strict=True):
        name = row["program"]
        require(all(row[k] == frozen[k] for k in ("case_id", "program", "split", "scale", "binary_sha256", "source_sha256"))
                and row["source_sha256"] == build["sources"][name] and row["binary_sha256"] == build["executables"][name]
                and sha(row["stdin"].encode()) == row["input_sha256"] == frozen["stdin_sha256"]
                and sha(row["stdout"].encode()) == row["expected_sha256"] == frozen["stdout_sha256"],
                f"{row['case_id']}: corpus or oracle differs")


def verify_receipt(encoded, row, root_pid):
    require(sha(encoded) == row["report_sha256"], "CLI receipt digest differs")
    report = json.loads(encoded)
    work, execution = report["measurement"], report["execution"]
    require(execution["state"] == "complete" and execution["cleanup_complete"] and execution["records_cleanup_complete"]
            and execution["exit_code"] == 0 and execution["signal"] is None
            and execution["root_pid"] == work["root_pid"] == root_pid, "invalid metered process or exit")
    require(report["authority"] == work["authority"] == "diagnostic_only"
            and report["integrity_verified"] is False and work["integrity_verified"] is False
            and work["measurement_contract"]["trusted_verdict_eligible"] is False
            and work["complete"] and work["valid"] and work["process_count"] == work["image_count"] == 1
            and work["quality"] == "uninterrupted" and work["signals_observed"] == 0
            and work["cpuid_profile"] == "x86-64-v2", "invalid coverage or measurement contract")
    rep, latency, memory = work["rep_breakdown"], work["latency_bound"], work["memory_operand_bytes"]
    require(rep["version"] == latency["version"] == memory["version"] == 1
            and work["rep_bytes_enabled"] and work["rep_telemetry_status"] == "movs_stos_only"
            and work["unsupported_rep_entries"] == 0, "counter capability mismatch")
    counts = dict(instructions=work["instructions"], rep_bytes=work["rep_bytes"], movs_bytes=rep["movs_bytes"],
                  stos_bytes=rep["stos_bytes"], cycles_x16=latency["cycles_x16"], memory_operand_bytes=memory["bytes"])
    require(all(v == row[k] for k, v in counts.items()), "counter totals differ from the original CLI receipt")


def verify_execution(study, data, protocol):
    placement, _ = load(study / "placement.json.gz")
    receipts = archive_files(study / "reports.tar.xz")
    target, actual = protocol["platform_target"], data["platform"]
    require(all(actual[k] == target[k] for k in ("machine_type", "zone", "cpu_platform", "image", "threads_per_core"))
            and actual["cpu"] == target["benchmark_cpu"] == 1 and actual["affinity"] == [1]
            and actual["cgroup_allowed_cpus"] == "1" and placement["protocol_sha256"] == PROTOCOL_SHA256
            and placement["max_trace_overrun"] == placement["max_trace_dropped_events"] == 0, "CPU placement mismatch")
    placements = placement["trials"]
    require(len(placements) == len(protocol["schedule"]), "missing placement evidence")
    seen_pids = set()
    roles = Counter()
    for case in data["cases"]:
        for kind, rows in (("warmup", [case["warmup"]]), ("native", case["native"]), ("metered", case["metered"])):
            for row in rows:
                index = row["sequence"]
                trial = placements[index]
                require(trial["sequence"] == index and trial["case_id"] == case["case_id"]
                        and trial["kind"] == kind and trial["rep"] == row["rep"], "placement trial differs")
                expected = ({"supervisor": ["durante"], "metered_root": ["drrun", "libdynamorio.so"]}
                            if kind == "metered" else {"native": [case["program"]]})
                require(set(trial["roles"]) == set(expected), "unexpected traced role")
                for role, names in expected.items():
                    actor = trial["roles"][role]
                    require(actor["pid"] not in seen_pids
                            and actor["events"] == [{"cpu": 1, "executable": name} for name in names],
                            "inconsistent process placement")
                    seen_pids.add(actor["pid"])
                    roles[role] += 1
                if kind == "metered":
                    name = f"reports/{index:03}.json"
                    require(row["report_path"] == name and name in receipts, "missing metered receipt")
                    verify_receipt(receipts.pop(name), row, trial["roles"]["metered_root"]["pid"])
    require(not receipts, "unexpected metered receipts")
    require((actual["native_execs"], actual["supervisors"], actual["metered_roots"]) ==
            (roles["native"], roles["supervisor"], roles["metered_root"])
            and actual["max_trace_overrun"] == placement["max_trace_overrun"]
            and actual["max_trace_dropped_events"] == placement["max_trace_dropped_events"]
            and type(actual["cpu_steal_ticks"]) is int and actual["cpu_steal_ticks"] >= 0,
            "inconsistent platform summary")
    cleanup, _ = load(study / "cleanup.json")
    require(cleanup["instance_http"] == cleanup["boot_disk_http"] == 404
            and cleanup["key_removed"] is True, "cleanup evidence is incomplete")



def compare_http(study, data, artifacts, durante_commit):
    cases = {case["case_id"]: case for case in data["cases"] if case["scale"] == 1}
    rows = json.loads((study / "embedded-http.json").read_bytes())
    require(len(rows) == len(cases) and {r["case_id"] for r in rows} == set(cases),
            "embedded HTTP cases differ")
    columns = {"instructions": "instructions", "rep_bytes": "rep_bytes", "movs_bytes": "movs_bytes",
               "stos_bytes": "stos_bytes", "latency_x16": "cycles_x16", "operand_bytes": "memory_operand_bytes"}
    comparison = {}
    for row in rows:
        case = cases[row["case_id"]]
        inst = row["installation"]
        require(row["accepted"] is True and row["input_sha256"] == case["stdin_sha256"]
                and row["output_sha256"] == case["stdout_sha256"] and row["binary_sha256"] == case["binary_sha256"],
                "embedded execution used a different artifact or outcome")
        require(row["cpuid_profile"] == "x86-64-v2" and row["engine_profile"] == "dynamorio-11.3-durante-v11-vm32m"
                and inst["source_commit"] == durante_commit and inst["unchanged_before_after"] is True
                and inst["client_sha256"] == artifacts["v11.so"] and inst["launcher_sha256"] == artifacts["dynamorio/bin64/drrun"]
                and inst["release_core_sha256"] == artifacts["dynamorio/lib64/release/libdynamorio.so"],
                "embedded execution used a different runtime")
        comparison[row["case_id"]] = {
            target: [row[source] - r[target] for r in case["metered"]] for source, target in columns.items()
        }
    return comparison

def error_bounds(data):
    # The ratio of each column is invariant under a positive change of its unit.
    models = {"instruction_rep": ("instructions", "rep_bytes"),
              "instruction_movs_stos": ("instructions", "movs_bytes", "stos_bytes"),
              "latency": ("cycles_x16",), "latency_memory": ("cycles_x16", "memory_operand_bytes")}
    heldout = sorted((c for c in data["cases"] if c["split"] == "heldout"), key=lambda c: c["case_id"])
    result = {}
    for name, cols in models.items():
        vectors = {c["case_id"]: {"native": [r["cpu_us"] * 1000 for r in c["native"]],
                                  "features": [[r[k] for k in cols] for r in c["metered"]]} for c in heldout}
        best, witness = Fraction(0), None
        for a, b in permutations(heldout, 2):
            if a["program"] == b["program"]:
                continue
            slow, fast = a["case_id"], b["case_id"]
            bound = pair_bound(vectors[slow], vectors[fast])
            if bound is not None and bound[0] > best:
                best, details = bound
                witness = dict(slow_case=slow, fast_case=fast, **details)
        fixed = pair_bound(vectors["cycle_walk/2"], vectors["cycle_walk/1"])
        result[name] = {"strongest_between_programs": witness, "cycle_walk_2_vs_1": fixed[1] if fixed else None}
    return result


def replay(study):
    protocol, protocol_sha = load(study / "protocol.json")
    data, data_sha = load(study / "measurements.json.gz")
    plan, plan_sha = load(study / "analysis-plan.json")
    require(protocol_sha == PROTOCOL_SHA256, "collection protocol differs from the pre-measurement commit")
    verify_fixtures(study, protocol)
    verify_inputs(data, protocol, plan_sha, protocol_sha)
    verify_execution(study, data, protocol)
    result = evaluate(data, plan)
    result.update(analysis_plan_sha256=plan_sha, protocol_sha256=protocol_sha, dataset_sha256=data_sha,
                  coefficient_independent_bounds=error_bounds(data),
                  embedded_minus_gce_counts=compare_http(study, data, protocol["artifacts"], protocol["durante_commit"]))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--study", type=Path, default=STUDY)
    parser.add_argument("--write", action="store_true", help="write the verified evaluation")
    args = parser.parse_args()
    result = replay(args.study)
    path = args.study / "evaluation.json.gz"
    if args.write:
        path.write_bytes(gzip.compress((json.dumps(result, indent=2, allow_nan=False) + "\n").encode(), mtime=0))
    else:
        require(load(path)[0] == result, "evaluation differs from replay")
        print("Verified 21 cases, 210 pinned executions, 42 original receipts, both model comparisons and pair bounds.")


if __name__ == "__main__":
    main()
