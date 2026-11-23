#!/usr/bin/env python3
"""Replay the CPU-pinned Compute Engine Rust study and its paired Cloud Run comparison."""
import argparse
from collections import Counter
import gzip
import json
from pathlib import Path
import re

from durante_native_envelope import pair_bound
from verify_durante_v10_native import (
    ROOT, check_fit, collect_cases, digest, median_ns, ppm, replay_fit, require, score_case,
    summarize as replay_cloud_run,
)

LANGUAGE = "RUST2024"
PROTOCOL_SHA256 = "f72611411891542bd7518f5f5688afbef9362b4dfea63e8c8d79aa48baf6f8c3"
CLOUD_RUN = ROOT / "docs/durante/rust-long-v10"
PER_CASE_FIELDS = ("native_min_ns", "native_median_ns", "native_max_ns", "native_spread_ppm")


def read_pinned(path, expected_digest):
    raw = path.read_bytes()
    require(digest(raw) == expected_digest, f"wrong digest for {path.name}")
    return json.loads(raw)


def reference_inputs(protocol):
    prior = read_pinned(CLOUD_RUN / "protocol.json", protocol["cloud_run_protocol_sha256"])
    evaluated = read_pinned(CLOUD_RUN / LANGUAGE / "evaluation.json", protocol["cloud_run_long_evaluation_sha256"])
    results = read_pinned(CLOUD_RUN / "summary.json", protocol["cloud_run_summary_sha256"])
    require(results["platform_sha256"] == protocol["cloud_run_platform_sha256"] and
            results == replay_cloud_run(CLOUD_RUN), "published Cloud Run results do not replay")
    cloud_bytes = gzip.decompress((CLOUD_RUN / LANGUAGE / "dataset.json.gz").read_bytes())
    require(digest(cloud_bytes) == protocol["cloud_run_dataset_sha256"] == evaluated["dataset_sha256"],
            "wrong long-input Cloud Run comparison dataset")
    history = read_pinned(ROOT / "docs/durante/cloud-run-cpp20-v10-performance.json", prior["previous_clock_receipt_sha256"])
    short_fit = read_pinned(ROOT / "docs/durante/v10-native/RUST2024/evaluation.json", prior["rust_short_evaluation_sha256"])
    frozen = {"short_rust": short_fit["evaluation"]["candidate_policy"],
              "cloud_run_long_rust": evaluated["evaluation"]["candidate_policy"]}
    return prior, history, json.loads(cloud_bytes), results["languages"][LANGUAGE], frozen


def compare_native(cloud_rows, guest_rows, thresholds):
    require(cloud_rows.keys() == guest_rows.keys(), "unpaired native cases")
    cases = {}
    for label, guest in guest_rows.items():
        cloud = cloud_rows[label]
        cases[label] = {"cloud_run": {key: cloud[key] for key in PER_CASE_FIELDS},
                        "gce": {key: guest[key] for key in PER_CASE_FIELDS},
                        "median_change_ns": guest["native_median_ns"] - cloud["native_median_ns"],
                        "spread_change_ppm": guest["native_spread_ppm"] - cloud["native_spread_ppm"]}
    totals = {}
    for platform, samples in (("gce", guest_rows), ("cloud_run", cloud_rows)):
        spreads = sorted(case["native_spread_ppm"] for case in samples.values())
        totals[platform] = {
            "cases_within_spread_limit": sum(s <= thresholds["max_native_spread_ppm"] for s in spreads),
            "cases_meeting_duration_floor": sum(s["native_min_ns"] >= thresholds["min_native_ns"] for s in samples.values()),
            "median_case_spread_ppm": median_ns(spreads),
        }
    return {"cases": cases, "platforms": totals}


def binaries_by_workload(data):
    # collect_cases on the guest and replay_cloud_run on the baseline already
    # verify that the digest is constant for all nine rows of a workload.
    return {s["workload_id"]: s["binary_sha256"] for s in data["samples"]}


def targeted_bound(groups):
    def observed(label):
        return {"native": [n for pair, _ in groups[label] for n in pair],
                "features": [counts for _, counts in groups[label]]}

    outcome = pair_bound(observed("integer/4"), observed("fill/1"))
    return None if outcome is None else outcome[1]


