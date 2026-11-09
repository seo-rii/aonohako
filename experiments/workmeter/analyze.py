#!/usr/bin/env python3
"""Recompute completed experiment results; diagnostic work is never CPU time.

Raw execution reports, metadata, expected boundary outcomes and cohort membership
are verified before statistics. Missing JIT/native work remains unavailable.
"""
from __future__ import annotations

import argparse
import base64
from collections import Counter, defaultdict
import gzip
import hashlib
import io
import json
import math
from pathlib import Path
import statistics

import protocol
from validate import check_batch


def sha(data):
    return hashlib.sha256(data).hexdigest()


def distribution(values):
    values = list(values)
    if not values:
        return {"n": 0, "mean": None, "median": None, "cv_pct": None,
                "min": None, "max": None, "max_over_min": None}
    mean = statistics.mean(values)
    return {"n": len(values), "mean": mean, "median": statistics.median(values),
            "cv_pct": 100 * statistics.pstdev(values) / mean if len(values) > 1 and mean else None,
            "min": min(values), "max": max(values),
            "max_over_min": max(values) / min(values) if min(values) > 0 else None}


def work_distribution(values):
    values = list(values)
    available = [x for x in values if type(x) is int and x > 0]
    result = distribution(available)
    result.update(total_observations=len(values), unavailable=len(values) - len(available),
                  complete_series=len(available) == len(values))
    if len(available) != len(values):
        result["cv_pct"] = None
    return result


def unpack(packed, limit):
    if packed.get("encoding") != "gzip+base64":
        raise ValueError("wrong raw encoding")
    compressed = base64.b64decode(packed["data"], validate=True)
    with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as stream:
        raw = stream.read(limit + 1)
    if len(raw) > limit or len(raw) != packed["bytes"] or sha(raw) != packed["sha256"]:
        raise ValueError("raw report length or digest mismatch")
    return raw


def decode_counts(rows):
    counts = {}
    for row in rows:
        key = (row["kind"], row["number"])
        count = row["count"]
        if (key[0] not in ("e", "l", "b") or type(key[1]) is not int or key in counts
                or type(count) is not int or count < 0):
            raise ValueError("invalid JIT counter identity or value")
        counts[key] = count
    return counts


def verify_pypy(report):
    from pypy import meter
    before, after = decode_counts(report["baseline_counts"]), decode_counts(report["end_counts"])
    logging_enabled = bool(before or after)
    if report["logging_enabled"] != logging_enabled or report["counter_snapshot_nonempty"] != bool(after):
        raise ValueError("JIT logging evidence differs from raw snapshots")
    valid = (report["target_ok"] and logging_enabled and report.get("delta_error") is None
             and not report["hook_errors"] and not report["trace_capture_capped"])
    if report["valid"] is not bool(valid):
        raise ValueError("JIT validity differs from raw conditions")
    if report.get("delta_error") is None:
        delta = meter.delta_counts(before, after)
        if meter.encode_counts(delta) != report["delta_counts"]:
            raise ValueError("JIT counter delta mismatch")
    else:
        delta = {}
        if report.get("valid") is True:
            raise ValueError("valid JIT report has counter delta error")
    if report["mode"] == "mapped":
        harness = {"/opt/interpreter-meter/pypy/runner.py", "/opt/interpreter-meter/pypy/meter.py"}
        recomputed = meter.summarize(report["traces"], delta, harness)
        if report["hook_errors"] or report["trace_capture_capped"]:
            recomputed["available"] = False
        if recomputed != report["metric"]:
            raise ValueError("JIT mapping differs from raw trace reconstruction")


