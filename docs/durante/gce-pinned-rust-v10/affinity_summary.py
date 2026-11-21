#!/usr/bin/env python3
"""Bind guest-wide Linux exec events and single-cgroup observations to a Rust reference dataset."""
from collections import Counter
from decimal import Decimal
import argparse
import hashlib
import gzip
import json
from pathlib import Path
import re


def need(ok, message):
    if not ok:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def digest_string(value):
    return sha(value.encode())


def read(file):
    return json.loads(file.read_bytes())


def public_snapshot(raw):
    return {field: raw[field] for field in ("init_pid", "cpuset_requested", "cpuset_effective", "allowed_cpus",
                                             "start_or_end_cpu", "observer_allowed_cpus")} | {
        "cgroup_path_sha256": digest_string(raw["path"])}


def expand_cpu_list(raw):
    result = []
    for interval in raw.split(","):
        bounds = list(map(int, interval.split("-")))
        result.extend(range(bounds[0], bounds[-1] + 1))
    return result


EVENT = re.compile(r"\[(\d+)\]\s+(?:\S+\s+)?(\d+\.\d+):\s+(sched_process_(?:exec|fork|exit)|cgroup_attach_task):\s+(.*)$")


def integer(info, field):
    return int(re.search(rf"(?:^| ){field}=(\d+)(?: |$)", info).group(1))


