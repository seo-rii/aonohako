"""Run the two deciding v13 clocks and ordinary native controls on guest CPU 1."""
import argparse
from contextlib import ExitStack
import hashlib
import importlib.util
import json
import os
from pathlib import Path
from statistics import median
import sys

ROOT = Path(__file__).resolve().parent
ARMS = ("shared", "candidate")
V13_ENGINE = "dynamorio-11.3-durante-v13-vm32m"


def read(path):
    return json.loads(path.read_bytes())


def write(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True, allow_nan=False) + "\n")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def rotate(items, place):
    index = place % len(items)
    return items[index:] + items[:index]


def prelude(plan, phase):
    keys = sorted(k for k, case in plan["cases"].items() if case["split"] == "new")
    if phase == "repeat":
        keys.reverse()
    for rep in range(1, 6):
        for case_id in rotate(keys, rep - 1):
            yield case_id, "ample", "native", rep, plan["cases"][case_id]["limits"]["ample"]


def cells(plan, thresholds):
    for case_id, case in sorted(plan["cases"].items()):
        limits = dict(case["limits"])
        if case["split"] == "new":
            for bound in ("low", "high"):
                limits[bound] = thresholds[case_id][bound + "_ms"]
        for bound, ms in sorted(limits.items()):
            yield case_id, bound, ms


def schedule(plan, phase, thresholds):
    yield from prelude(plan, phase)
    panel = list(cells(plan, thresholds))
    order = ["native", "shared", "candidate"]
    if phase == "repeat":
        panel.reverse()
        order.reverse()
    for rep in (1, 2, 3):
        for pos, (case_id, bound, ms) in enumerate(rotate(panel, rep - 1)):
            for arm in rotate(order, pos + rep):
                if arm != "native" and rep == 3:
                    continue
                if arm == "native" and bound == "ample" and plan["cases"][case_id]["split"] == "new":
                    continue
                yield case_id, bound, arm, rep, ms


def clock_policy(plan, baseline_policy, arm):
    policy = {**baseline_policy, "runtime_id": "CPP20@" + plan["image"], "engine_profile": V13_ENGINE}
    if arm == "candidate":
        coefficient = plan["calibration"]["candidate"]
        policy.update(model_id="pinned-inversions-reference-work", reference_id="inversions-reference-v13",
                      instruction_ns_numerator=coefficient["numerator"], rep_byte_ns_numerator=coefficient["numerator"],
                      ns_denominator=coefficient["denominator"])
    elif arm != "shared":
        raise ValueError("unknown clock")
    return policy


def load_pilot():
    directory = ROOT / "pilot"
    sys.path.insert(0, str(directory))
    spec = importlib.util.spec_from_file_location("qualified_pilot", directory / "guest.py")
    pilot = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(pilot)
    pilot.PORTS.update(shared=18441, candidate=18442)
    return pilot