def clock_comparison(result, groups, frozen, evaluation, thresholds):
    names = [key for key, row in result["cases"].items() if row["split"] == "validation"]
    summaries = {}
    predictions = {}
    for name, clock in {"common_v10": None, **frozen, "gce_long_rust": evaluation.get("candidate_policy")}.items():
        if name == "gce_long_rust" and clock is None:
            summaries[name] = {"fit_produced_coefficients": False}
            continue
        scores = (result["cases"] if name == "common_v10" else
                  {key: score_case(observations, clock, thresholds) for key, observations in groups.items()})
        predictions[name] = {key: {field: row[field] for field in
                                   ("trial_clock_ns", "worst_signed_error_ns", "worst_error_ppm")}
                             for key, row in scores.items()}
        worst = max(names, key=lambda key: scores[key]["worst_error_ppm"])
        summaries[name] = {
            "heldout_cases": len(names),
            "heldout_within_error_limit": sum(scores[key]["worst_error_ppm"] <= thresholds["max_error_ppm"] for key in names),
            "worst_heldout_case": worst,
            "worst_heldout_error_ppm": scores[worst]["worst_error_ppm"],
            "worst_heldout_signed_error_ns": scores[worst]["worst_signed_error_ns"],
        }
    return {"models": summaries, "predictions": predictions}


def measurement(directory, prior, history, cloud, cloud_result, frozen, reference):
    if reference is not None:
        reference = reference.resolve()
        require(digest(reference.read_bytes()) == prior["reference_sha256"], "incorrect reference executable")
    dataset_bytes = gzip.decompress((directory / LANGUAGE / "dataset.json.gz").read_bytes())
    dataset = json.loads(dataset_bytes)
    require(dataset["complete"] is True and not dataset.get("failures") and not (directory / LANGUAGE / "failure.json").exists(),
            "incomplete guest collection cannot establish a completed comparison")
    for field in ("arch", "os", "compiler_flags", "compiler_path", "compiler_version", "entry_artifact"):
        require(dataset["environment"][field] == cloud["environment"][field], f"Rust compiler or runtime differs: {field}")
    guest, groups = collect_cases(dataset, LANGUAGE, prior, history)
    raw_fit = (directory / LANGUAGE / "evaluation.json").read_bytes()
    receipt = json.loads(raw_fit)
    evaluation = check_fit(dataset_bytes, dataset, receipt, guest, groups, prior, history)
    if reference:
        replay_fit(reference, dataset_bytes, receipt, evaluation)
    cloud_binaries = binaries_by_workload(cloud)
    guest_binaries = binaries_by_workload(dataset)
    require(guest_binaries.keys() == cloud_binaries.keys(), "missing comparison program")
    for phase in ("native_before", "native_after"):
        for sample in dataset["samples"]:
            control = sample[phase]
            if control["cpu_accounting"]["selected_source"] == "wait_rusage":
                require(control["cpu_accounting"]["wait_target_ns"] == control["cpu_time_ns"],
                        "native wait accounting differs from its selected CPU time")
    guest.update(dataset_sha256=digest(dataset_bytes), evaluation_sha256=digest(raw_fit), fit_passed=evaluation["passed"])
    guest["diagnostic_cpu_over_native_ppm"] = ppm(2 * guest["diagnostic_cpu_sum_ns"], guest["native_control_cpu_sum_ns"])
    guest["cpu_accounting_sources"] = {
        phase: dict(Counter(s[phase]["cpu_accounting"]["selected_source"] for s in dataset["samples"]))
        for phase in ("native_before", "metered", "native_after")
    }
    return dataset, {
        "gce": guest,
        "clocks": clock_comparison(guest, groups, frozen, evaluation, prior["thresholds"]),
        "native_comparison": compare_native(cloud_result["cases"], guest["cases"], prior["thresholds"]),
        "integer4_fill1_bound": targeted_bound(groups),
        "binaries": {k: {"gce_sha256": sha, "cloud_run_sha256": cloud_binaries[k], "byte_identical": sha == cloud_binaries[k]}
                     for k, sha in guest_binaries.items()},
    }