def verify_row(row, metadata_sha, expected_hashes=None):
    for field in ("cpu_ms", "user_cpu_ms", "system_cpu_ms", "wall_ms"):
        value = row[field]
        if type(value) not in (int, float) or not math.isfinite(value) or value < 0:
            raise ValueError("invalid CPU or wall observation")
    if not math.isclose(row["cpu_ms"], row["user_cpu_ms"] + row["system_cpu_ms"], abs_tol=1e-7):
        raise ValueError("CPU components mismatch")
    if row.get("language") == "python" and row.get("family") != "boundary":
        if expected_hashes is None or row.get("attestation") != {
                "pid": row["pid"], "hashes": expected_hashes, "isolated": 1,
                "ignore_environment": 1, "no_site": 1, "safe_path": True}:
            raise ValueError("actual seed or isolation differs from native build")
    if row.get("raw_jitlog") is not None:
        unpack(row["raw_jitlog"], 16 << 20)
    packed = row.get("raw_report")
    if packed is None:
        if row.get("metric") is not None:
            raise ValueError("metric has no raw report")
        return
    report = json.loads(unpack(packed, 64 << 20))
    if row["arm"] == "jit_mapped":
        verify_pypy(report)
    if row["arm"] == "direct":
        raise ValueError("direct control unexpectedly has a metric report")
    checked = protocol.summarize_metric(report, row["arm"], row["pid"])
    if checked != row["metric"]:
        raise ValueError("metric summary differs from raw reconstruction")
    if row["arm"] in ("source_plain", "ir_count") and checked["valid"]:
        if checked["metadata_manifest_sha256"] != metadata_sha:
            raise ValueError("report uses different native metadata")


def evidence(root, native):
    runtime_bytes = (root / "runtime-manifest.json").read_bytes()
    runtime = json.loads(runtime_bytes)
    manifest_bytes = (native / "metadata-manifest.json").read_bytes()
    manifest = json.loads(manifest_bytes)
    if sha(manifest_bytes) != runtime["metadata_manifest_sha256"]:
        raise ValueError("native manifest differs from runtime identity")
    files = manifest["files"]
    if set(files) != {p.name for p in (native / "metadata").iterdir() if p.is_file()}:
        raise ValueError("native metadata file set differs")
    for name, digest in files.items():
        if len(name) != 37 or not name.endswith(".json") or any(c not in "0123456789abcdef" for c in name[:-5]):
            raise ValueError("invalid metadata filename")
        if sha((native / "metadata" / name).read_bytes()) != digest:
            raise ValueError("native metadata digest mismatch")
    protocol.META = native / "metadata"
    protocol.metadata_manifest.cache_clear()
    protocol.block_weights.cache_clear()
    probe = json.loads((native / "provenance/hashseed-smoke.json").read_text())
    if probe["exit_code"] != 0 or len(probe["rows"]) != 2:
        raise ValueError("native seed gate failed")
    expected_hashes = probe["rows"][0]["fixed"]["1"]["hashes"]
    if expected_hashes != probe["rows"][1]["fixed"]["1"]["hashes"]:
        raise ValueError("native seed gates disagree")
    return sha(runtime_bytes), sha(manifest_bytes), len(files), expected_hashes


def workload_summary(rows):
    groups = defaultdict(list)
    for row in rows:
        groups[(row["workload_id"], row["arm"], row["parallelism"])].append(row)
    result = {}
    for (workload, arm, parallelism), samples in groups.items():
        result.setdefault(workload, {}).setdefault(str(parallelism), {})[arm] = {
            "cpu_ms": distribution(r["cpu_ms"] for r in samples),
            "wall_ms": distribution(r["wall_ms"] for r in samples),
            "work_units": work_distribution(r["work_units"] for r in samples),
            "terminations": dict(Counter(r["termination"] for r in samples)),
            "metric_states": dict(Counter(r["metric_state"] for r in samples)),
            "observations": len(samples),
        }
    return result