def is_same_group(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")


def extract_events(raw):
    events = []
    for line in raw.decode().splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        match = EVENT.search(line)
        need(match is not None, "malformed or unaccounted trace row: " + line[:160])
        cpu, timestamp, event, fields = match.groups()
        events.append((int(cpu), int(Decimal(timestamp) * 1_000_000_000), event, fields))
    return events


def summarize(folder, dataset, lineage_path):
    first, last, observations = (read(folder / name) for name in ("start.json", "stop.json", "samples.json"))
    dsraw = dataset.read_bytes()
    data = json.loads(dsraw)
    cp = first["cpu"]
    before, after = first["before"], last["after"]
    for key in ("init_pid", "init_start_ticks", "boot_id", "guest_id_sha256", "path", "observer_allowed_cpus"):
        need(before[key] == after[key], "scope changed during measurements: " + key)
    for scope in (before, after):
        need(scope["cpuset_requested"] == scope["cpuset_effective"] == str(cp) and scope["allowed_cpus"] == [cp]
             and scope["start_or_end_cpu"] == cp and cp not in scope["observer_allowed_cpus"],
             "scope not pinned or housekeeping overlaps selected CPU")
    cgroup = before["path"]
    match_id = re.search(r"(?:^|/)(?:docker-)?([0-9a-f]{64})(?:\.scope)?$", cgroup)
    need(match_id is not None, "unrecognized workload cgroup: " + cgroup)
    cid = match_id.group(1)
    required_events = {"sched/sched_process_exec", "sched/sched_process_fork", "sched/sched_process_exit",
                       "cgroup/cgroup_attach_task"}
    need(set(first["events"]) == required_events and "[mono]" in first["trace_clock"].split(),
         "unexpected trace clock or event configuration")
    online = expand_cpu_list(first["online"])
    need(set(last["stats"]) == {f"cpu{n}" for n in online}, "CPU coverage changed during collection")
    mask = int(first["trace_cpumask"].replace(",", ""), 16)
    need(all(mask & (1 << cpu) for cpu in online), "exec trace did not cover all online CPUs")
    events_raw = (folder / "events.trace").read_bytes()
    need(sha(events_raw) == last["event_sha256"], "guest trace SHA mismatch")
    events = extract_events(events_raw)
    stats = {}
    for cpu in online:
        k = f"cpu{cpu}"
        s = last["stats"][k]
        stats[k] = dict(entries=int(s["entries"]), overrun=int(s["overrun"]),
                        commit_overrun=int(s["commit overrun"]), dropped_events=int(s["dropped events"]),
                        consumed_events=int(s["read events"]))
        need(all(v == 0 for key,v in stats[k].items() if key != "entries"), "lost trace events on " + k)
    n_by_cpu = Counter(e[0] for e in events)
    need(all(n_by_cpu[cpu] == stats[f"cpu{cpu}"]["entries"] for cpu in online), "trace entry count does not reconcile")
    tasks = observations["tasks"]
    need(tasks and observations["polls"] > 0, "no independent task observations")
    for t in tasks:
        need(is_same_group(t["cgroup"], cgroup) and t["cpu"] == [cp] and t["masks"] == [[cp]], "unpinned observed thread")
    root = None
    recent_attach = {}
    compact_events = []
    root_attach = None
    alive = {}   # pid -> whether descended from a metered launcher in this process generation
    lineage = Counter()
    lineage_cpus = set()
    launches = []
    metered_images = Counter()
    sampled_roles = Counter()
    for cpu, when, kind, info in events:
        if kind == "sched_process_fork":
            pid = integer(info, "pid")
            if pid in alive:
                child = integer(info, "child_pid")
                compact_events.append(["fork", when, cpu, pid, child])
                alive[child] = alive[pid]
                lineage["births"] += 1
                lineage_cpus.add(cpu)
        elif kind == "sched_process_exit":
            pid = integer(info, "pid")
            if pid in alive:
                compact_events.append(["exit", when, cpu, pid])
                alive.pop(pid)
                lineage["exits"] += 1
                lineage_cpus.add(cpu)
        elif kind == "cgroup_attach_task":
            pid = integer(info, "pid")
            dest = re.search(r"(?:^| )dst_path=(\S+)", info).group(1)
            recent_attach[pid] = (when, cpu, dest)
            if pid in alive:
                need(is_same_group(dest, cgroup), "collector descendant changed to a different cgroup")
                compact_events.append(["attach", when, cpu, pid, digest_string(dest)])
                lineage["cgroup_moves"] += 1
        else:
            pid, old = integer(info, "pid"), integer(info, "old_pid")
            path = info.split("filename=", 1)[1].rsplit(" pid=", 1)[0]
            executable = Path(path).name
            if root is None:
                if executable != "durante-reference":
                    continue
                root = pid
                attached, attach_cpu, dest = recent_attach[pid]
                need(dest == cgroup and attached <= when, "collector was not placed into measured container cgroup")
                root_attach = [attached, attach_cpu, pid, digest_string(dest)]
                alive[pid] = False
            if old not in alive and pid not in alive:
                continue
            metered = alive.get(old, alive.get(pid, False))
            if old != pid:
                alive.pop(old, None)
            alive[pid] = metered
            label = executable if executable in ("durante-reference", "Main", "drrun") else "other"
            compact_events.append(["exec", when, cpu, pid, old, label, digest_string(path)])
            lineage["execs"] += 1
            lineage_cpus.add(cpu)
            if executable == "drrun":
                need(path == "/opt/durante/dynamorio/bin64/drrun" and not metered, "unexpected nested or alternate drrun")
                alive[pid] = True
            elif metered:
                metered_images[executable] += 1
                continue
            elif executable != "Main":
                continue
            launches.append(dict(program=executable, cpu=cpu, timestamp_ns=when, pid=pid,
                                 exec_path_sha256=digest_string(path)))
    need(root and lineage_cpus == {cp}, "collector lineage used an unexpected CPU or is missing")
    root_observed = any(t["tgid"] == root and t["exe"] == "durante-reference" for t in tasks)
    need(root_observed, "traced reference executable was not observed in the measured cgroup")
    for row in tasks:
        sampled_roles[row["exe"] if row["exe"] in ("durante-reference","Main","drrun") else "other"] += 1
    need(data["source_language"] == "RUST2024" and data["complete"] and len(data["samples"]) == 45, "expected complete long Rust dataset")
    need(len(launches) == 3*len(data["samples"]), f"target exec count {len(launches)} does not match the dataset")
    for i, obs in enumerate(launches):
        index, pos = divmod(i, 3)
        phase = ("native_before","metered","native_after")[pos]
        want = "drrun" if pos == 1 else "Main"
        need(obs["program"] == want and obs["cpu"] == cp, "unexpected launch order or CPU")
        need(i == 0 or obs["timestamp_ns"] > launches[i-1]["timestamp_ns"], "launch times are not strictly ordered")
        sample = data["samples"][index]
        obs.update(triplet_index=index, phase=phase, workload_id=sample["workload_id"], scale=sample["scale"],
                   repetition=sample["repetition"])
    tree = {"schema": 1, "raw_trace_sha256": last["event_sha256"], "root_attach": root_attach, "events": compact_events}
    tree_bytes = (json.dumps(tree, separators=(",", ":")) + "\n").encode()
    lineage_path.write_bytes(gzip.compress(tree_bytes, compresslevel=9, mtime=0))
    return dict(schema=1, dataset_sha256=sha(dsraw), selected_cpu=cp, housekeeping_cpus=before["observer_allowed_cpus"],
                container_id_sha256=digest_string(cid), guest_id_sha256=before["guest_id_sha256"],
                guest_boot_id_sha256=digest_string(before["boot_id"]), before=public_snapshot(before), after=public_snapshot(after),
                trace=dict(cpus=online, raw_sha256=last["event_sha256"], per_cpu=stats, trace_clock="mono", root_pid=root,
                           lineage_sha256=sha(tree_bytes), lineage_rows=len(compact_events), lineage_cpu_set=sorted(lineage_cpus), births=lineage["births"], execs=lineage["execs"],
                           exits=lineage["exits"], cgroup_moves=lineage["cgroup_moves"], metered_image_execs=dict(metered_images)),
                sampler=dict(polls=observations["polls"], unique_tids=len({t["tid"] for t in tasks}),
                             root_observed=root_observed, observed_cpu_set=[cp], allowed_cpu_sets=[[cp]],
                             sampled_task_images=dict(sampled_roles)), launches=launches)


if __name__ == "__main__":
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument("audit_dir", type=Path)
    args.add_argument("dataset", type=Path)
    args.add_argument("lineage_output", type=Path, help="write the compressed, sanitized collector process tree")
    cmd = args.parse_args()
    print(json.dumps(summarize(cmd.audit_dir, cmd.dataset, cmd.lineage_output), indent=2))