def check_affinity(affinity, cpu, housekeeping, samples):
    online = sorted([cpu, *housekeeping])
    require(affinity["schema"] == 1 and affinity["selected_cpu"] == cpu and
            sorted(affinity["housekeeping_cpus"]) == sorted(housekeeping), "affinity CPU selection differs from the guest")
    for snap in (affinity["before"], affinity["after"]):
        require(snap["cpuset_requested"] == snap["cpuset_effective"] == str(cpu) and snap["allowed_cpus"] == [cpu] and
                snap["start_or_end_cpu"] == cpu and sorted(snap["observer_allowed_cpus"]) == sorted(housekeeping) and
                type(snap["init_pid"]) is int and snap["init_pid"] > 0 and
                re.fullmatch(r"[0-9a-f]{64}", snap["cgroup_path_sha256"]), "container lacks a hard single-CPU cpuset")
    require(all(affinity["before"][key] == affinity["after"][key] for key in ("init_pid", "cgroup_path_sha256")),
            "container lifetime changed during measurement")
    trace = affinity["trace"]
    require(sorted(trace["cpus"]) == online and set(trace["per_cpu"]) == {f"cpu{n}" for n in online} and
            trace["trace_clock"] == "mono" and type(trace["root_pid"]) is int and trace["root_pid"] > 0 and
            trace["lineage_cpu_set"] == [cpu] and re.fullmatch(r"[0-9a-f]{64}", trace["raw_sha256"]),
            "incomplete guest trace coverage or workload lineage on another CPU")
    for record in trace["per_cpu"].values():
        require(type(record["entries"]) is int and record["entries"] >= 0 and
                all(type(record[k]) is int and record[k] == 0 for k in ("overrun", "commit_overrun", "dropped_events", "consumed_events")),
                "lost CPU placement trace events")
    sampler = affinity["sampler"]
    require(sampler["polls"] > 0 and sampler["unique_tids"] > 0 and sampler["root_observed"] is True and
            sampler["allowed_cpu_sets"] == [[cpu]] and sampler["observed_cpu_set"] == [cpu],
            "sampled task affinity or actual CPU disagrees with the cpuset")
    launches = affinity["launches"]
    phases = (("native_before", "Main"), ("metered", "drrun"), ("native_after", "Main"))
    require(len(launches) == len(samples) * len(phases) and trace["execs"] >= len(launches) and
            trace["births"] > 0 and trace["exits"] > 0 and trace["cgroup_moves"] >= 0,
            "placement trace does not cover every measurement phase")
    last_time = -1
    for i, sample in enumerate(samples):
        for j, (phase, program) in enumerate(phases):
            launch = launches[i * len(phases) + j]
            require(launch["triplet_index"] == i and launch["phase"] == phase and launch["program"] == program and
                    all(launch[k] == sample[k] for k in ("workload_id", "scale", "repetition")),
                    "mislabeled or reordered workload launch")
            require(launch["cpu"] == cpu and type(launch["pid"]) is int and launch["pid"] > 0 and
                    type(launch["timestamp_ns"]) is int and launch["timestamp_ns"] > last_time and
                    re.fullmatch(r"[0-9a-f]{64}", launch["exec_path_sha256"]), "invalid guest CPU launch evidence")
            if phase == "metered":
                require(launch["exec_path_sha256"] == digest(b"/opt/durante/dynamorio/bin64/drrun"),
                        "metered launch lacks the pinned runtime executable path")
            last_time = launch["timestamp_ns"]