def paired_cost(rows):
    groups = defaultdict(dict)
    for row in rows:
        key = tuple(row[k] for k in ("service", "workload_id", "iteration", "parallelism", "copy_index"))
        groups[key][row["arm"]] = row
    costs = defaultdict(list)
    for key, arms in groups.items():
        plain, meter = (("source_plain", "ir_count") if "source_plain" in arms else ("direct", "jit_mapped"))
        if plain not in arms or meter not in arms:
            continue
        a, b = arms[plain], arms[meter]
        if a["cpu_ms"] <= 0:
            continue
        costs[(key[1], key[3])].append({"ratio": b["cpu_ms"] / a["cpu_ms"],
            "added_cpu_ms": b["cpu_ms"] - a["cpu_ms"], "plain_cpu_ms": a["cpu_ms"],
            "meter_cpu_ms": b["cpu_ms"]})
    result = {}
    for (workload, parallelism), values in costs.items():
        result.setdefault(workload, {})[str(parallelism)] = {
            key: distribution(v[key] for v in values)
            for key in ("ratio", "added_cpu_ms", "plain_cpu_ms", "meter_cpu_ms")
        }
    return result


def scaling_summary(workloads, summary):
    groups = defaultdict(dict)
    for w in workloads:
        if w.get("scale") in (1, 2, 4) and w.get("kind") == "diagnostic":
            groups[(w["language"], w["family"])][w["scale"]] = w["id"]
    result = []
    for (language, family), ids in sorted(groups.items()):
        for arm in protocol.arms_for(language):
            values = {}
            for scale, workload in sorted(ids.items()):
                record = summary.get(workload, {}).get("1", {}).get(arm)
                if record:
                    values[str(scale)] = {"cpu_median_ms": record["cpu_ms"]["median"],
                        "work_median": record["work_units"]["median"],
                        "work_complete": record["work_units"]["complete_series"]}
            ratios = {}
            for scale in (2, 4):
                base, larger = values.get("1"), values.get(str(scale))
                if not base or not larger:
                    continue
                cpu = larger["cpu_median_ms"] / base["cpu_median_ms"] if base["cpu_median_ms"] else None
                work = (larger["work_median"] / base["work_median"]
                        if base["work_median"] and larger["work_median"]
                        and base["work_complete"] and larger["work_complete"] else None)
                ratios[str(scale)] = {"cpu_ratio": cpu, "work_ratio": work,
                    "flat_work_while_cpu_grows": bool(work is not None and work <= 1.1 and cpu is not None and cpu >= 1.5)}
            result.append({"language": language, "family": family, "arm": arm,
                "serial_medians": values, "ratios_to_scale1": ratios,
                "flatness_heuristic": "work ratio <=1.1 while CPU ratio >=1.5; descriptive coverage signal only"})
    return result


def selected_case_max(rows, workloads, failures):
    expected = defaultdict(set)
    for w in workloads:
        if w.get("kind") == "submission":
            expected[w["submission_id"]].add(w["id"])
    groups = defaultdict(list)
    for row in rows:
        if row["submission_id"] in expected:
            key = tuple(row[k] for k in ("submission_id", "service", "iteration", "parallelism", "copy_index", "arm"))
            groups[key].append(row)
    maxima = defaultdict(list)
    for key, cases in groups.items():
        if {r["workload_id"] for r in cases} != expected[key[0]] or len(cases) != len(expected[key[0]]):
            failures.append({"stage": "selected_case_aggregation", "key": list(key), "error": "missing or duplicate case"})
            continue
        available = all(type(r["work_units"]) is int and r["work_units"] > 0 for r in cases)
        maxima[(key[0], key[3], key[5])].append({
            "cpu_ms": max(r["cpu_ms"] for r in cases),
            "work_units": max(r["work_units"] for r in cases) if available else None,
            "cpu_max_case": max(cases, key=lambda r: r["cpu_ms"])["workload_id"],
            "work_max_case": max(cases, key=lambda r: r["work_units"])["workload_id"] if available else None,
            "service": key[1], "iteration": key[2], "copy_index": key[4],
        })
    output = {}
    for (submission, parallelism, arm), values in maxima.items():
        output.setdefault(str(submission), {}).setdefault(str(parallelism), {})[arm] = {
            "selected_cases": len(expected[submission]), "full_case_rejudge": False,
            "cpu_ms": distribution(v["cpu_ms"] for v in values),
            "work_units": work_distribution(v["work_units"] for v in values), "samples": values,
        }
    return output


