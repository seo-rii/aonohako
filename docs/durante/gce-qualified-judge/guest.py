"""Calibrate a shared work policy on reference solutions, then exercise two frozen HTTP acceptance boots."""
import argparse
import base64
from contextlib import ExitStack, contextmanager
from fractions import Fraction
import json
import os
from pathlib import Path
import platform
from statistics import median
import subprocess
import sys
import urllib.error
import urllib.request
from time import sleep

import v12_guest as source

sys.path.insert(0, str(Path(__file__).resolve().parent / "scripts"))
from verify_durante_native_boundaries import COMBINED, CPU_SOURCES, judge_config, model_binding
from verify_durante_observation import post, require

PROBLEMS = ("range_min", "shortest_paths", "inversions")
SERVER = Path("/opt/durante/server")
REFERENCE = Path("/opt/durante/reference")
HARD = source.HARD
NOMINAL = source.NOMINAL
PORTS = {"native": 18317, "raw": 18318, "judge": 18319}


def packed(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n")


def ceil_ms(ns, numerator, denominator=1):
    divisor = 1_000_000 * denominator
    return (ns * numerator + divisor - 1) // divisor


@contextmanager
def worker(kind, work_root, out, image, settings):
    directory = work_root / kind
    directory.mkdir(mode=0o755)
    env = {"PATH": os.defpath, "PORT": str(PORTS[kind]), "AONOHAKO_DEPLOYMENT_TARGET": "dev",
           "AONOHAKO_INBOUND_AUTH": "none", "AONOHAKO_EXECUTION_TRANSPORT": "embedded",
           "AONOHAKO_SANDBOX_BACKEND": "helper", "AONOHAKO_WORK_ROOT": str(directory),
           "AONOHAKO_WORK_ROOT_MAX_BYTES": str(256 << 20), "AONOHAKO_MAX_ACTIVE_RUNS": "1",
           "AONOHAKO_CPU_NORMALIZATION": "false", "AONOHAKO_CPU_SHORT_CASE_SAMPLING": "false",
           "AONOHAKO_DURANTE_OBSERVE": "off", "AONOHAKO_DURANTE_RUNTIME_IMAGE": image,
           "AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT": image, **settings}
    url = f"http://127.0.0.1:{PORTS[kind]}"
    with (out / f"{kind}.log").open("xb") as log:
        process = subprocess.Popen([SERVER], env=env, stdout=log, stderr=log, umask=0o022)
        try:
            for _ in range(150):
                require(process.poll() is None, f"{kind} worker failed readiness")
                try:
                    with urllib.request.urlopen(url + "/readyz", timeout=1) as response:
                        if response.read() == b"ok":
                            break
                except (urllib.error.URLError, TimeoutError):
                    sleep(0.1)
            else:
                raise RuntimeError(f"{kind} worker readiness timeout")
            require(os.sched_getaffinity(process.pid) == {1}, f"{kind} worker affinity")
            yield url, process.pid
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def measurements():
    return {"AONOHAKO_DURANTE_FAMILIES": "cpp", "AONOHAKO_DURANTE_CPUID_PROFILE": "x86-64-v2",
            "AONOHAKO_DURANTE_LATENCY_BOUND": "off", "AONOHAKO_DURANTE_RUNTIME_DETERMINISM": "off",
            "AONOHAKO_DURANTE_RESOURCE_POLICY": json.dumps({
                "max_limits": HARD, "memory_budget_mb": 2048, "reserved_memory_mb": 512})}


def scheduled(phase, frozen=None):
    if phase == "calibrate":
        for rep in range(1, 6):
            for index in range(3):
                problem = PROBLEMS[(index + rep - 1) % 3]
                yield problem, "reference", "calibration", "native", rep, NOMINAL["time_ms"]
                if rep <= 3:
                    yield problem, "reference", "calibration", "raw", rep, NOMINAL["time_ms"]
        return
    configs = [("contest", role) for role in ("fast", "slow", "wrong")]
    configs += [("stress", "fast"), ("low", "reference"), ("high", "reference")]
    for rep in range(1, 4):
        for offset in range(len(configs)):
            boundary, role = configs[(offset + rep - 1) % len(configs)]
            for index in range(3):
                problem = PROBLEMS[(index + rep + offset - 1) % 3]
                time_ms = frozen["cases"][problem][boundary + "_ms"]
                kinds = ("native", "judge") if (index + rep + offset) % 2 else ("judge", "native")
                for kind in kinds:
                    if kind == "native" or rep <= 2:
                        yield problem, role, boundary, kind, rep, time_ms


def request_for(problem, kind, limit, stdin, expected, binary, binding):
    req = {"problem_id": problem, "lang": "CPP20",
           "binaries": [{"name": "Main", "mode": "exec", "data_b64": base64.b64encode(binary).decode()}],
           "stdin": stdin.decode(), "expected_stdout": expected.decode(),
           "limits": {**NOMINAL, "time_ms": limit}, "emit_logs": True}
    if kind != "native":
        req.update(work_measurement=True, work_measurement_limits=HARD)
    if kind == "raw":
        req["work_measurement_clock"] = {"version": 1, "budget": source.BUDGET}
    if kind == "judge":
        req.update(work_measurement_time=True, work_measurement_judge=True, work_measurement_judge_case_required=True,
                   work_measurement_judge_clock_binding=binding)
    return req


def validate(receipt, kind, req, stdout, expected, frozen):
    status = receipt.get("status")
    require(not receipt.get("cpu_time_normalization") and not receipt.get("cpu_time_sampling")
            and receipt.get("cpu_time_ns", 0) > 0, "invalid timing")
    if kind == "raw":
        source.validate(receipt, "live")
        require(stdout == expected, "calibration raw output")
        return
    require(status in ("Accepted", "Wrong Answer", "Time Limit Exceeded"), f"unexpected status {status}")
    limit_ns = req["limits"]["time_ms"] * 1_000_000
    if kind == "native":
        require(not any(k.startswith("work_measurement") for k in receipt) and receipt.get("cpu_accounting"),
                "missing ordinary native accounting")
        if status == "Time Limit Exceeded":
            require(receipt["verdict_source"] in CPU_SOURCES and receipt["cpu_time_ns"] > limit_ns, "native TLE cause")
        else:
            require(receipt["verdict_source"] == "stdout" and receipt["exit_code"] == 0
                    and (status == "Accepted") == (stdout == expected), "native answer check")
        return
    judge = receipt["work_measurement_judgment"]
    execution, report = receipt["work_measurement_execution"], receipt["work_measurement"]
    clock = execution["clock"]
    require(receipt.get("verdict_source") == "durante_judge" and judge["verdict"] == status
            and judge["authority"] == report["authority"] == execution["authority"] == "diagnostic_only"
            and judge["integrity_mode"] == "off" and execution["raw_limits"] == HARD
            and execution["nominal_limits"] == req["limits"] and clock["model_binding"] == frozen["model_binding"]
            and clock["budget"] == limit_ns and clock["metric"] == "model_weighted_nanoseconds_v1"
            and clock["state"] == "observed", "unexpected judging policy")
    progress = clock["progress"]
    require(not progress.get("overflow_mask") and all(type(progress[k]) is int and progress[k] >= 0
            for k in ("instructions","movs_bytes","stos_bytes")), "invalid progress")
    work = progress["instructions"] + progress["movs_bytes"] + progress["stos_bytes"]
    policy = frozen["policy"]
    above = work * policy["instruction_ns_numerator"] > limit_ns * policy["ns_denominator"]
    cause = execution["termination_source"]
    require(judge["termination_source"] == cause, "judge and clock disagree")
    if status == "Time Limit Exceeded":
        require(above and cause in ("durante_work_clock", "durante_work_clock_final")
                and execution["state"] == "budget_exceeded" and receipt.get("score") is None, "unverified work timeout")
    else:
        require(not above and cause == "stdout" and execution["state"] == "complete" and receipt["exit_code"] == 0
                and execution["output_matches"] == (status == "Accepted") == (stdout == expected)
                and receipt.get("score") == (1 if status == "Accepted" else None), "judge answer check")
    if cause == "durante_work_clock":
        require(not report["complete"] and receipt.get("exit_code") is None and execution.get("time_estimate") is None,
                "invalid live stop")
    else:
        breakdown = report["rep_breakdown"]
        require(report["complete"] and report["instructions"] == progress["instructions"]
                and breakdown["movs_bytes"] == progress["movs_bytes"] and breakdown["stos_bytes"] == progress["stos_bytes"]
                and execution["time_estimate"]["runtime_options_id"] == frozen["runtime_options_id"]
                and execution["time_estimate"]["nanoseconds"] ==
                (work * policy["instruction_ns_numerator"] + policy["ns_denominator"] - 1) // policy["ns_denominator"],
                "terminal counters differ from live clock")


def configure(policy, image, out):
    dataset = {"metered_limits": HARD, "native_limits": NOMINAL, "dispatch_profile": policy["dispatch_profile"],
               "cpuid_profile": "x86-64-v2"}
    args = argparse.Namespace(runtime_image=image, reference=str(REFERENCE), output=out)
    _, env, options = judge_config(args, dataset, policy)
    return env, options, source.read_json(out / "registry.json")


def freeze(rows, plan, platform_data, manifest, out):
    ratios, cases = [], {}
    profiles = {(r["receipt"]["work_measurement"]["engine_profile"], r["receipt"]["work_measurement"]["dispatch_profile"])
                for r in rows if r["kind"] == "raw"}
    require(len(profiles) == 1, "reference profile changed")
    engine, dispatch = profiles.pop()
    for problem in PROBLEMS:
        native = [r["receipt"]["cpu_time_ns"] for r in rows if r["problem"] == problem and r["kind"] == "native"]
        raw = [r["receipt"]["work_measurement"] for r in rows if r["problem"] == problem and r["kind"] == "raw"]
        require(len(native) == 5 and len(raw) == 3, "incomplete reference")
        ns, work = median(native), median([r["instructions"] + r["rep_bytes"] for r in raw])
        ratios.append(Fraction(ns, work))
        cases[problem] = dict(native_median_ns=ns, work_median=work, contest_ms=max(10, ceil_ms(ns, 4)),
                              low_ms=max(1, 4*ns//5_000_000), high_ms=ceil_ms(ns, 6, 5), stress_ms=1)
    coefficient = median(ratios)
    policy = dict(method=COMBINED, model_id="pinned-judge-pilot", model_version=1,
                  reference_id="three-reference-algorithms", runtime_id="CPP20@" + plan["runtime_image"], family="cpp",
                  engine_profile=engine, dispatch_profile=dispatch, cpuid_profile="x86-64-v2",
                  instruction_ns_numerator=coefficient.numerator, rep_byte_ns_numerator=coefficient.numerator,
                  ns_denominator=coefficient.denominator)
    _, options, registry = configure(policy, plan["runtime_image"], out)
    binding = model_binding(policy, options)
    allowed = [{"problem_id": p, "source_language": "CPP20", "stdin_sha256": manifest[p]["stdin_sha256"],
                "time_ms": limit, "model_binding": binding} for p in PROBLEMS
               for limit in sorted({cases[p][label + "_ms"] for label in ("contest", "stress", "low", "high")})]
    return dict(schema=1, plan_sha256=platform_data["plan_sha256"], manifest_sha256=plan["manifest_sha256"],
                runtime_sha256=plan["runtime_sha256"], calibration_boot_sha256=platform_data["boot_sha256"],
                calibration_trials_sha256=source.sha((out/"trials.jsonl").read_bytes()), policy=policy,
                runtime_options_id=options, model_binding=binding, time_models=registry,
                judge_cases={"schema": 1, "cases": allowed}, cases=cases)


def collect(args):
    root = Path.cwd()
    out = root / "output"
    out.mkdir()
    manifest_path = root / "corpus/manifest.json"
    plan_path = root / "plan.json"
    plan = source.read_json(plan_path)
    manifest = source.read_json(manifest_path)
    for file, key in ((SERVER,"server_sha256"),(REFERENCE,"reference_sha256"),(root/"runtime.squashfs","runtime_sha256"),
                      (manifest_path,"manifest_sha256")):
        require(source.sha(file.read_bytes()) == plan[key], f"different {key}")
    require(plan["runtime_image"] == "sha256:" + plan["runtime_sha256"], "wrong runtime image binding")
    cg = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split("::")[-1].lstrip("/")
    boot_id = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    host = dict(boot_sha256=source.sha(boot_id.encode()), phase=args.phase, kernel=platform.release(), cpu=1,
                affinity=sorted(os.sched_getaffinity(0)), cpuset_cpus_effective=(cg/"cpuset.cpus.effective").read_text().strip(),
                cpu1_thread_siblings=Path("/sys/devices/system/cpu/cpu1/topology/thread_siblings_list").read_text().strip(),
                steal_ticks_before=source.cpu_steal(), plan_sha256=source.sha(plan_path.read_bytes()), workers={})
    require(host["affinity"] == [1] and host["cpuset_cpus_effective"] == host["cpu1_thread_siblings"] == "1",
            "unqualified guest placement")
    settings = {"native": {}}
    frozen = None
    if args.phase == "calibrate":
        settings["raw"] = measurements()
    else:
        frozen_raw = args.frozen.read_bytes()
        host["frozen_sha256"] = source.sha(frozen_raw)
        frozen = json.loads(frozen_raw)
        require(frozen["plan_sha256"] == host["plan_sha256"] and frozen["manifest_sha256"] == plan["manifest_sha256"]
                and frozen["calibration_boot_sha256"] != host["boot_sha256"], "bad calibration identity")
        (out/"frozen.json").write_bytes(frozen_raw)
        judge_env, options, registry = configure(frozen["policy"], plan["runtime_image"], out)
        require(options == frozen["runtime_options_id"] and registry == frozen["time_models"]
                and model_binding(frozen["policy"], options) == frozen["model_binding"], "frozen registry mismatch")
        settings["judge"] = {**judge_env, "AONOHAKO_DURANTE_JUDGE_CASES": json.dumps(frozen["judge_cases"])}
    rows = []
    with ExitStack() as stack:
        services = {kind: stack.enter_context(worker(kind, args.work_root, out, plan["runtime_image"], env))
                    for kind, env in settings.items()}
        host["workers"] = {kind: pid for kind, (_,pid) in services.items()}
        write_json(out / "platform.json", host)
        with (out/"trials.jsonl").open("xb") as journal, (out/"exec-trace.txt").open("x") as trace:
            for seq, (p, role, boundary, kind, rep, limit) in enumerate(scheduled(args.phase, frozen)):
                binary = (root/"corpus"/f"{p}-{role}").read_bytes()
                stdin = (root/"corpus"/f"{p}.stdin").read_bytes()
                expected = (root/"corpus"/f"{p}.stdout").read_bytes()
                bh, ih = source.sha(binary), source.sha(stdin)
                require(bh == manifest[p]["elf_sha256"][role] and ih == manifest[p]["stdin_sha256"]
                        and source.sha(expected) == manifest[p]["stdout_sha256"], "trial manifest mismatch")
                req = request_for(p, kind, limit, stdin, expected, binary, frozen and frozen["model_binding"])
                (args.trace_dir / "trace").write_text("\n")
                try:
                    receipt, text = post(services[kind][0], "/execute", req, timeout=HARD["wall_time_ms"]/1000 + 30)
                except urllib.error.HTTPError as exc:
                    write_json(out / "failed-http.json", {
                        "sequence": seq, "problem": p, "kind": kind, "status": exc.code,
                        "reason": exc.read(16 << 10).decode(errors="replace")})
                    raise
                stdout = text.encode()
                row = dict(sequence=seq, problem=p, role=role, boundary=boundary, kind=kind, rep=rep, limit_ms=limit,
                           binary_sha256=bh, stdin_sha256=ih, stdout=text, stdout_sha256=source.sha(stdout),
                           request_sha256=source.sha(json.dumps(req).encode()), receipt=receipt)
                try:
                    row["execs"] = source.collect_execs(args.trace_dir, trace, seq, kind)
                except (OSError, ValueError):
                    write_json(out / "failed-trial.json", row)
                    raise
                journal.write(packed(row) + b"\n")
                journal.flush()
                validate(receipt, kind, req, stdout, expected, frozen)
                if args.phase == "calibrate":
                    require(kind != "native" or receipt["status"] == "Accepted", "incomplete native calibration")
                    rows.append(row)
    host["steal_ticks_after"] = source.cpu_steal()
    write_json(out/"platform.json", host)
    if args.phase == "calibrate":
        write_json(out/"frozen.json", freeze(rows, plan, host, manifest, out))


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("phase", choices=("calibrate","primary","repeat"))
    cli.add_argument("--frozen", type=Path)
    cli.add_argument("--work-root", required=True, type=Path)
    cli.add_argument("--trace-dir", required=True, type=Path)
    args = cli.parse_args()
    require((args.phase == "calibrate") == (args.frozen is None), "invalid phase/frozen file combination")
    collect(args)
