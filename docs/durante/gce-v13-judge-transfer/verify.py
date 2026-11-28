"""Replay both guests' kernel traces and real deciding HTTP receipts."""
import argparse
from collections import Counter, defaultdict
import importlib.util
import json
from pathlib import Path
import re
from statistics import median
import subprocess
import sys

from audit_transfer_receipts import check_latency_evidence, check_measured_cpu
from protocol import (CASES_SHA, ENGINES, EXPECTED, FREEZE_SHA, IDENTITY_FILES, ORIGINAL_BINDING, ORIGINAL_IMAGE,
                      TIMES, clock_for, digest, panel, read, require, schedule)

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent / "gce-qualified-judge"
sys.path.insert(0, str(PILOT))


def checker(arm):
    # Isolate the profile parameter of the original pure checks; v12 and v13
    # both implement the same weighted-clock receipt contract.
    spec = importlib.util.spec_from_file_location("pilot_checks_" + arm, PILOT / "verify.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.PROFILE = ENGINES[arm][0]
    return module


V12 = checker("v12")
V13 = checker("v13")
PHASES = ("primary", "repeat")
READ = V12.evidence
FILES = ("platform.json", "trials.jsonl", "exec-trace.txt", "counter-probe.json", "latency.json")
RELEASE_GATES = frozenset(("original_contest_verdicts_and_live_stops",
    "all_judge_deadline_verdicts_match", "ample_held_out_answers", "terminal_outputs_and_rep_match",
    "terminal_instruction_spread_at_most_128", "freestanding_counters_exact"))
CANDIDATE_IMAGE = "sha256:5ebf65c242036f85ea8873926aa9e8721fa22cd8d0d3394dd32309e09354a3d2"


def check_topology(infrastructure, registration):
    require(set(infrastructure) == set(PHASES), "expected two fresh guest receipts")
    history = []
    for parent in (PILOT/"evidence", PILOT/"refit/evidence"):
        history.extend(read(parent/"infrastructure.json").values())
    for v in infrastructure.values():
        require(v["zone"] == "asia-northeast3-a" and v["machine_type"] == "n2d-standard-4" and
                v["cpu_platform"] == "AMD Milan" and v["threads_per_core"] == 1 and
                v["image"] == "debian-12-bookworm-v20261117" and v["external_ip"] is False and
                v["service_account"] is False and v["disk_auto_delete"] is True and
                v["cleanup_http"] == {"instance":404, "boot_disk":404} and
                v["archive_sha256"] == registration["archive_sha256"], "unqualified machine, payload or cleanup")
    for k in ("instance_id_sha256", "disk_id_sha256", "boot_sha256"):
        values = {v[k] for v in infrastructure.values()}
        require(len(values)==2 and all(re.fullmatch("[a-f0-9]{64}", s) for s in values) and
                values.isdisjoint(v[k] for v in history), "reused identity or invalid "+k)


def count_tuple(report):
    return report["instructions"], report["rep_breakdown"]["movs_bytes"], report["rep_breakdown"]["stos_bytes"]


def check_install(receipt, plan):
    inst = receipt["work_measurement"]["installation"]
    require(inst["source_commit"] == plan["upstream"] and
            inst["runtime_source_commit"] == "6847bb210bea0ca11d1bf47704383d6a075da490" and
            inst["runtime_patch_sha256"] == plan["patches"]["lifetime"] and
            inst["runtime_code_replay_patch_sha256"] == plan["patches"]["replay"], "different engine source/patches")


def classify(native, judged):
    if len(set(native)) != 1: return "mixed_native"
    if len(set(judged)) != 1: return "mixed_judge"
    a,b = native[0],judged[0]
    if a == b: return "agreement"
    if b == V12.TLE: return "false_timeout"
    if a == V12.TLE: return "false_pass"
    return "wrong_completed_answer"


def check_counts(series):
    ins, mov, stos = zip(*series, strict=True)
    return max(ins)-min(ins), len(set(mov)) == len(set(stos)) == 1


def summarize(cells, groups, probes):
    successes = {
        "original_contest_verdicts_and_live_stops":True,
        "all_judge_deadline_verdicts_match":True,
        "ample_held_out_answers":True,
        "terminal_outputs_and_rep_match":True,
        "terminal_instruction_spread_at_most_128":True,
        "freestanding_counters_exact":len(probes)==8 and len(set(probes))==1 and probes[0][1:]==(4096,4096),
    }
    counters, outputs = defaultdict(list), defaultdict(set)
    timing, spread, exact_pairs = [], [], []
    tight = {arm:Counter() for arm in ENGINES}
    for phase in PHASES:
        for key,c in cells.items():
            rows = groups[phase,key]
            nk, a, b = (rows[x] for x in ("native","v12","v13"))
            require([len(g) for g in (nk,a,b)] == [3,2,2], "missing repeat "+key)
            native, old, new = ([r["receipt"]["status"] for r in g] for g in (nk,a,b))
            successes["all_judge_deadline_verdicts_match"] &= len(set(old+new)) == 1
            expected = EXPECTED[c["role"]]
            if c["boundary"] == "stress":
                expected = V12.TLE
            if c["boundary"] in ("contest","stress"):
                successes["original_contest_verdicts_and_live_stops"] &= set(native+old+new) == {expected}
                if expected == V12.TLE:
                    successes["original_contest_verdicts_and_live_stops"] &= all(
                        r["receipt"]["work_measurement_execution"]["termination_source"] == "durante_work_clock" for r in a+b)
            if c["boundary"] in ("held-low","held-high","low","high"):
                category = "held_out" if c["boundary"].startswith("held-") else "original_reference"
                for arm,statuses in (("v12",old),("v13",new)):
                    tight[arm][category+"/"+classify(native,statuses)] += 1
            binary_input = (c["binary_sha256"], digest(c["stdin"].encode()))
            for row in nk+a+b:
                r=row["receipt"]
                if r["status"] in (V12.AC,V12.WA):
                    outputs[binary_input].add(row["stdout"])
                    if c["expected_solver_stdout_sha256"]:
                        require(digest(row["stdout"].encode()) == c["expected_solver_stdout_sha256"], "known solver output differs")
                m=r.get("work_measurement",{})
                if m.get("complete"):
                    counters[binary_input].append(count_tuple(m))
            for baseline,candidate in zip(a,b,strict=True):
                measurements=[x["receipt"]["work_measurement"] for x in (baseline,candidate)]
                if all(m["complete"] for m in measurements):
                    exact_pairs.append(count_tuple(measurements[0]) == count_tuple(measurements[1]))
            if c["boundary"]=="held-ample":
                successes["ample_held_out_answers"] &= set(native+old+new) == {expected}
                if set(native+old+new) != {expected}:
                    continue
                native_ns=median(r["receipt"]["cpu_time_ns"] for r in nk)
                stat={"phase":phase,"case":key,"native_cpu_ns":native_ns}
                for name,group in (("v12",a),("v13",b)):
                    stat[name+"_raw_cpu_ns"]=median(r["receipt"]["cpu_time_ns"] for r in group)
                    stat[name+"_estimated_ns"]=median(
                        r["receipt"]["work_measurement_execution"]["time_estimate"]["nanoseconds"] for r in group)
                timing.append(stat)
    for values in counters.values():
        delta, rep_equal=check_counts(values)
        spread.append(delta)
        successes["terminal_instruction_spread_at_most_128"] &= delta <= 128
        successes["terminal_outputs_and_rep_match"] &= rep_equal
    successes["terminal_outputs_and_rep_match"] &= all(len(v)==1 for v in outputs.values())
    outcomes={}
    for arm, tally in tight.items():
        outcomes[arm]={}
        for category in ("held_out","original_reference"):
            values={label:tally[category+"/"+label] for label in
                    ("agreement","false_timeout","false_pass","mixed_native","mixed_judge","wrong_completed_answer")}
            values["groups"]=sum(values.values())
            outcomes[arm][category]=values
    statistics={"exact_paired_completions":sum(exact_pairs),"paired_completions":len(exact_pairs),
                "max_instruction_spread":max(spread),"freestanding_counts":sorted(set(probes)),
                "boundary_outcomes":outcomes,
                "ample_timing_ns":timing}
    if timing:
        statistics["paired_v12_over_v13_raw_cpu_median_ratio"]=median(x["v12_raw_cpu_ns"]/x["v13_raw_cpu_ns"] for x in timing)
        statistics["worst_abs_relative_time_error"]={arm:max(abs(x[arm+"_estimated_ns"]/x["native_cpu_ns"]-1) for x in timing) for arm in ENGINES}
    return successes,statistics


def read_arm(directory, registration, phase, arm, infra, cells, clock):
    reg = registration["plan"]
    plan = reg["arms"][arm]
    data={name:READ(directory,name) for name in FILES}
    for name,content in data.items():
        require(digest(content) == infra["files"][arm+"/"+name], "altered evidence "+arm+"/"+name)
    host=json.loads(data["platform.json"])
    require((host["phase"],host["arm"],host["affinity"],host["cpuset"],host["siblings"])==(phase,arm,[1],"1","1") and
            host["boot_sha256"]==infra["boot_sha256"] and host["plan_sha256"]==registration["payload"]["plan.json"] and
            host["engine"]==plan["engine"] and host["image"]==plan["image"] and host["binding"]==plan["binding"] and
            host["steal_ticks_after"]>=host["steal_ticks_before"], "invalid worker placement/identity")
    check = V12 if arm=="v12" else V13
    rows = [json.loads(line) for line in data["trials.jsonl"].splitlines()]
    windows=check.read_windows(data["exec-trace.txt"])
    committed=list(schedule(arm,phase,cells))
    require(len(rows)==len(windows)==len(committed), "incomplete worker receipt set")
    for i,(row,window,(cell,kind,rep)) in enumerate(zip(rows,windows,committed,strict=True)):
        c=cells[cell]
        require((row["sequence"],row["cell"],row["kind"],row["rep"]) == (i,cell,kind,rep) and
                row["binary_sha256"]==c["binary_sha256"] and
                row["request_sha256"]==reg["request_hashes"][arm+"/"+kind+"/"+cell], "unexpected request or order")
        check.check_placement(row,window)
        row.update(limit_ms=c["time_ms"],stdout_sha256=digest(row["stdout"].encode()))
        oracle=digest(c["expected_stdout"].encode())
        if kind=="native":
            check.check_native(row,oracle)
        else:
            check_measured_cpu(row["receipt"])
            check_install(row["receipt"],plan)
            check.check_judge(row,clock,{"installed_files":plan["files"]},oracle)
    probe=json.loads(data["counter-probe.json"])
    traces=check.read_windows(probe["trace"].encode())
    require(len(probe["rows"])==len(traces)==2, "missing deterministic control")
    observations=[]
    for r,window in zip(probe["rows"],traces,strict=True):
        require(r["request_sha256"]==reg["request_hashes"][arm+"/probe"] and r["stdout"]=="ok\n" and
                r["receipt"]["status"]==check.AC, "different deterministic control")
        row={**r,"kind":"judge","limit_ms":30000,"stdout_sha256":digest(r["stdout"].encode())}
        check.check_placement(row,window)
        check_measured_cpu(row["receipt"])
        check_install(row["receipt"],plan)
        check.check_judge(row,clock,{"installed_files":plan["files"]},digest(b"ok\n"))
        observations.append(count_tuple(row["receipt"]["work_measurement"]))
    latency = json.loads(data["latency.json"])
    check_latency_evidence(latency,phase,arm,infra["boot_sha256"],plan,check)
    return rows,observations,latency


def verify(evidence, registration):
    reg=registration["plan"]
    require(digest((json.dumps(reg,indent=2,sort_keys=True,allow_nan=False)+"\n").encode()) == registration["payload"]["plan.json"],
            "registration plan digest differs")
    root_freeze=PILOT/"refit/freeze.json"
    root_cases=PILOT/"refit/case_manifest.json"
    require(digest(root_freeze.read_bytes())==reg["freeze_sha256"]==FREEZE_SHA and
            digest(root_cases.read_bytes())==reg["cases_sha256"]==CASES_SHA,"changed pre-transfer corpus/clock")
    for name in ("guest.sh","guest.py","protocol.py","latency_probe.py","counter_probe.S"):
        require(digest((HERE/name).read_bytes()) == registration["payload"][name], "registered guest source changed: "+name)
    builder=read(evidence/"builder.json")
    candidate=reg["arms"]["v13"]
    require(builder["aonohako_packaging_commit"]=="b7f7ab7e3dbbba6226735fc4179f2708be4088df" and
            builder["runtime_build"]["squashfs_sha256"]==CANDIDATE_IMAGE.removeprefix("sha256:") and
            all(builder["installed_sha256"][f]==candidate["files"][f] for f in IDENTITY_FILES) and
            builder["runtime_build"]["commit"]==ENGINES["v13"][1] and
            builder["cleanup_http"]=={"instance":404,"boot_disk":404} and builder["temporary_key_removed"] is True,
            "builder provenance differs from the preregistered installation")
    original=read(PILOT/"registration.json")
    for name,sha in original["payload"].items():
        require(registration["payload"]["pilot/"+name]==sha,"different historical pilot "+name)
    frozen=read(root_freeze)
    cells=panel(original["corpus"],read(root_cases),frozen)
    infra=read(evidence/"infrastructure.json")
    check_topology(infra,registration)
    clocks={}
    for arm,check in (("v12",V12),("v13",V13)):
        p=reg["arms"][arm]
        image=ORIGINAL_IMAGE if arm=="v12" else CANDIDATE_IMAGE
        require(p["image"]==image and p["engine"]==ENGINES[arm][0] and p["upstream"]==ENGINES[arm][1] and
                set(p["files"])==set(IDENTITY_FILES), "unrecognized engine/image")
        pins=read(HERE.parents[2]/"third_party"/("durante-"+arm)/"UPSTREAM.json")
        require(pins["commit"]==p["upstream"] and all(re.fullmatch("[a-f0-9]{64}",h) for h in p["files"].values()),
                "different vendor source or invalid file hash")
        p={**p,"patches":{"lifetime":pins["files"]["toolkit/patches/dynamorio-vfork-lifetime.patch"],
                         "replay":pins["files"]["toolkit/patches/dynamorio-code-replay.patch"]}}
        if arm=="v12":
            require(all(p["files"][k]==original["plan"]["installed_files"][k] for k in IDENTITY_FILES),"different baseline installation")
        # Derived data only: do not mutate the signed registration.
        registration={**registration,"plan":{**registration["plan"],"arms":{**registration["plan"]["arms"],arm:p}}}
        clocks[arm]=clock_for(arm,image,cells,frozen,check.model_binding)
        require(clocks[arm]["model_binding"]==p["binding"],"invalid arm binding")
    groups=defaultdict(lambda:defaultdict(list))
    probes=[]
    latency={}
    for phase in PHASES:
        for arm in ENGINES:
            rows,control,latency[phase,arm]=read_arm(evidence/phase/arm,registration,phase,arm,infra[phase],cells,clocks[arm])
            probes.extend(control)
            for row in rows:
                groups[phase,row["cell"]]["native" if row["kind"]=="native" else arm].append(row)
    gates,statistics=summarize(cells,groups,probes)
    from latency_probe import compare_latency_probes
    lat={phase:compare_latency_probes(latency[phase,"v12"],latency[phase,"v13"],original["corpus"]) for phase in PHASES}
    result={"schema":1,"guests":2,"total_http_requests":788,"panel_http_requests":756,
            "main_panel_arms":{"native":324,"v12":216,"v13":216},
            "main_cells_per_guest":{"original":18,"held_out_boundaries":24,"held_out_ample":12},
            "deterministic_control_requests":8,"latency_diagnostic_requests":24,
            "bindings":{a:c["model_binding"] for a,c in clocks.items()},"gates":gates,
            "eligible":all(gates.values()),"measurements":statistics,"latency_diagnostics":lat}
    return result,clocks["v13"]


def operator_environment(result, clock):
    gates=result["gates"]
    require(result["eligible"] is True and set(gates)==RELEASE_GATES and all(value is True for value in gates.values()),
            "v13 release criteria failed")
    require(result["bindings"]=={"v12":ORIGINAL_BINDING,"v13":clock["model_binding"]} and
            clock["model_binding"] != ORIGINAL_BINDING and
            clock["model_binding"]==V13.model_binding(clock["policy"],clock["runtime_options_id"]) and
            clock["policy"]["engine_profile"]==ENGINES["v13"][0] and clock["policy"]["runtime_id"]=="CPP20@"+CANDIDATE_IMAGE,
            "v13 release binding differs from measured candidate")
    original=json.loads(subprocess.check_output([sys.executable,"-B",str(PILOT/"operator_env.py")]))
    scope=json.loads(original["AONOHAKO_DURANTE_JUDGE_CASES"])
    require(len(scope["cases"])==3 and {r["problem_id"]:r["time_ms"] for r in scope["cases"]}==TIMES and
            all(r["model_binding"]==result["bindings"]["v12"] for r in scope["cases"]), "different historical release")
    for entry in scope["cases"]:
        entry["model_binding"]=clock["model_binding"]
        require(entry in clock["judge_cases"]["cases"],"unqualified release scope")
    original.update(AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT=CANDIDATE_IMAGE,AONOHAKO_DURANTE_RUNTIME_IMAGE=CANDIDATE_IMAGE,
                    AONOHAKO_DURANTE_TIME_MODELS=json.dumps(clock["time_models"]),AONOHAKO_DURANTE_JUDGE_CASES=json.dumps(scope))
    return original


if __name__ == "__main__":
    cli=argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--evidence",type=Path,default=HERE/"evidence")
    cli.add_argument("--registration",type=Path,default=HERE/"registration.json")
    cli.add_argument("--operator-env",action="store_true")
    args=cli.parse_args()
    result,clock=verify(args.evidence,read(args.registration))
    print(json.dumps(operator_environment(result,clock) if args.operator_env else result,indent=2,sort_keys=True,allow_nan=False))
