#!/usr/bin/env python3
"""Run the frozen v12 schedule against actual embedded /execute workers."""
import argparse
import base64
import contextlib
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time
import urllib.error
import urllib.request

CPU = 1
PORTS = {"native": 18401, "live": 18402, "counters": 18403}
HARD = {"cpu_time_ms": 120000, "wall_time_ms": 170000, "memory_mb": 1024}
NOMINAL = {"time_ms": 30000, "wall_time_ms": 60000, "memory_mb": 768, "output_bytes": 8 << 20}
BUDGET = (1 << 63) - 1
DRRUN = "/opt/durante/dynamorio/bin64/drrun"
PROFILE = "dynamorio-11.3-durante-v12-vm32m"


def require(ok, message):
    if not ok:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read_json(path):
    return json.loads(path.read_bytes())


def fetch(url, *, data=None, timeout=2):
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        body = response.read((16 << 20) + 1)
        require(len(body) <= 16 << 20, "oversize HTTP response")
        return response.headers.get_content_type(), body


@contextlib.contextmanager
def worker(kind, server_binary, work, log_dir):
    work = work / kind
    work.mkdir(mode=0o755)
    port = PORTS[kind]
    url = f"http://127.0.0.1:{port}"
    env = {"PATH": os.defpath, "PORT": str(port), "AONOHAKO_DEPLOYMENT_TARGET": "dev",
           "AONOHAKO_INBOUND_AUTH": "none", "AONOHAKO_EXECUTION_TRANSPORT": "embedded",
           "AONOHAKO_SANDBOX_BACKEND": "helper", "AONOHAKO_WORK_ROOT": str(work),
           "AONOHAKO_CPU_NORMALIZATION": "false", "AONOHAKO_CPU_SHORT_CASE_SAMPLING": "false",
           "AONOHAKO_MAX_ACTIVE_RUNS": "1", "AONOHAKO_DURANTE_OBSERVE": "off"}
    if kind != "native":
        env.update(AONOHAKO_DURANTE_FAMILIES="cpp", AONOHAKO_DURANTE_CPUID_PROFILE="x86-64-v2",
                   AONOHAKO_DURANTE_LATENCY_BOUND="v1" if kind == "counters" else "off",
                   AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES="v1" if kind == "counters" else "off",
                   AONOHAKO_DURANTE_RESOURCE_POLICY=json.dumps({"max_limits": HARD,
                       "memory_budget_mb": 2048, "reserved_memory_mb": 512}))
    with (log_dir / f"{kind}.log").open("xb") as log:
        proc = subprocess.Popen([str(server_binary)], env=env, stdout=log, stderr=log, umask=0o022)
        try:
            for _ in range(150):
                require(proc.poll() is None, f"{kind} worker exited before readiness; see {kind}.log")
                try:
                    if fetch(url + "/readyz")[1] == b"ok":
                        break
                except (urllib.error.URLError, TimeoutError):
                    time.sleep(0.1)
            else:
                raise RuntimeError(f"{kind} worker did not become ready")
            yield proc.pid
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()


def make_body(case, kind, root):
    contents = {}
    for key in ("stdin", "stdout", "binary"):
        data = (root / case[key + "_path"]).read_bytes()
        require(sha(data) == case[key + "_sha256"], f"{case['case_id']}: changed {key}")
        contents[key] = data
    request = {"lang": "CPP20", "binaries": [{"name": "Main", "mode": "exec",
                  "data_b64": base64.b64encode(contents["binary"]).decode("ascii")}],
               "stdin": contents["stdin"].decode(), "expected_stdout": contents["stdout"].decode(),
               "limits": NOMINAL, "emit_logs": True}
    if kind != "native":
        request.update(work_measurement=True, work_measurement_limits=HARD)
        if kind == "live":
            request["work_measurement_clock"] = {"version": 1, "budget": BUDGET}
    return json.dumps(request, separators=(",", ":")).encode()


