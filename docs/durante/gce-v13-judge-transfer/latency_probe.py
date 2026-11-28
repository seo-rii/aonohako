"""Compare instrumented CPU and work with the raw live page and latency bound active."""
import argparse
import io
import json
import os
from pathlib import Path
from statistics import median

from protocol import ENGINES, digest, read, require, write


PROBLEMS = ("range_min", "shortest_paths", "inversions")
BUDGET = (1 << 63) - 1


def observations(document, manifest, arm):
    """Check complete HTTP observations, including their terminal and live counters."""
    engine, upstream = ENGINES[arm]
    require(document["schema"] == 1 and document["arm"] == arm and
            document["engine"] == engine and document["upstream"] == upstream, "unexpected probe installation")
    values = {}
    for row in document["rows"]:
        problem, rep = row["problem"], row["rep"]
        cell = manifest[problem]
        require(row["binary_sha256"] == cell["elf_sha256"]["fast"] and row["stdin_sha256"] == cell["stdin_sha256"]
                and digest(row["stdout"].encode()) == cell["stdout_sha256"], "different probe case or answer")
        events = row["execs"]
        require(sum(e["path"] == "/opt/durante/dynamorio/bin64/drrun" for e in events) == 1
                and all(e["cpu"] == 1 for e in events), "missing CPU-1 launcher")
        r = row["receipt"]
        m, envelope = r["work_measurement"], r["work_measurement_execution"]
        require(r["exit_code"] == 0 and r["cpu_time_ns"] > 0 and r["cpu_accounting"]["selected_source"]
                and not r.get("cpu_time_sampling") and not r.get("cpu_time_normalization")
                and envelope["state"] == "complete" and envelope["output_matches"] is True
                and envelope["termination_source"] == "stdout" and r["verdict_source"] == "durante_complete",
                "missing CPU accounting or unsuccessful diagnostic")
        installation = m["installation"]
        require(installation["source_commit"] == upstream and installation["unchanged_before_after"] is True
                and m["engine_profile"] == engine and m["family"] == "cpp" and m["cpuid_profile"] == "x86-64-v2"
                and m["complete"] is True and m["quality"] == "uninterrupted" and m["rep_bytes_enabled"] is True
                and m["signals_observed"] == m["unsupported_rep_entries"] == 0 and m["processes"] == m["images"] == 1,
                "different engine or ineligible diagnostic")
        for field, path in (("client_sha256","lib/libdurante.so"), ("launcher_sha256","dynamorio/bin64/drrun"),
                            ("release_core_sha256","dynamorio/lib64/release/libdynamorio.so")):
            require(installation[field] == document["files"][path], "different installed probe executable")
        counts = dict(instructions=m["instructions"], movs_bytes=m["rep_breakdown"]["movs_bytes"],
                      stos_bytes=m["rep_breakdown"]["stos_bytes"])
        clock = envelope["clock"]
        require(clock["version"] == 1 and clock["state"] == "observed" and clock["budget"] == BUDGET and
                clock["metric"] == "instructions_plus_movs_stos_bytes_v1" and clock["progress"] == counts,
                "live page and terminal counts disagree")
        bound = m["latency_bound"]
        require("memory_operand_bytes" not in m and bound["version"] == 1 and
                bound["cycles_x16"] >= 4 * counts["instructions"] > 0, "latency diagnostic options differ")
        key = (problem, rep)
        require(key not in values, "duplicate probe observation")
        values[key] = dict(**counts, cycles_x16=bound["cycles_x16"], cpu_ns=r["cpu_time_ns"])
    require(set(values) == {(p,r) for p in PROBLEMS for r in (1,2)}, "incomplete probe schedule")
    return values


def collect_latency_probe(root, arm, work, out, pilot, trace):
    """Run after the contest schedule inside its CPU-1 scope with that arm still mounted."""
    settings = pilot.measurements()
    settings.update(AONOHAKO_DURANTE_LATENCY_BOUND="v1", AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES="off")
    plan = read(root/"plan.json")["arms"][arm]
    platform = read(out/"platform.json")
    require(os.sched_getaffinity(0) == {1}, "unexpected probe affinity")
    result = dict(schema=1, arm=arm, engine=plan["engine"], upstream=plan["upstream"], files=plan["files"],
                  image=plan["image"], boot_sha256=platform["boot_sha256"], phase=platform["phase"], rows=[])
    corpus = root/"pilot"/"corpus"
    manifest = read(corpus/"manifest.json")
    with pilot.worker("raw",work,out,plan["image"],settings) as (url,pid):
        require(digest(Path(f"/proc/{pid}/exe").read_bytes()) == plan["files"]["server"], "different probe worker")
        for rep, problems in ((1,PROBLEMS),(2,reversed(PROBLEMS))):
            for problem in problems:
                binary = (corpus/(problem+"-fast")).read_bytes()
                stdin, expected = (corpus/(problem+".stdin")).read_bytes(), (corpus/(problem+".stdout")).read_bytes()
                row = dict(problem=problem, rep=rep, binary_sha256=digest(binary), stdin_sha256=digest(stdin))
                require(row["binary_sha256"] == manifest[problem]["elf_sha256"]["fast"] and
                        row["stdin_sha256"] == manifest[problem]["stdin_sha256"] and
                        digest(expected) == manifest[problem]["stdout_sha256"], "changed probe payload")
                req = pilot.request_for(problem,"raw",30000,stdin,expected,binary,None)
                require(req["work_measurement_clock"]["budget"] == BUDGET, "different live probe budget")
                (trace/"trace").write_text("\n")
                row["receipt"], row["stdout"] = pilot.post(url,"/execute",req,timeout=200)
                raw = io.StringIO()
                row["execs"] = pilot.source.collect_execs(trace,raw,len(result["rows"]),"raw")
                row["trace"] = raw.getvalue()
                result["rows"].append(row)
    write(out/"latency.json", result)
    observations(result,manifest,arm)


def compare_latency_probes(v12, v13, manifest):
    """Report paired CPU medians and signed counter differences, including startup variation."""
    require(v12["boot_sha256"] == v13["boot_sha256"] and v12["phase"] == v13["phase"], "probes from different boots")
    before, after = observations(v12,manifest,"v12"), observations(v13,manifest,"v13")
    result = {}
    for problem in PROBLEMS:
        keys = [(problem,1),(problem,2)]
        before_ns = median(before[k]["cpu_ns"] for k in keys)
        after_ns = median(after[k]["cpu_ns"] for k in keys)
        deltas = {field:[after[k][field] - before[k][field] for k in keys]
                  for field in ("instructions","cycles_x16","movs_bytes","stos_bytes")}
        result[problem] = dict(v12_median_cpu_ns=before_ns, v13_median_cpu_ns=after_ns,
                               speedup=before_ns/after_ns, v13_minus_v12=deltas)
    return result


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("v12", type=Path)
    cli.add_argument("v13", type=Path)
    cli.add_argument("manifest", type=Path, help="original three-case corpus/manifest.json")
    args = cli.parse_args()
    print(json.dumps(compare_latency_probes(read(args.v12),read(args.v13),read(args.manifest)), indent=2))
