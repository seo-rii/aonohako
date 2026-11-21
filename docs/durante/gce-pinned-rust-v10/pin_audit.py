#!/usr/bin/env python3
"""Record exec/fork placement for one Docker cgroup in an isolated tracefs instance."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.request

TRACE = Path("/sys/kernel/tracing")
CG = Path("/sys/fs/cgroup")
EVENTS = ("sched/sched_process_exec", "sched/sched_process_fork", "sched/sched_process_exit",
          "cgroup/cgroup_attach_task")


def read(p):
    return p.read_text().strip()


def save(p, value):
    p.write_text(json.dumps(value, indent=2) + "\n")


def load(p):
    return json.loads(p.read_bytes())


def write(p, value):
    p.write_text(str(value) + "\n")


def need(condition, message):
    if not condition:
        raise RuntimeError(message)


def cgroup(pid):
    prefix = "0::"
    return next(line[len(prefix):] for line in read(Path(f"/proc/{pid}/cgroup")).splitlines() if line.startswith(prefix))


def stat_cpu(pid):
    text = read(Path(f"/proc/{pid}/stat"))
    fields = text[text.rindex(")") + 2:].split()
    return int(fields[36])


def snap(a):
    rel = cgroup(a.init_pid)
    group = CG / rel.lstrip("/")
    root = Path(f"/proc/{a.init_pid}")
    parent_name = read(group / "cpuset.cpus")
    actual = read(group / "cpuset.cpus.effective")
    mask = sorted(os.sched_getaffinity(a.init_pid))
    need(actual == str(a.cpu) and parent_name == str(a.cpu) and mask == [a.cpu], "container does not have requested hard cpuset")
    need(a.cpu not in os.sched_getaffinity(0), "audit observer must be pinned to a different guest CPU")
    req = urllib.request.Request("http://metadata.google.internal/computeMetadata/v1/instance/id",
                                 headers={"Metadata-Flavor": "Google"})
    with urllib.request.urlopen(req, timeout=5) as response:
        guest_id = response.read(128).strip()
        need(response.headers.get("Metadata-Flavor") == "Google" and guest_id.isdigit(), "unexpected guest identity")
    return dict(path=rel, guest_id_sha256=hashlib.sha256(guest_id).hexdigest(), init_pid=a.init_pid, init_start_ticks=read(root / "stat").rsplit(")", 1)[1].split()[19],
                cpuset_requested=parent_name, cpuset_effective=actual, allowed_cpus=mask, start_or_end_cpu=stat_cpu(a.init_pid),
                observer_allowed_cpus=sorted(os.sched_getaffinity(0)), boot_id=read(Path("/proc/sys/kernel/random/boot_id")),
                proc_stat=read(Path("/proc/stat")).splitlines()[:1+len(list(Path("/sys/devices/system/cpu").glob("cpu[0-9]*")))])


def run_sampler(a):
    # Runs on a separate guest core. Inspect only tasks owned by this cgroup.
    # This binds the collector PID in the kernel trace to the observed hard cpuset.
    root = CG / cgroup(a.init_pid).lstrip("/")
    evidence = {}
    samples = 0
    while not (a.out / "stop-sampling").exists():
        for thread_file in root.rglob("cgroup.threads"):
            try:
                tids = read(thread_file).split()
            except FileNotFoundError:
                continue
            for tid in tids:
                try:
                    pid = int(tid)
                    fields = read(Path(f"/proc/{tid}/status")).splitlines()
                    tgid = int(next(s.split()[1] for s in fields if s.startswith("Tgid:")))
                    mask = sorted(os.sched_getaffinity(pid))
                    cpu = stat_cpu(tid)
                    rel = cgroup(pid)
                    name = os.path.basename(os.readlink(f"/proc/{tid}/exe"))
                except (FileNotFoundError, ProcessLookupError, StopIteration):
                    continue
                key = (pid, tgid, name, rel)
                record = evidence.setdefault(key, {"seen": 0, "cpu": set(), "mask": set()})
                record["seen"] += 1
                record["cpu"].add(cpu)
                record["mask"].add(tuple(mask))
        samples += 1
        if samples == 1:
            (a.out / "sampler-ready").touch()
        time.sleep(0.25)
    rows = [dict(tid=k[0], tgid=k[1], exe=k[2], cgroup=k[3], seen=v["seen"], cpu=sorted(v["cpu"]),
                 masks=sorted(v["mask"])) for k, v in evidence.items()]
    save(a.out / "samples.json", {"polls": samples, "tasks": rows})
    (a.out / "sampler-done").touch()


def await_file(path, seconds=15):
    for _ in range(seconds * 20):
        if path.is_file():
            return
        time.sleep(0.05)
    raise RuntimeError("sampler acknowledgment timeout: " + path.name)


def start(a):
    a.out.mkdir(parents=True, exist_ok=True)
    need(not (a.out / "start.json").exists(), "start requires a new audit interval")
    before = snap(a)
    instance = TRACE / "instances" / ("rust-affinity-" + secrets.token_hex(6))
    instance.mkdir()
    try:
        write(instance / "tracing_on", 0)
        write(instance / "buffer_size_kb", 4096)
        write(instance / "trace_clock", "mono")
        for event in EVENTS:
            write(instance / "events" / event / "enable", 1)
        (instance / "trace").write_text("\n")
        rec = dict(trace_instance=instance.name, before=before, cpu=a.cpu, events=EVENTS,
                   trace_clock=read(instance / "trace_clock"), trace_cpumask=read(instance / "tracing_cpumask"),
                   online=read(Path("/sys/devices/system/cpu/online")))
        save(a.out / "start.json", rec)
        with (a.out / "sampler.log").open("wb") as log:
            child = subprocess.Popen([sys.executable, __file__, "sample", "--out", str(a.out),
                                      "--init-pid", str(a.init_pid), "--cpu", str(a.cpu)],
                                     stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
        write(a.out / "sampler.pid", child.pid)
        await_file(a.out / "sampler-ready")
        write(instance / "tracing_on", 1)
    except BaseException:
        (a.out / "stop-sampling").touch()
        write(instance / "tracing_on", 0)
        for event in EVENTS:
            e = instance / "events" / event / "enable"
            if e.exists():
                write(e, 0)
        instance.rmdir()
        raise


def stop(a):
    rec = load(a.out / "start.json")
    instance = TRACE / "instances" / rec["trace_instance"]
    write(instance / "tracing_on", 0)
    (a.out / "stop-sampling").touch()
    try:
        data = (instance / "trace").read_bytes()
        (a.out / "events.trace").write_bytes(data)
        stats = {}
        for f in sorted((instance / "per_cpu").glob("cpu*/stats")):
            stats[f.parent.name] = dict((x.split(":",1)[0].strip(), x.split(":",1)[1].strip()) for x in read(f).splitlines() if ":" in x)
        save(a.out / "stop.json", dict(after=snap(a), stats=stats, event_sha256=hashlib.sha256(data).hexdigest()))
    finally:
        for event in EVENTS:
            write(instance / "events" / event / "enable", 0)
        instance.rmdir()
    await_file(a.out / "sampler-done")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("start", "stop", "sample"))
    parser.add_argument("--init-pid", required=True, type=int)
    parser.add_argument("--cpu", required=True, type=int)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    args.out = args.out.resolve()
    {"start": start, "stop": stop, "sample": run_sampler}[args.action](args)


if __name__ == "__main__":
    main()