def analyze_fanout(root, workloads, config, instances, runtime_sha, fixture_sha, metadata_sha, repeats, failures, expected_hashes=None):
    selected = {w["id"]: w for w in workloads if w.get("kind") == "submission" and w["language"] == "python"}
    expected = {(wid, i, arm) for wid in selected for i in range(repeats)
                for arm in protocol.arms_for("python")}
    seen = set(); samples = []; inputs = []; observed_rows = 0; observed_batches = 0
    for path in sorted(root.glob("*fanout.ndjson")):
        raw = path.read_bytes()
        inputs.append({"path": path.name, "bytes": len(raw), "sha256": sha(raw)})
        for number, line in enumerate(raw.splitlines(), 1):
            observed_batches += 1
            identity = {"file": path.name, "line": number}
            try:
                batch = json.loads(line); request = batch["request"]
                if len(request["arms"]) != 1 or request["parallelism"] != 1:
                    raise ValueError("invalid fanout request shape")
                key = (request["workload_id"], request["iteration"], request["arms"][0])
                if key not in expected or key in seen:
                    raise ValueError("unexpected or duplicate fanout batch")
                seen.add(key)
                results = batch["results"]
                observed_rows += sum(len(r.get("rows", [])) for r in results)
                services = [r["service"] for r in results]
                if len(services) != len(set(services)) or set(services) != set(config["services"]):
                    raise ValueError("missing or duplicate fanout service")
                cpus = []; target_walls = []; request_walls = []
                for result in results:
                    errors = check_batch(result, selected[key[0]], request, instances.get(result["service"]))
                    if errors:
                        raise ValueError(";".join(errors))
                    env = result["environment"]
                    if env["runtime_manifest_sha256"] != runtime_sha or env["fixture_manifest_sha256"] != fixture_sha:
                        raise ValueError("fanout runtime or fixture identity differs")
                    row = result["rows"][0]
                    verify_row(row, metadata_sha, expected_hashes)
                    cpus.append(row["cpu_ms"]); target_walls.append(row["wall_ms"])
                    request_walls.append(result["client_wall_ms"])
                wall = batch["client_batch_wall_ms"]
                if type(wall) not in (int, float) or not math.isfinite(wall) or wall <= 0:
                    raise ValueError("invalid fanout batch wall")
                if not math.isclose(sum(cpus), batch["sum_target_cpu_ms"], rel_tol=1e-12, abs_tol=1e-7):
                    raise ValueError("fanout target CPU sum mismatch")
                samples.append({"workload_id": key[0], "iteration": key[1], "arm": key[2],
                    "batch_wall_ms": wall, "sum_target_cpu_ms": sum(cpus),
                    "max_target_wall_ms": max(target_walls), "max_request_wall_ms": max(request_walls),
                    "services": services})
            except Exception as error:
                failures.append({"stage": "fanout_validation", "identity": identity,
                    "type": type(error).__name__, "error": str(error)})
    missing = expected - seen
    if missing:
        failures.append({"stage": "fanout_completeness", "missing_batches": len(missing),
                         "identities": [list(k) for k in sorted(missing)]})
    groups = defaultdict(list); pairs = defaultdict(dict)
    for sample in samples:
        groups[(sample["workload_id"], sample["arm"])].append(sample)
        pairs[(sample["workload_id"], sample["iteration"])][sample["arm"]] = sample
    summary = {}
    for (wid, arm), values in groups.items():
        summary.setdefault(wid, {})[arm] = {
            metric: distribution(row[metric] for row in values)
            for metric in ("batch_wall_ms", "sum_target_cpu_ms", "max_target_wall_ms", "max_request_wall_ms")
        }
    cost = defaultdict(list)
    for (wid, iteration), arms in pairs.items():
        if set(arms) == {"source_plain", "ir_count"}:
            plain, meter = arms["source_plain"], arms["ir_count"]
            cost[wid].append({"iteration": iteration,
                "batch_wall_ratio": meter["batch_wall_ms"] / plain["batch_wall_ms"],
                "added_batch_wall_ms": meter["batch_wall_ms"] - plain["batch_wall_ms"],
                "target_cpu_ratio": meter["sum_target_cpu_ms"] / plain["sum_target_cpu_ms"]})
    return {"expected_batches": len(expected), "observed_batches": observed_batches,
        "observed_executions": observed_rows, "validated_batches": len(samples), "inputs": inputs,
        "scope": "Four-service concurrent dispatch including RPC and report handling; not production Eden latency.",
        "included_in_main_cv": False, "workloads": summary,
        "paired_cost": {wid: {metric: distribution(r[metric] for r in values)
            for metric in ("batch_wall_ratio", "added_batch_wall_ms", "target_cpu_ratio")}
            for wid, values in cost.items()}, "samples": samples}


