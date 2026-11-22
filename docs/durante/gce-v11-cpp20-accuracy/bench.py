"""Collect the frozen C++20 workload schedule on a single Linux CPU."""
import hashlib
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import time

ROOT = Path(__file__).resolve().parent
OUT = ROOT / "output"
CPU = 1


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def check(condition, reason):
    if not condition:
        raise ValueError(reason)


def execution(args, stdin_path, directory, timeout):
    with stdin_path.open("rb") as stdin, (directory / "stdout").open("wb") as stdout, (directory / "stderr").open("wb") as stderr:
        start = time.monotonic_ns()
        child = subprocess.Popen(args, stdin=stdin, stdout=stdout, stderr=stderr, start_new_session=True)
        signal.alarm(timeout)
        try:
            _, status, usage = os.wait4(child.pid, 0)
            child.returncode = os.waitstatus_to_exitcode(status)
        except TimeoutError:
            os.killpg(child.pid, signal.SIGKILL)
            child.wait()
            raise
        finally:
            signal.alarm(0)
        wall = time.monotonic_ns() - start
    check(child.returncode == 0, f"{directory.name}: child exit {child.returncode}")
    check((directory / "stderr").stat().st_size == 0, f"{directory.name}: nonempty stderr")
    user_us = round(usage.ru_utime * 1_000_000)
    sys_us = round(usage.ru_stime * 1_000_000)
    return dict(user_us=user_us, sys_us=sys_us, cpu_us=user_us + sys_us,
                wall_us=round(wall / 1000), launcher_pid=child.pid,
                stdout_sha256=sha256((directory / "stdout").read_bytes()), exit_code=child.returncode)


def one(case, item, sequence):
    case_id = case["case_id"]
    program = ROOT / "corpus" / "bin" / case["program"]
    tag = f"{sequence:03d}-{item['kind']}-{item['rep']}"
    directory = OUT / "runs" / tag
    directory.mkdir()
    report = directory / "report.json"
    args = [str(program)]
    metered = item["kind"] == "metered"
    if metered:
        args = [str(ROOT / "durante"), "run", "--drrun", str(ROOT / "dynamorio/bin64/drrun"),
                "--client", str(ROOT / "v11.so"), "--report", str(report), "--timeout-ms", "120000",
                "--cpuid-profile", "x86-64-v2", "--latency-bound", "--memory-operand-bytes", "--", *args]
    row = execution(args, OUT / "inputs" / case["stdin_sha256"], directory, 135 if metered else 30)
    check(row["stdout_sha256"] == case["stdout_sha256"], f"{case_id} {tag}: wrong output")
    row.update(sequence=sequence, rep=item["rep"])
    if metered:
        data = json.loads(report.read_bytes())
        m, e = data["measurement"], data["execution"]
        check(e["state"] == "complete" and e["cleanup_complete"] and e["records_cleanup_complete"]
              and e["signal"] is None and e["exit_code"] == 0, f"{case_id}: incomplete execution")
        check(data["authority"] == m["authority"] == "diagnostic_only" and data["integrity_verified"] is False
              and m["integrity_verified"] is False and not m["measurement_contract"]["trusted_verdict_eligible"],
              f"{case_id}: bad authority")
        check(m["valid"] and m["complete"] and m["quality"] == "uninterrupted" and m["signals_observed"] == 0
              and m["image_count"] == m["process_count"] == 1 and m["cpuid_profile"] == "x86-64-v2",
              f"{case_id}: bad coverage")
        rep = m["rep_breakdown"]
        check(m["rep_bytes_enabled"] and m["rep_telemetry_status"] == "movs_stos_only"
              and m["unsupported_rep_entries"] == 0 and rep["version"] == 1
              and rep["movs_bytes"] + rep["stos_bytes"] == m["rep_bytes"],
              f"{case_id}: unsupported REP")
        check(m["latency_bound"]["version"] == m["memory_operand_bytes"]["version"] == 1
              and m["latency_bound"]["cycles_x16"] >= 4 * m["instructions"] > 0
              and m["memory_operand_bytes"]["bytes"] >= 0, f"{case_id}: invalid version-1 counters")
        row.update(root_pid=e["root_pid"], instructions=m["instructions"], rep_bytes=m["rep_bytes"],
                   movs_bytes=rep["movs_bytes"], stos_bytes=rep["stos_bytes"],
                   cycles_x16=m["latency_bound"]["cycles_x16"], memory_operand_bytes=m["memory_operand_bytes"]["bytes"],
                   report_sha256=sha256(report.read_bytes()))
    return row


def cpu_steal():
    row = next(line for line in Path("/proc/stat").read_text().splitlines() if line.startswith("cpu1 "))
    return int(row.split()[8])


def main():
    check(os.sched_getaffinity(0) == {CPU}, "runner affinity")
    slice_path = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split(":")[-1].lstrip("/")
    allowed = (slice_path / "cpuset.cpus.effective").read_text().strip()
    check(allowed == str(CPU), "unexpected cpuset")
    started = cpu_steal()
    OUT.mkdir()
    (OUT / "runs").mkdir()
    (OUT / "inputs").mkdir()
    protocol = json.loads((ROOT / "protocol.json").read_bytes())
    index = json.loads((ROOT / "corpus" / "cases.json").read_bytes())
    if isinstance(index, dict):
        index = index["cases"]
    cases = {str(c["case_id"]): c for c in index}
    collected = {}
    for c in protocol["cases"]:
        case_id = c["case_id"]
        fixture = cases[case_id]
        stdin = fixture["stdin"].encode()
        check(sha256(stdin) == c["stdin_sha256"] and sha256(fixture["stdout"].encode()) == c["stdout_sha256"],
              f"{case_id}: frozen input/output digest")
        check(sha256((ROOT / "corpus" / "bin" / c["program"]).read_bytes()) == c["binary_sha256"],
              f"{case_id}: frozen ELF digest")
        (OUT / "inputs" / c["stdin_sha256"]).write_bytes(stdin)
        collected[case_id] = dict(c, native=[], metered=[], warmup=None)
    with (OUT / "progress.jsonl").open("x") as progress:
        for sequence, item in enumerate(protocol["schedule"]):
            case = collected[item["case_id"]]
            row = one(case, item, sequence)
            kind = item["kind"]
            if kind == "warmup":
                case["warmup"] = row
            else:
                case[kind].append(row)
            progress.write(json.dumps(dict(item, **{k:v for k,v in row.items() if k not in item})) + "\n")
            progress.flush()
    config = dict(cpu=CPU, affinity=sorted(os.sched_getaffinity(0)), cgroup_allowed_cpus=allowed,
                  kernel=platform.release(), cpu_model=next(line.split(":", 1)[1].strip() for line in
                  Path("/proc/cpuinfo").read_text().splitlines() if line.startswith("model name")),
                  clock_tick_hz=os.sysconf("SC_CLK_TCK"), cpu_steal_ticks=cpu_steal()-started)
    write(OUT / "results.json", dict(protocol_sha256=sha256((ROOT / "protocol.json").read_bytes()),
          platform=config, cases=list(collected.values())))


def timeout(signum, frame):
    raise TimeoutError("per-trial deadline reached")


if __name__ == "__main__":
    signal.signal(signal.SIGALRM, timeout)
    main()