def collect(phase, work, trace):
    from plan import bracket
    plan = read(ROOT / "plan.json")
    installation = read(ROOT / "installation.json")
    pilot = load_pilot()
    if plan["image"] != "sha256:" + digest((ROOT / "runtime.squashfs").read_bytes()):
        raise ValueError("mounted image digest differs from plan")
    files = installation["files"]
    for name, expected in files.items():
        if digest((Path("/opt/durante") / name).read_bytes()) != expected:
            raise ValueError("mounted installation differs at " + name)
    cgroup = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split("::")[-1].lstrip("/")
    host = dict(schema=1, phase=phase, boot_sha256=digest(Path("/proc/sys/kernel/random/boot_id").read_text().strip().encode()),
                affinity=sorted(os.sched_getaffinity(0)), cpuset=(cgroup / "cpuset.cpus.effective").read_text().strip(),
                siblings=Path("/sys/devices/system/cpu/cpu1/topology/thread_siblings_list").read_text().strip(),
                image=plan["image"], files=files, plan_sha256=digest((ROOT / "plan.json").read_bytes()),
                clocks={}, steal_ticks_before=pilot.source.cpu_steal())
    if host["affinity"] != [1] or host["cpuset"] != "1" or host["siblings"] != "1":
        raise RuntimeError("guest workers require CPU 1 with SMT disabled")
    out = ROOT / "output" / "clock"
    out.mkdir(parents=True)
    binaries = {}
    for case in plan["cases"].values():
        if case["binary"] not in binaries:
            binaries[case["binary"]] = (ROOT / case["binary"]).read_bytes()
        if digest(binaries[case["binary"]]) != case["binary_sha256"]:
            raise ValueError("different contestant binary")
    endpoints, observed_ns = {}, {}
    sequence = 0
    with ExitStack() as stack, (out / "trials.jsonl").open("x") as rows, (out / "exec-trace.txt").open("x") as traces:
        def start(arm, settings):
            url, pid = stack.enter_context(pilot.worker(arm, work, out, plan["image"], settings))
            if digest(Path(f"/proc/{pid}/exe").read_bytes()) != files["server"]:
                raise ValueError("running server differs from installed server")
            endpoints[arm] = url

        def observe(case_id, boundary, arm, rep, ms):
            nonlocal sequence
            case = plan["cases"][case_id]
            stdin = case["stdin"].encode()
            oracle = case["expected_stdout"].encode()
            kind = "native" if arm == "native" else "judge"
            clock = host["clocks"].get(arm, {})
            req = pilot.request_for(case["problem"], kind, ms, stdin, oracle, binaries[case["binary"]], clock.get("model_binding"))
            (trace / "trace").write_text("\n")
            receipt, stdout = pilot.post(endpoints[arm], "/execute", req, timeout=200)
            events = pilot.source.collect_execs(trace, traces, sequence, kind)
            row = dict(sequence=sequence, case_id=case_id, boundary=boundary, kind=arm, rep=rep, time_ms=ms,
                       binary_sha256=case["binary_sha256"], stdin_sha256=digest(stdin),
                       request_sha256=digest(json.dumps(req).encode()), stdout=stdout, execs=events, receipt=receipt)
            rows.write(pilot.packed(row).decode() + "\n")
            rows.flush()
            sequence += 1
            pilot.validate(receipt, kind, req, stdout.encode(), oracle, clock)
            if receipt["status"] in ("Accepted", "Wrong Answer") and stdout != case["solver_stdout"]:
                raise ValueError("solution output differs from the independent oracle for this role")
            return receipt

        start("native", {})
        for case_id, boundary, arm, rep, ms in prelude(plan, phase):
            receipt = observe(case_id, boundary, arm, rep, ms)
            if receipt["status"] not in ("Accepted", "Wrong Answer"):
                raise RuntimeError("could not complete a prospective ample native calibration")
            observed_ns.setdefault(case_id, []).append(receipt["cpu_time_ns"])
        if phase == "primary":
            thresholds = {case_id: dict(**bracket(values), native_median_ns=int(median(values)))
                          for case_id, values in observed_ns.items()}
        else:
            thresholds = read(ROOT / "primary-thresholds.json")
        if set(thresholds) != set(observed_ns):
            raise ValueError("prospective threshold cases differ")
        write(out / "thresholds.json", thresholds)
        host["thresholds_sha256"] = digest((out / "thresholds.json").read_bytes())
        if phase == "repeat" and host["thresholds_sha256"] != digest((ROOT / "primary-thresholds.json").read_bytes()):
            raise ValueError("repeat threshold document changed")
        scopes = sorted({(plan["cases"][k]["problem"], digest(plan["cases"][k]["stdin"].encode()), ms)
                         for k, _, ms in cells(plan, thresholds)})
        for arm in ARMS if phase == "primary" else reversed(ARMS):
            dest = out / arm
            dest.mkdir()
            policy = clock_policy(plan, installation["policy"], arm)
            env, options, registry = pilot.configure(policy, plan["image"], dest)
            binding = pilot.model_binding(policy, options)
            if (arm == "shared") != (binding == installation["model_binding"]):
                raise ValueError("model binding did not identify the registered clock")
            judge_cases = {"schema": 1, "cases": [
                dict(problem_id=problem, source_language="CPP20", stdin_sha256=sha, time_ms=ms, model_binding=binding)
                for problem, sha, ms in scopes]}
            host["clocks"][arm] = dict(policy=policy, runtime_options_id=options, model_binding=binding,
                                       time_models=registry, judge_cases=judge_cases)
            env.update(AONOHAKO_DURANTE_JUDGE_CASES=json.dumps(judge_cases), AONOHAKO_DURANTE_JUDGE_INTEGRITY="off")
            start(arm, env)
        write(out / "platform.json", host)
        for i, trial in enumerate(schedule(plan, phase, thresholds)):
            if i >= sum(len(values) for values in observed_ns.values()):
                observe(*trial)
    host.update(steal_ticks_after=pilot.source.cpu_steal(), trials=sequence)
    write(out / "platform.json", host)
    memory = ROOT / "memory_probe.py"
    if memory.is_file():
        spec = importlib.util.spec_from_file_location("memory_probe", memory)
        probe = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(probe)
        memory_out, memory_work = ROOT / "output" / "memory", work / "memory"
        memory_out.mkdir()
        memory_work.mkdir(mode=0o755)
        probe.collect(ROOT, memory_out, memory_work, trace, phase)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("primary", "repeat"))
    parser.add_argument("--work-root", type=Path, required=True)
    parser.add_argument("--trace-dir", type=Path, required=True)
    parser.add_argument("--frozen", type=Path, required=True)
    args = parser.parse_args()
    collect(args.phase, args.work_root, args.trace_dir)