def execute(body, kind):
    mediatype, data = fetch(f"http://127.0.0.1:{PORTS[kind]}/execute", data=body, timeout=185)
    require(mediatype == "text/event-stream", "unexpected execute response")
    events = []
    for frame in data.decode().split("\n\n"):
        lines = frame.splitlines()
        if len(lines) == 2 and lines[0].startswith("event: ") and lines[1].startswith("data: "):
            events.append((lines[0][7:], json.loads(lines[1][6:])))
    require(events and events[-1][0] == "result" and sum(n == "result" for n, _ in events) == 1,
            "missing/duplicate terminal SSE event")
    actual = "".join(v["chunk"] for n, v in events if n == "log" and v["stream"] == "stdout")
    return events[-1][1], sha(actual.encode())


def validate(receipt, kind):
    require(receipt.get("exit_code") == 0 and receipt["cpu_time_ns"] > 0, "unsuccessful execution")
    require(not receipt.get("cpu_time_normalization") and not receipt.get("cpu_time_sampling"),
            "native time normalization or short-case sampling active")
    if kind == "native":
        require(receipt["status"] == "Accepted" and receipt.get("score") in (None, 1) and
                receipt["verdict_source"] == "stdout" and "work_measurement" not in receipt and
                "work_measurement_observation" not in receipt and receipt.get("cpu_accounting"),
                "native control did not return the ordinary verdict and CPU accounting")
        return
    report = receipt["work_measurement"]
    env = receipt["work_measurement_execution"]
    require(receipt["status"] == "Container Initialization Failed" and receipt.get("score") is None and
            env["state"] == "complete" and env["output_matches"] is True and env["raw_limits"] == HARD and
            env["termination_source"] == "stdout", "incomplete instrumented run")
    require(report["engine_profile"] == PROFILE and report["family"] == "cpp" and
            report["cpuid_profile"] == "x86-64-v2" and report["authority"] == "diagnostic_only" and
            report["complete"] and report["signals_observed"] == 0 and report["quality"] == "uninterrupted" and
            report["unsupported_rep_entries"] == 0 and report["processes"] == report["images"] == 1 and
            report["installation"]["unchanged_before_after"], "wrong engine or ineligible counters")
    rep = report["rep_breakdown"]
    require(report["rep_bytes"] == rep["movs_bytes"] + rep["stos_bytes"], "REP byte mismatch")
    if kind == "live":
        clock = env["clock"]
        require(clock["state"] == "observed" and clock["budget"] == BUDGET and
                clock["metric"] == "instructions_plus_movs_stos_bytes_v1" and
                clock["progress"] == {"instructions": report["instructions"],
                                      "movs_bytes": rep["movs_bytes"], "stos_bytes": rep["stos_bytes"]} and
                "latency_bound" not in report and "memory_operand_bytes" not in report,
                "raw live clock differed from terminal counters")
    else:
        require("clock" not in env and report["latency_bound"]["version"] ==
                report["memory_operand_bytes"]["version"] == 1, "missing diagnostic counter profile")


EXEC = re.compile(r"\[(\d+)\].*?sched_process_exec: filename=(.*?) pid=(\d+) old_pid=")


def collect_execs(trace_dir, raw_file, sequence, kind):
    lines = []
    events = []
    for line in (trace_dir / "trace").read_text().splitlines():
        found = EXEC.search(line)
        if found:
            cpu, path, pid = found.groups()
            if Path(path).name in {"Main", "server", "drrun", "libdynamorio.so"}:
                events.append({"cpu": int(cpu), "path": path, "pid": int(pid)})
                lines.append(line)
    raw_file.write(f"# trial {sequence}\n" + "\n".join(lines) + "\n")
    targets = [e for e in events if (Path(e["path"]).name == "Main" if kind == "native" else e["path"] == DRRUN)]
    require(len(targets) == 1 and all(e["cpu"] == CPU for e in events),
            f"trial {sequence}: missing, duplicate or misplaced target: {events}")
    for stat in trace_dir.glob("per_cpu/cpu*/stats"):
        for line in stat.read_text().splitlines():
            key, _, val = line.partition(":")
            if key.strip() in {"overrun", "commit overrun", "dropped events"}:
                require(int(val.strip()) == 0, f"trial {sequence}: lost trace events")
    return events


