"""Run two frozen deciding clocks and ordinary native CPU limits on the same pinned guest."""
import argparse
from contextlib import ExitStack
import importlib.util
import json
import os
from pathlib import Path
import platform
import sys
import urllib.error

ROOT = Path(__file__).resolve().parent
PILOT = ROOT / "pilot"
sys.path.insert(0, str(PILOT))
spec = importlib.util.spec_from_file_location("original_pilot", PILOT / "guest.py")
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)
v12 = pilot.source
require = v12.require


def scheduled(phase, frozen, held):
    for problem, role, boundary, kind, rep, limit in pilot.scheduled(phase, frozen["candidate"]):
        yield "original", problem, problem, role, boundary, "candidate" if kind == "judge" else kind, rep, limit
    arms = ("native", "baseline", "candidate")
    for rep in range(1, 4):
        for b, boundary in enumerate(("low", "high")):
            for offset in range(len(held)):
                case = held[(offset + 3 * (rep - 1) + b) % len(held)]
                limit = frozen["deadlines"][case["case_id"]][boundary + "_ms"]
                rotation = (rep + offset + b) % 3
                for kind in arms[rotation:] + arms[:rotation]:
                    if kind == "native" or rep <= 2:
                        yield "held_out", case["case_id"], case["problem"], case["role"], boundary, kind, rep, limit