def check_lineage(raw, affinity):
    trace = affinity["trace"]
    require(digest(raw) == trace["lineage_sha256"], "collector process tree digest differs from placement evidence")
    tree = json.loads(raw)
    events = tree["events"]
    require(tree["schema"] == 1 and tree["raw_trace_sha256"] == trace["raw_sha256"] and
            events and len(events) == trace["lineage_rows"], "incomplete collector process tree")
    at, caller_cpu, root, initial_cgroup = tree["root_attach"]
    require(caller_cpu in trace["cpus"] and root == trace["root_pid"] and
            initial_cgroup == affinity["before"]["cgroup_path_sha256"], "collector was attached to a different cgroup")
    cpu = affinity["selected_cpu"]
    require(events[0][0] == "exec" and events[0][3] == root and events[0][5] == "durante-reference",
            "collector lineage is missing its initial exec")
    # Linux exec can change the pid when a nonleader thread replaces its image.
    # The boolean follows descendants of drrun so their execs cannot be counted
    # as additional native phases.
    live = {root: False}
    counts = Counter()
    events_by_cpu = Counter([caller_cpu])
    launches = []
    pending_loaders = set()
    loader_execs = 0
    # Tracefs reports the literal path passed to execve, including bin64/...
    loader_sha = digest(b"/opt/durante/dynamorio/bin64/../lib64/release/libdynamorio.so")
    last_time = at
    for event, timestamp, observed_cpu, pid, *fields in events:
        require(type(timestamp) is int and timestamp >= last_time and observed_cpu in trace["cpus"],
                "invalid timestamp or CPU in process tree")
        last_time = timestamp
        counts[event] += 1
        events_by_cpu[observed_cpu] += 1
        if event == "attach":
            # This CPU belongs to the caller performing the attachment.
            require(pid in live and fields == [initial_cgroup], "unverified cgroup destination in process tree")
            continue
        require(observed_cpu == cpu, "collector descendant observed outside the benchmark CPU")
        if event == "exec":
            old, role, path_sha = fields
            require((pid in live or old in live) and role in ("durante-reference", "drrun", "Main", "other") and
                    re.fullmatch(r"[0-9a-f]{64}", path_sha), "exec outside the traced process tree")
            metered = live.pop(old) if old in live else live[pid]
            if old in pending_loaders or pid in pending_loaders:
                require(pid == old and role == "other" and path_sha == loader_sha,
                        "drrun did not re-exec the pinned DynamoRIO loader in its own process")
                pending_loaders.remove(pid)
                loader_execs += 1
            elif metered and path_sha == loader_sha:
                raise ValueError("unmatched DynamoRIO loader exec")
            live[pid] = metered or role == "drrun"
            if role == "drrun":
                require(not metered, "unexpected nested metered launcher")
                pending_loaders.add(pid)
            if role in ("drrun", "Main") and not metered:
                launches.append((role, pid, observed_cpu, timestamp, path_sha))
        elif event == "fork":
            child, = fields
            require(pid in live and child not in live and type(child) is int and child > 0,
                    "orphan or duplicate fork in process tree")
            live[child] = live[pid]
        elif event == "exit":
            require(not fields and pid in live, "exit outside the traced process tree")
            require(pid not in pending_loaders, "drrun exited without executing the DynamoRIO loader")
            del live[pid]
        else:
            raise ValueError("unknown event in process tree")
    require(not live and all(counts[name] == trace[key] for name, key in (
        ("fork", "births"), ("exec", "execs"), ("exit", "exits"), ("attach", "cgroup_moves"))),
        "process tree lifetimes do not reconcile with placement evidence")
    require(all(n <= trace["per_cpu"][f"cpu{observed}"]["entries"] for observed, n in events_by_cpu.items()),
            "process tree exceeds the captured kernel event counts")
    published = [(p["program"], p["pid"], p["cpu"], p["timestamp_ns"], p["exec_path_sha256"]) for p in affinity["launches"]]
    require(launches == published, "phase launches do not match the observed executable lineage")
    require(not pending_loaders and loader_execs == sum(role == "drrun" for role, *_ in launches) and
            trace["metered_image_execs"].get("libdynamorio.so") == loader_execs,
            "DynamoRIO loader executions do not reconcile with the metered phases")


def check_attempts(directory, attempts, data, digest_final):
    completed = []
    failures = set()
    for attempt in attempts:
        sha = attempt["dataset_sha256"]
        require(type(attempt["complete"]) is bool and type(attempt["collector_exit"]) is int and
                re.fullmatch(r"[0-9a-f]{64}", sha), "invalid collector attempt identity")
        if attempt["complete"]:
            require(attempt["collector_exit"] == 0 and attempt["triplets"] == len(data["samples"]) and sha == digest_final,
                    "completed attempt differs from analyzed dataset")
            completed.append(attempt)
        else:
            require(sha not in failures, "duplicate failed collector receipt")
            failures.add(sha)
            original = read_pinned(directory / "attempts" / f"{sha}.json", sha)
            require(attempt["collector_exit"] > 0 and original["complete"] is False and
                    original["source_language"] == LANGUAGE and original["corpus_id"] == data["corpus_id"] and
                    original["runtime_image"] == data["runtime_image"] and len(original["samples"] or []) == attempt["triplets"] and
                    original["failures"] == [attempt["reason"]], "superseded collector failure does not match retained data")
    require(len(completed) == 1 and attempts[-1] == completed[0], "expected one final successful collection")
    require(failures == {p.stem for p in (directory / "attempts").glob("*.json")},
            "archived collection failures differ from the guest attempt history")


def check_topology(platform):
    topology = platform["topology"]
    cpus = platform["online_cpus"]
    house = platform["housekeeping_cpus"]
    require(len(topology) == len(cpus) and {row["cpu"] for row in topology} == set(cpus) and
            all(row["thread_siblings_list"] == str(row["cpu"]) for row in topology) and
            len({(row["physical_package_id"], row["core_id"]) for row in topology}) == len(cpus),
            "guest topology does not expose one hardware thread per reported core")
    slices = platform["housekeeping_slice_effective"]
    require(set(slices) == {"system.slice", "user.slice", "init.scope"} and
            all(mask == ",".join(map(str, house)) for mask in slices.values()), "housekeeping slices overlap the workload CPU")
    ticks = (platform["benchmark_steal_ticks_before"], platform["benchmark_steal_ticks_after"])
    require(all(type(tick) is int for tick in ticks) and 0 <= ticks[0] <= ticks[1] and
            type(platform["clock_ticks_per_second"]) is int and platform["clock_ticks_per_second"] > 0,
            "invalid guest steal-time observations")