def analyze(root, native, repeats=3):
    runtime_sha, metadata_sha, metadata_count, expected_hashes = evidence(root, native)
    fixture_bytes = (root / "fixtures/manifest.json").read_bytes()
    workloads = json.loads(fixture_bytes)["workloads"]
    by_id = {w["id"]: w for w in workloads}
    config = json.loads((root / "config.json").read_text())
    stress = {w["id"] for w in workloads if w.get("kind") == "submission" or w.get("scale") == 4}
    expected = {(s, w["id"], i, p) for s in config["services"] for w in workloads
                for i in range(repeats) for p in (1, 2) if p == 1 or w["id"] in stress}
    seen = set(); rows = []; failures = []; inputs = []; instances = {}; profiles = {}; raw_rows = 0; lines = 0
    boundary_counts = Counter(); batches = []
    for path in sorted(root.glob(config["prefix"] + "-*-responses.ndjson")):
        data = path.read_bytes()
        inputs.append({"path": path.name, "bytes": len(data), "sha256": sha(data)})
        for number, line in enumerate(data.splitlines(), 1):
            lines += 1
            identity = {"file": path.name, "line": number}
            try:
                result = json.loads(line); request = result["request"]; service = result["service"]
                raw_rows += len(result.get("rows", []))
                key = (service, request["workload_id"], request["iteration"], request["parallelism"])
                if key not in expected or key in seen:
                    raise ValueError("unexpected or duplicate cohort identity")
                seen.add(key)
                w = by_id[key[1]]
                if set(request["arms"]) != set(protocol.arms_for(w["language"])):
                    raise ValueError("incomplete requested arm set")
                errors = check_batch(result, w, request, instances.get(service))
                if errors:
                    raise ValueError(";".join(errors))
                env = result["environment"]; instance = env["instance_id"]
                instances[service] = instance; profiles[instance] = env
                if env["runtime_manifest_sha256"] != runtime_sha or env["fixture_manifest_sha256"] != sha(fixture_bytes):
                    raise ValueError("runtime or fixture identity differs")
                accepted = []
                for row in result["rows"]:
                    verify_row(row, metadata_sha, expected_hashes)
                    metric = row.get("metric") or {}
                    work = metric.get("work_units") if metric.get("valid") is True else None
                    if type(work) is not int or work <= 0:
                        work = None
                    state = ("no_report" if not row.get("raw_report") else "invalid"
                             if metric.get("valid") is not True else "available_partial"
                             if work is not None else "no_positive_work")
                    accepted.append({"service": service, "instance_id": instance,
                        "workload_id": w["id"], "submission_id": w.get("submission_id"),
                        "language": w["language"], "family": w.get("family"), "scale": w.get("scale"),
                        "boundary_condition": w.get("boundary"), "iteration": key[2], "parallelism": key[3],
                        "copy_index": row["copy_index"], "arm": row["arm"], "cpu_ms": row["cpu_ms"],
                        "wall_ms": row["wall_ms"], "work_units": work, "metric_state": state,
                        "termination": row["termination"], "exit_code": row["exit_code"],
                        "report_sha256": row["raw_report"]["sha256"] if row.get("raw_report") else None})
                rows.extend(accepted)
                if w.get("boundary"):
                    for row in accepted:
                        boundary_counts[(w["language"], w["boundary"], row["arm"], row["termination"], row["metric_state"])] += 1
                batches.append({"service": service, "workload_id": w["id"], "iteration": key[2],
                    "parallelism": key[3], "request_wall_ms": result.get("request_wall_ms"), "arms": result.get("batches", [])})
            except Exception as error:
                failures.append({"stage": "record_validation", "identity": identity,
                                 "type": type(error).__name__, "error": str(error)})
    missing = expected - seen
    if missing:
        failures.append({"stage": "cohort_completeness", "missing_batches": len(missing),
                         "identities": [list(k) for k in sorted(missing)]})
    if len(set(instances.values())) != len(config["services"]):
        failures.append({"stage": "instance_completeness", "observed": len(set(instances.values())), "expected": len(config["services"])})
    summary = workload_summary(rows)
    originals = selected_case_max(rows, workloads, failures)
    fanout = analyze_fanout(root, workloads, config, instances, runtime_sha, sha(fixture_bytes), metadata_sha, repeats, failures, expected_hashes)
    return {"schema": 1, "exit_code": int(bool(failures)), "failures": failures,
        "expected_batches": len(expected), "observed_batches": lines, "observed_executions": raw_rows,
        "validated_executions": len(rows), "metadata_files_verified": metadata_count,
        "inputs": inputs, "runtime_manifest_sha256": runtime_sha, "metadata_manifest_sha256": metadata_sha,
        "instance_profiles": profiles, "workloads": summary, "paired_cost": paired_cost(rows),
        "scaling": scaling_summary(workloads, summary), "selected_case_submissions": originals,
        "boundaries": [{"language": k[0], "condition": k[1], "arm": k[2], "termination": k[3],
                        "metric_state": k[4], "count": v} for k, v in sorted(boundary_counts.items())],
        "samples": rows, "batches": batches, "fanout": fanout,
        "fanout_files_excluded_from_main_cv": [p.name for p in sorted(root.glob("*fanout.ndjson"))],
        "limitations": [
            "Selected cases only; submission statistics are not full-case rejudges.",
            "Software work counts are diagnostic, partial coverage, and not milliseconds or TLE authority.",
            "Missing/zero/invalid work is unavailable; it never establishes zero execution cost.",
            "Same-instance parallelism=2 is interference stress, not production TC fanout.",
            "Distinct Cloud Run instances may share physical hosts; this cohort is not cross-date validation.",
            "CPU includes process startup, target, report serialization and teardown.",
            "Any invalid observations remain failures and are excluded only from explicitly validated summaries.",
        ]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.repeats < 1:
        parser.error("repeats must be positive")
    root = args.root.resolve()
    result = analyze(root, args.evidence or root / "cpython/remote-1/evidence", args.repeats)
    destination = args.output or root / "analysis.json"
    destination.write_text(json.dumps(result, indent=2, allow_nan=False) + "\n")
    destination.chmod(0o600)
    print(json.dumps({"exit_code": result["exit_code"], "validated_executions": result["validated_executions"],
                      "failures": len(result["failures"]), "output": str(destination)}))
    return result["exit_code"]


if __name__ == "__main__":
    raise SystemExit(main())
