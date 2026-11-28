"""Collect a single mounted arm using the pinned worker's normal HTTP endpoint."""
import argparse
from contextlib import ExitStack
import importlib.util
import io
import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
from protocol import CASES_SHA, FREEZE_SHA, clock_for, digest, panel, read, require, schedule, write
PILOT = ROOT / "pilot"
sys.path.insert(0, str(PILOT))
spec = importlib.util.spec_from_file_location("pilot_guest", PILOT / "guest.py")
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)


def collect(phase, arm, work, trace):
    plan = read(ROOT/"plan.json")["arms"][arm]
    freeze, held = ROOT/"freeze.json", ROOT/"case_manifest.json"
    require(digest(freeze.read_bytes()) == FREEZE_SHA and digest(held.read_bytes()) == CASES_SHA, "different frozen experiment")
    for name, sha in plan["files"].items():
        require(digest((Path("/opt/durante")/name).read_bytes()) == sha, "different mounted file "+name)
    require(digest((Path.cwd()/"runtime.squashfs").read_bytes()) == plan["image"].removeprefix("sha256:"), "different mounted image")
    cells = panel(read(PILOT/"corpus/manifest.json"),read(held),read(freeze))
    clock = clock_for(arm,plan["image"],cells,read(freeze),pilot.model_binding)
    require(clock["model_binding"] == plan["binding"], "different clock binding")
    out = ROOT/"output"/arm
    out.mkdir()
    cgroup = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split("::")[-1].lstrip("/")
    host = dict(phase=phase,arm=arm,boot_sha256=digest(Path("/proc/sys/kernel/random/boot_id").read_text().strip().encode()),
                affinity=sorted(os.sched_getaffinity(0)),cpuset=(cgroup/"cpuset.cpus.effective").read_text().strip(),
                siblings=Path("/sys/devices/system/cpu/cpu1/topology/thread_siblings_list").read_text().strip(),
                plan_sha256=digest((ROOT/"plan.json").read_bytes()),image=plan["image"],engine=plan["engine"],
                binding=plan["binding"],steal_ticks_before=pilot.source.cpu_steal())
    require(host["affinity"] == [1] and host["cpuset"] == host["siblings"] == "1", "wrong placement")
    env, opts, models = pilot.configure(clock["policy"],plan["image"],out)
    require(opts == clock["runtime_options_id"] and models == clock["time_models"], "different effective runtime model")
    env.update(AONOHAKO_DURANTE_JUDGE_CASES=json.dumps(clock["judge_cases"]), AONOHAKO_DURANTE_JUDGE_INTEGRITY="off")
    write(out/"platform.json",host)
    with ExitStack() as stack:
        urls = {}
        for kind in (("native","judge") if arm == "v12" else ("judge",)):
            url, pid = stack.enter_context(pilot.worker(kind,work,out,plan["image"],env if kind=="judge" else {}))
            require(digest(Path(f"/proc/{pid}/exe").read_bytes()) == plan["files"]["server"], "different live worker")
            urls[kind] = url
        with (out/"trials.jsonl").open("xb") as rows, (out/"exec-trace.txt").open("x") as traces:
            for seq,(cell_id, kind, rep) in enumerate(schedule(arm,phase,cells)):
                c=cells[cell_id]
                binary=(PILOT/"corpus"/(c["problem"]+"-"+c["role"])).read_bytes()
                require(digest(binary)==c["binary_sha256"],"different frozen ELF")
                expected=c["expected_stdout"].encode()
                req=pilot.request_for(c["problem"],kind,c["time_ms"],c["stdin"].encode(),expected,binary,plan["binding"])
                (trace/"trace").write_text("\n")
                receipt,stdout=pilot.post(urls[kind],"/execute",req,timeout=200)
                events=pilot.source.collect_execs(trace,traces,seq,kind)
                row=dict(sequence=seq,cell=cell_id,kind=kind,rep=rep,binary_sha256=c["binary_sha256"],
                         request_sha256=digest(json.dumps(req).encode()),stdout=stdout,execs=events,receipt=receipt)
                rows.write(pilot.packed(row)+b"\n")
                rows.flush()
                pilot.validate(receipt,kind,req,stdout.encode(),expected,clock)
        probe_req = pilot.request_for("transfer-counter-probe","judge",30000,b"",b"ok\n",(ROOT/"counter-probe").read_bytes(),plan["binding"])
        probe_rows, raw_trace = [], io.StringIO()
        for seq in (0,1):
            (trace/"trace").write_text("\n")
            receipt,stdout=pilot.post(urls["judge"],"/execute",probe_req,timeout=200)
            events=pilot.source.collect_execs(trace,raw_trace,seq,"judge")
            pilot.validate(receipt,"judge",probe_req,stdout.encode(),b"ok\n",clock)
            probe_rows.append(dict(receipt=receipt,stdout=stdout,execs=events,request_sha256=digest(json.dumps(probe_req).encode())))
        write(out/"counter-probe.json",dict(rows=probe_rows,trace=raw_trace.getvalue()))
    from latency_probe import collect_latency_probe
    collect_latency_probe(ROOT,arm,work,out,pilot,trace)
    host["steal_ticks_after"]=pilot.source.cpu_steal()
    write(out/"platform.json",host)


if __name__ == "__main__":
    cli=argparse.ArgumentParser(description=__doc__)
    cli.add_argument("phase",choices=("primary","repeat"))
    cli.add_argument("--trace-dir",type=Path,required=True)
    cli.add_argument("--work-root",type=Path,required=True)
    cli.add_argument("--frozen",type=Path,required=True)
    args=cli.parse_args()
    require(args.frozen.resolve() == ROOT/"plan.json", "unrecognized plan")
    arm=Path("arm").read_text().strip()
    require(arm in ("v12","v13"),"unrecognized worker arm")
    collect(args.phase,arm,args.work_root,args.trace_dir)