def check_placement(platform, affinity, affinity_hash, data, guest, prior, prior_deployment):
    require(platform["target"] == "gce" and platform["zone"].startswith(platform["region"] + "-") and
            platform["machine_type"].startswith("n2d-") and platform["threads_per_core"] == 1 and
            platform["guest_cpu_platform"] and platform["boot_image"], "unexpected GCE machine or guest")
    online = platform["online_cpus"]
    cpu = platform["benchmark_cpu"]
    house = platform["housekeeping_cpus"]
    require(type(cpu) is int and cpu in online and len(online) == len(set(online)) and
            house and set(house) == set(online) - {cpu}, "benchmark CPU or housekeeping set is inconsistent")
    check_topology(platform)
    require(data["environment"]["cpu_model"] == platform["cpu_model"] and data["environment"]["kernel"] == platform["kernel"] and
            data["environment"]["logical_cpus"] == "1", "collector CPU, kernel or visible CPU count differs from guest evidence")
    require(platform["protocol_sha256"] == PROTOCOL_SHA256 and platform["affinity_sha256"] == affinity_hash and
            platform["dataset_sha256"] == affinity["dataset_sha256"] == guest["dataset_sha256"] and
            type(platform["collector_exit"]) is int and platform["collector_exit"] == 0 and
            platform["triplets"] == guest["completed_triplets"], "guest collector receipt differs from recorded data")
    require(platform["base_image"] == prior["base_image"] and platform["image_digest"] == data["runtime_image"] == prior_deployment["image_digest"] and
            platform["layer_sha256"] == prior_deployment["layer_sha256"] and platform["binaries"] == prior_deployment["binaries"] and
            re.fullmatch(r"sha256:[0-9a-f]{64}", platform["image_config_digest"]), "container image identity mismatch")
    for key in ("guest_id_sha256", "guest_boot_id_sha256", "container_id_sha256"):
        require(re.fullmatch(r"[0-9a-f]{64}", platform[key]) and platform[key] == affinity[key],
                "affinity evidence does not identify the measured guest and container")
    binaries = platform["binaries"]
    require(all(binaries[k] == prior[k] for k in ("worker_sha256", "client_sha256", "reference_sha256")) and
            binaries["commit"] == prior["durante_commit"] and binaries["profile"] == prior["engine_profile"],
            "deployed reference or Durante binary differs from frozen protocol")
    require(platform["cleanup"]["instance_http"] == 404 and platform["cleanup"]["boot_disk_http"] == 404 and
            platform["cleanup"]["disposable_ssh_key_removed"] is True,
            "missing deletion evidence for the guest or boot disk")
    check_affinity(affinity, cpu, house, data["samples"])


def summarize(directory, reference=None):
    protocol = read_pinned(directory / "protocol.json", PROTOCOL_SHA256)
    prior, history, cloud, cloud_result, frozen = reference_inputs(protocol)
    data, report = measurement(directory, prior, history, cloud, cloud_result, frozen, reference)
    platform_bytes = (directory / "platform.json").read_bytes()
    affinity_bytes = (directory / "affinity.json").read_bytes()
    platform = json.loads(platform_bytes)
    affinity = json.loads(affinity_bytes)
    prior_platform = read_pinned(CLOUD_RUN / "platform.json", protocol["cloud_run_platform_sha256"])
    prior_deployment, = prior_platform["deployments"]
    check_placement(platform, affinity, digest(affinity_bytes), data, report["gce"], prior, prior_deployment)
    check_attempts(directory, platform["attempts"], data, report["gce"]["dataset_sha256"])
    check_lineage(gzip.decompress((directory / "lineage.json.gz").read_bytes()), affinity)
    return {"protocol_sha256": PROTOCOL_SHA256, "platform_sha256": digest(platform_bytes),
            "affinity_sha256": digest(affinity_bytes), **report}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--reference", type=Path, help="rerun the Go fit using the pinned reference binary")
    parser.add_argument("--verify", type=Path, help="compare recomputed results with an archived summary")
    args = parser.parse_args()
    try:
        result = summarize(args.directory, args.reference)
        if args.verify:
            require(result == json.loads(args.verify.read_bytes()), "summary differs from measured evidence")
            print("Verified GCE native controls, CPU placement, clock comparisons and cleanup.")
        else:
            print(json.dumps(result, indent=2, allow_nan=False))
    except (KeyError, OSError, RuntimeError, ValueError, TypeError, ZeroDivisionError) as error:
        parser.exit(1, f"Verification failed: {error}\n")


if __name__ == "__main__":
    main()