def cpu_steal():
    return int(next(x.split()[8] for x in Path("/proc/stat").read_text().splitlines() if x.startswith("cpu1 ")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--work-root", type=Path, required=True)
    parser.add_argument("--trace-dir", type=Path)
    parser.add_argument("--preflight", metavar="CASE_ID", help="run one local trial per arm, without guest placement claims")
    args = parser.parse_args()
    require(bool(args.preflight) != bool(args.trace_dir), "use a guest trace or an explicit local preflight")
    root = Path.cwd()
    out = root / ("preflight-output" if args.preflight else "output")
    out.mkdir()
    cases = {c["case_id"]: c for c in read_json(root / "cases.json")}
    schedule = read_json(root / "plan.json")["schedule"]
    if args.preflight:
        schedule = [s for s in schedule if s["case_id"] == args.preflight and s["rep"] == 1]
        require({s["kind"] for s in schedule} == {"native", "live", "counters"}, "invalid preflight selection")
    affinity = sorted(os.sched_getaffinity(0))
    cg = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split(":")[-1].lstrip("/")
    if not args.preflight:
        require(affinity == [CPU] and (cg / "cpuset.cpus.effective").read_text().strip() == str(CPU),
                "wrong guest affinity or cgroup cpuset")
    with contextlib.ExitStack() as stack:
        workers = {kind: stack.enter_context(worker(kind, args.server.resolve(), args.work_root, out)) for kind in PORTS}
        worker_affinities = {k: sorted(os.sched_getaffinity(pid)) for k, pid in workers.items()}
        if not args.preflight:
            require(all(v == [CPU] for v in worker_affinities.values()), "worker affinity mismatch")
        platform_data = {"preflight": bool(args.preflight), "cpu": CPU, "affinity": affinity, "workers": workers,
                         "worker_affinities": worker_affinities, "cgroup": str(cg), "kernel": platform.release(),
                         "cpu_model": next(x.split(":", 1)[1].strip() for x in Path("/proc/cpuinfo").read_text().splitlines()
                                           if x.startswith("model name")), "steal_ticks_before": cpu_steal(),
                         "plan_sha256": sha((root / "plan.json").read_bytes()),
                         "cases_sha256": sha((root / "cases.json").read_bytes()),
                         "cpuset_cpus_effective": (cg / "cpuset.cpus.effective").read_text().strip()
                             if (cg / "cpuset.cpus.effective").is_file() else None,
                         "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
                         "cpu1_thread_siblings": Path("/sys/devices/system/cpu/cpu1/topology/thread_siblings_list").read_text().strip(),
                         "server_sha256": sha(args.server.read_bytes())}
        with (out / "trials.jsonl").open("x") as trials, (out / "exec-trace.txt").open("x") as raw_trace:
            for seq, item in enumerate(schedule):
                case = cases[item["case_id"]]
                kind = "native" if item["kind"] == "warmup" else item["kind"]
                body = make_body(case, kind, root)
                if args.trace_dir:
                    (args.trace_dir / "trace").write_text("\n")
                receipt = None
                try:
                    receipt, stdout = execute(body, kind)
                    require(stdout == case["stdout_sha256"], "stdout SHA-256 mismatch")
                    validate(receipt, kind)
                except Exception:
                    if receipt is not None:
                        (out / f"failed-{seq}.json").write_text(json.dumps(receipt))
                    raise
                execs = collect_execs(args.trace_dir, raw_trace, seq, kind) if args.trace_dir else []
                row = {**item, "sequence": seq, "program": case["program"], "binary_sha256": case["binary_sha256"],
                       "stdin_sha256": case["stdin_sha256"], "stdout_sha256": stdout, "request_sha256": sha(body),
                       "service_port": PORTS[kind], "receipt": receipt, "execs": execs}
                trials.write(json.dumps(row, separators=(",", ":")) + "\n")
                trials.flush()
        platform_data["steal_ticks_after"] = cpu_steal()
        (out / "platform.json").write_text(json.dumps(platform_data, indent=2) + "\n")


if __name__ == "__main__":
    main()