def collect(phase, work_root, trace_dir, frozen_path):
    out = ROOT / "output"
    out.mkdir()
    plan = v12.read_json(PILOT / "plan.json")
    corpus = v12.read_json(PILOT / "corpus/manifest.json")
    manifest_path = ROOT / "case_manifest.json"
    held = [case for case in v12.read_json(manifest_path)["cases"] if case["split"] == "held_out"]
    by_id = {case["case_id"]: case for case in held}
    frozen = v12.read_json(frozen_path)
    require(len(held) == len(by_id) == 12 and set(by_id) == set(frozen["deadlines"]), "held-out panel changed")
    for path, key in ((pilot.SERVER, "server_sha256"), (pilot.REFERENCE, "reference_sha256"),
                      (PILOT / "runtime.squashfs", "runtime_sha256"), (PILOT / "corpus/manifest.json", "manifest_sha256")):
        require(v12.sha(path.read_bytes()) == plan[key], f"different {key}")
    cgroup = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split("::")[-1].lstrip("/")
    host = dict(phase=phase, boot_sha256=v12.sha(Path("/proc/sys/kernel/random/boot_id").read_text().strip().encode()),
                kernel=platform.release(), cpu=1, affinity=sorted(os.sched_getaffinity(0)),
                cpuset_cpus_effective=(cgroup / "cpuset.cpus.effective").read_text().strip(),
                cpu1_thread_siblings=Path("/sys/devices/system/cpu/cpu1/topology/thread_siblings_list").read_text().strip(),
                steal_ticks_before=v12.cpu_steal(), case_manifest_sha256=v12.sha(manifest_path.read_bytes()),
                plan_sha256=v12.sha((PILOT / "plan.json").read_bytes()), runtime_sha256=plan["runtime_sha256"],
                server_sha256=plan["server_sha256"], freeze_sha256=v12.sha(frozen_path.read_bytes()))
    require(host["affinity"] == [1] and host["cpuset_cpus_effective"] == host["cpu1_thread_siblings"] == "1",
            "unqualified worker placement")
    require(plan["runtime_image"] == "sha256:" + host["runtime_sha256"], "wrong runtime binding")
    with ExitStack() as stack:
        services = {"native": stack.enter_context(pilot.worker("native", work_root, out, plan["runtime_image"], {}))}
        for arm, slot in (("baseline", "raw"), ("candidate", "judge")):
            config_dir = out / arm
            config_dir.mkdir()
            model = frozen[arm]
            env, options, registry = pilot.configure(model["policy"], plan["runtime_image"], config_dir)
            require(options == model["runtime_options_id"] and registry == model["time_models"]
                    and pilot.model_binding(model["policy"], options) == model["model_binding"], f"{arm} model mismatch")
            env["AONOHAKO_DURANTE_JUDGE_CASES"] = json.dumps(model["judge_cases"])
            services[arm] = stack.enter_context(pilot.worker(slot, work_root, out, plan["runtime_image"], env))
        host["workers"] = {arm: pid for arm, (_, pid) in services.items()}
        for pid in host["workers"].values():
            require(v12.sha(Path(f"/proc/{pid}/exe").read_bytes()) == host["server_sha256"], "different executing server")
        pilot.write_json(out / "platform.json", host)
        with (out / "trials.jsonl").open("xb") as journal, (out / "exec-trace.txt").open("x") as trace:
            for seq, trial in enumerate(scheduled(phase, frozen, held)):
                study, case_id, problem, role, boundary, arm, rep, limit = trial
                binary = (PILOT / "corpus" / f"{problem}-{role}").read_bytes()
                bh = v12.sha(binary)
                require(bh == corpus[problem]["elf_sha256"][role], "different executable")
                if study == "original":
                    stdin = (PILOT / "corpus" / f"{problem}.stdin").read_bytes()
                    expected = (PILOT / "corpus" / f"{problem}.stdout").read_bytes()
                    require(v12.sha(stdin) == corpus[problem]["stdin_sha256"] and
                            v12.sha(expected) == corpus[problem]["stdout_sha256"], "original input/oracle mismatch")
                else:
                    case = by_id[case_id]
                    stdin, expected = case["stdin"].encode(), case["expected_stdout"].encode()
                    require(bh == case["binary_sha256"] and v12.sha(stdin) == case["stdin_sha256"] and
                            v12.sha(expected) == case["stdout_sha256"], "held-out payload mismatch")
                kind = "native" if arm == "native" else "judge"
                model = frozen.get(arm)
                req = pilot.request_for(problem, kind, limit, stdin, expected, binary, model and model["model_binding"])
                (trace_dir / "trace").write_text("\n")
                try:
                    receipt, stdout = pilot.post(services[arm][0], "/execute", req, timeout=pilot.HARD["wall_time_ms"]/1000+30)
                except urllib.error.HTTPError as error:
                    pilot.write_json(out / "failed-http.json", {"sequence": seq, "status": error.code,
                                                               "reason": error.read(16 << 10).decode(errors="replace")})
                    raise
                row = dict(sequence=seq, study=study, case_id=case_id, problem=problem, role=role, boundary=boundary,
                           kind=arm, rep=rep, limit_ms=limit, binary_sha256=bh, stdin_sha256=v12.sha(stdin),
                           expected_stdout_sha256=v12.sha(expected), stdout=stdout, stdout_sha256=v12.sha(stdout.encode()),
                           request_sha256=v12.sha(json.dumps(req).encode()), receipt=receipt)
                try:
                    row["execs"] = v12.collect_execs(trace_dir, trace, seq, kind)
                except (ValueError, OSError):
                    pilot.write_json(out / "failed-trial.json", row)
                    raise
                journal.write(pilot.packed(row) + b"\n")
                journal.flush()
                pilot.validate(receipt, kind, req, stdout.encode(), expected, model)
                if study == "held_out" and receipt["status"] != "Time Limit Exceeded":
                    require(receipt["status"] == case["expected_status"] and row["stdout_sha256"] == case["solver_stdout_sha256"],
                            "completed held-out solver output differs from the registered independent oracle")
    host["steal_ticks_after"] = v12.cpu_steal()
    pilot.write_json(out / "platform.json", host)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("phase", choices=("primary", "repeat"))
    cli.add_argument("--frozen", type=Path, required=True)
    cli.add_argument("--work-root", type=Path, required=True)
    cli.add_argument("--trace-dir", type=Path, required=True)
    args = cli.parse_args()
    collect(args.phase, args.work_root, args.trace_dir, args.frozen)
