#!/usr/bin/env python3
"""Compare a frozen CPP20 work clock with actual native verdicts at fixed CPU limits."""
import argparse
import base64
from collections import defaultdict
import hashlib
import http.client
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error

from verify_durante_observation import RESOURCE_POLICY, compile_cpp, post, require, server

AC = "Accepted"
TLE = "Time Limit Exceeded"
COMBINED = "weighted-work-nanoseconds-v1"
SPLIT = "weighted-rep-operations-nanoseconds-v2"
CPU_SOURCES = {"cpu_time", "cpu_time_final", "cpu_time_cgroup", "cpu_time_cgroup_final"}
# Go's TimePolicy struct order is part of WorkClockRequest.ModelBinding().
POLICY_FIELDS = ("method", "model_id", "model_version", "reference_id", "runtime_id", "family",
                 "engine_profile", "dispatch_profile", "cpuid_profile", "instruction_ns_numerator",
                 "rep_byte_ns_numerator", "movs_byte_ns_numerator", "stos_byte_ns_numerator", "ns_denominator")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, obj):
    path.write_text(json.dumps(obj, ensure_ascii=False, indent=2, allow_nan=False) + "\n", encoding="utf-8")


def thresholds(controls_ns):
    values = sorted(controls_ns)
    require(len(values) >= 6 and len(values) % 2 == 0 and
            all(type(n) is int and n > 0 for n in values), "expected at least three pairs of native CPU controls")
    middle = len(values) // 2
    twice_median = values[middle - 1] + values[middle]
    low = max(1, 4 * twice_median // 10_000_000)
    high = (6 * twice_median + 9_999_999) // 10_000_000
    require(2 * low * 1_000_000 < twice_median < 2 * high * 1_000_000,
            "native median cannot be bracketed by two positive whole-millisecond limits")
    return low, high


def weighted_ns(policy, instructions, rep_bytes, breakdown):
    total = instructions * policy["instruction_ns_numerator"]
    if policy.get("method") == SPLIT:
        total += (breakdown["movs_bytes"] * policy["movs_byte_ns_numerator"] +
                  breakdown["stos_bytes"] * policy["stos_byte_ns_numerator"])
    else:
        total += rep_bytes * policy["rep_byte_ns_numerator"]
    denominator = policy["ns_denominator"]
    return (total + denominator - 1) // denominator


def plan_limits(native_ns, reports, policy, max_nominal):
    # One row per actual request limit, even when two selection rules coincide.
    limits, exclusions = {}, []
    try:
        low, high = thresholds(native_ns)
    except RuntimeError as error:
        exclusions.append({"boundary": "native_bracket", "error": str(error)})
    else:
        for label, limit in (("low", low), ("high", high)):
            item = {"boundary": label, "nominal_ms": limit}
            if limit <= max_nominal:
                limits[limit] = item
            else:
                exclusions.append({**item, "error": "watchdog limits leave insufficient headroom"})

    # Frozen v1/v2 collection binaries can predate the optional interval in
    # evaluation.json. Use their validated observations and exact clock weights.
    predicted = [weighted_ns(policy, r["instructions"], r["rep_bytes"], r.get("rep_breakdown")) for r in reports]
    if max(predicted) > 2**63 - 1:
        exclusions.append({"boundary": "observed_disagreement", "error": "frozen estimate exceeds int64 nanoseconds"})
    else:
        # A time equal to the limit finishes within budget. The upper end is strict.
        native_ms = [(n + 999_999) // 1_000_000 for n in native_ns]
        predicted_ms = [(n + 999_999) // 1_000_000 for n in predicted]
        for direction, faster, slower in (("false_accept", predicted_ms, native_ms),
                                          ("false_timeout", native_ms, predicted_ms)):
            first, last = max(1, max(faster)), min(600_000, min(slower) - 1)
            if first > last:
                continue
            gap = {"direction": direction, "min_time_ms": first, "max_time_ms": last}
            if first > max_nominal:
                exclusions.append({"boundary": "observed_disagreement", "observed_deadline_disagreement": gap,
                                   "error": "watchdog limits leave insufficient headroom"})
            else:
                middle = (first + min(last, max_nominal)) // 2
                row = limits.setdefault(middle, {"boundary": "observed_disagreement", "nominal_ms": middle})
                row["observed_deadline_disagreement"] = gap
    return list(limits.values()), exclusions


def model_binding(policy, runtime_options_id, determinism=""):
    # Go omits optional empty fields and HTML-escapes strings in encoding/json.
    effective = {k: policy[k] for k in POLICY_FIELDS if policy.get(k)}
    bound = {"Policy": effective, "RuntimeOptionsID": runtime_options_id, "RuntimeDeterminism": determinism}
    wire = json.dumps(bound, ensure_ascii=False, separators=(",", ":"))
    for char in "<>&\u2028\u2029":
        wire = wire.replace(char, "\\u%04x" % ord(char))
    return "work-clock-v1:sha256:" + digest(wire.encode())


def check_native(response, nominal_ms, limits=None):
    require(not any(k.startswith("work_measurement") for k in response) and
            not any(k in response for k in ("cpu_time_normalization", "cpu_time_sampling", "raw_cpu_time_ns", "raw_cpu_time_ms")),
            "expected uninstrumented, unnormalized, unsampled native execution")
    ns = response.get("cpu_time_ns")
    accounting = response.get("cpu_accounting") or {}
    require(type(ns) is int and ns > 0 and response.get("cpu_time_ms") == (ns + 999_999) // 1_000_000 and
            accounting.get("selected_source") in ("process_clock", "wait_rusage", "cgroup"),
            "inconsistent native CPU accounting")
    if limits is not None:
        require(0 <= response["wall_time_ms"] <= limits["wall_time_ms"] and
                0 <= response["memory_kb"] <= limits["memory_mb"] * 1024, "native execution exceeded a non-CPU limit")
    require(response.get("score") is None and not response.get("stdout_truncated") and not response.get("stderr_truncated"),
            "unexpected native score or truncated output")
    if response["status"] == AC:
        require(response.get("verdict_source") == "stdout" and response.get("exit_code") == 0 and
                ns <= nominal_ms * 1_000_000, "unverified native stdout acceptance")
    else:
        require(response["status"] == TLE and response.get("verdict_source") in CPU_SOURCES and
                ns > nominal_ms * 1_000_000,
                "native verdict did not establish a CPU cutoff")


def classify(durante_status, native_statuses):
    require(durante_status in (AC, TLE) and len(native_statuses) == 3 and all(s in (AC, TLE) for s in native_statuses),
            "comparison requires one Durante and three native verdicts")
    matches = native_statuses.count(durante_status)
    if matches == len(native_statuses):
        return "unanimous_agreement"
    if matches == 0:
        return "unanimous_opposition"
    return "mixed"


def check_optional_counters(report, dataset):
    for name, value_key in (("latency_bound", "cycles_x16"), ("memory_operand_bytes", "bytes")):
        if not report["complete"] or not dataset.get(name + "_version"):
            require(name not in report, f"unexpected {name} counter")
            continue
        counter = report.get(name)
        require(isinstance(counter, dict) and set(counter) == {"version", value_key} and
                type(counter["version"]) is int and counter["version"] == dataset[name + "_version"] and
                type(counter[value_key]) is int and 0 <= counter[value_key] < 2**64,
                f"invalid or missing {name} counter")


def check_clock(response, req, policy, options_id, dataset, installation):
    judgment = response["work_measurement_judgment"]
    execution = response["work_measurement_execution"]
    report = response["work_measurement"]
    clock = execution["clock"]
    determinism = dataset.get("runtime_determinism", "")
    nominal = req["limits"]["time_ms"]
    hard = req["work_measurement_limits"]
    status = response["status"]
    cause = execution["termination_source"]
    require(response.get("verdict_source") == "durante_judge" and judgment["verdict"] == status and
            judgment["authority"] == execution["authority"] == report["authority"] == "diagnostic_only" and
            judgment["nominal_time_ms"] == nominal and judgment["integrity_mode"] == "off" and
            judgment["state"] == execution["state"] and judgment["termination_source"] == cause,
            "missing or inconsistent ordinary judge decision")
    require(execution["raw_limits"] == hard and execution["nominal_limits"] == req["limits"] and
            0 < response["cpu_time_ns"] <= hard["cpu_time_ms"] * 1_000_000 and
            0 <= response["cpu_time_ms"] <= hard["cpu_time_ms"] and
            0 <= response["wall_time_ms"] <= hard["wall_time_ms"] and
            0 <= response["memory_kb"] <= hard["memory_mb"] * 1024, "outside expected raw envelope")
    require(report["installation"] == installation and report["family"] == policy["family"] and
            report["engine_profile"] == policy["engine_profile"] and report["dispatch_profile"] == policy["dispatch_profile"],
            "different Durante installation or measurement profile")
    check_optional_counters(report, dataset)
    require(clock["version"] == 1 and clock["metric"] == "model_weighted_nanoseconds_v1" and
            clock["budget"] == nominal * 1_000_000 and clock["state"] == "observed" and
            clock["model_binding"] == model_binding(policy, options_id, determinism), "unverified work-clock binding")
    p = clock["progress"]
    counts = [p[k] for k in ("instructions", "movs_bytes", "stos_bytes")]
    overflow = p.get("overflow_mask", 0)
    require(type(overflow) is int and 0 <= overflow <= 7 and
            all(type(v) is int and 0 <= v < 2**64 and (not (overflow & (1 << i)) or v == 2**64 - 1)
                for i, v in enumerate(counts)), "invalid work-clock progress")
    # A saturated counter with an overflow flag proves a lower bound of 2**64.
    instructions, movs, stos = (2**64 if overflow & (1 << i) else count for i, count in enumerate(counts))
    elapsed_ns = weighted_ns(policy, instructions, movs + stos, {"movs_bytes": movs, "stos_bytes": stos})
    crossed = elapsed_ns > clock["budget"]
    eligibility = execution["time_estimate_eligibility"]
    if status == AC:
        require(cause == "stdout" and execution["state"] == "complete" and response.get("score") == 1 and
                execution.get("output_matches") is True and not crossed, "unverified Durante acceptance")
    else:
        require(status == TLE and cause in ("durante_work_clock", "durante_work_clock_final") and
                execution["state"] == "budget_exceeded" and crossed and response.get("score") is None,
                "Durante verdict did not establish a strict model-clock cutoff")
    if cause == "durante_work_clock":
        require(not report["complete"] and response.get("exit_code") is None and execution.get("output_matches") is None and
                execution.get("time_estimate") is None and eligibility["eligible"] is False,
                "live cutoff unexpectedly claimed terminal evidence")
    else:
        estimate = execution.get("time_estimate")
        breakdown = report["rep_breakdown"]
        require(report["complete"] and not overflow and report["signals_observed"] == 0 and report["unsupported_rep_entries"] == 0 and
                report.get("cpuid_profile", "") == policy.get("cpuid_profile", "") and
                report.get("runtime_determinism", "") == determinism and
                [report["instructions"], breakdown["movs_bytes"], breakdown["stos_bytes"]] == counts and
                response.get("exit_code") == 0 and execution.get("output_matches") is True,
                "terminal work-clock counters or output did not reconcile")
        if estimate is None:
            require(status == TLE and elapsed_ns > 2**63 - 1 and eligibility["eligible"] is False and
                    eligibility["reason_codes"] == ["numeric_overflow"] and execution.get("nominal_time_exceeded") is True,
                    "missing terminal estimate")
            return
        require(eligibility["eligible"] is True and estimate["authority"] == "diagnostic_only" and
                estimate["runtime_options_id"] == options_id and estimate["nanoseconds"] == elapsed_ns and
                all(estimate.get(k) == v for k,v in policy.items() if k != "method" and v) and
                estimate["method"] == (policy.get("method") or COMBINED), "unverified terminal work-clock estimate")


def validate_inputs(args, summary):
    # The reference validator checks every sample, binary identity, corpus case, profile and native control.
    dest = args.output / "reference-validation"
    argv = [args.reference, "validate", "--input", args.dataset, "--validation-manifest", args.manifest,
            "--frozen-policy", args.frozen_policy, "--output", dest]
    result = subprocess.run(argv, capture_output=True, timeout=120)
    summary["reference_validation_exit"] = result.returncode
    require(result.returncode in (0, 2), "reference validation failed: " + result.stderr.decode(errors="replace")[:1500])
    receipt = json.loads((dest / "evaluation.json").read_bytes())
    inputs = []
    for key, name in (("corpus_sha256","manifest"), ("frozen_policy_sha256","frozen_policy"), ("dataset_sha256","dataset")):
        data = getattr(args, name).read_bytes()
        summary["inputs"][key] = digest(data)
        require(receipt[key] == summary["inputs"][key], f"{name} changed since reference validation")
        inputs.append(json.loads(data))
    manifest, policy, dataset = inputs
    require(dataset["runner_sha256"] == summary["inputs"]["reference_sha256"] and
            dataset["runtime_image"] == args.runtime_image, "reference binary or runtime image differs from collection")
    require((policy.get("method") or COMBINED) in (COMBINED, SPLIT),
            "live enforcement requires a frozen instruction+REP or instruction+MOVS+STOS policy")
    return manifest, policy, dataset


def judge_config(args, dataset, policy):
    hard = dataset["metered_limits"]
    # One active run, 256 MiB work root and the same service reservation as the native smoke helper.
    resources = {**RESOURCE_POLICY, "max_limits": hard}
    resources["memory_budget_mb"] = max(resources["memory_budget_mb"], hard["memory_mb"] + 256 + resources["reserved_memory_mb"])
    env = {"AONOHAKO_DURANTE_FAMILIES": "cpp", "AONOHAKO_DURANTE_DISPATCH_PROFILE": dataset["dispatch_profile"],
           "AONOHAKO_DURANTE_CPUID_PROFILE": dataset.get("cpuid_profile", ""),
           "AONOHAKO_DURANTE_LATENCY_BOUND": "v1" if dataset.get("latency_bound_version") == 1 else "off",
           "AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES": "v1" if dataset.get("memory_operand_bytes_version") == 1 else "off",
           "AONOHAKO_DURANTE_RUNTIME_DETERMINISM": dataset.get("runtime_determinism") or "off",
           "AONOHAKO_DURANTE_RESOURCE_POLICY": json.dumps(resources)}
    descriptor_env = {"PATH": os.defpath, "AONOHAKO_DEPLOYMENT_TARGET": "dev", "AONOHAKO_INBOUND_AUTH": "none",
                      "AONOHAKO_EXECUTION_TRANSPORT": "embedded", "AONOHAKO_SANDBOX_BACKEND": "helper",
                      "AONOHAKO_DURANTE_RUNTIME_IMAGE": args.runtime_image,
                      "AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT": args.runtime_image, **env}
    # Ordinary CPP20 programs share a launcher; stdin and the binary are outside its runtime-options identity.
    request = args.output / "runtime-options-request.json"
    write_json(request, {"lang": "CPP20", "limits": dataset["native_limits"], "work_measurement": True,
                         "work_measurement_limits": hard})
    description = subprocess.run([args.reference, "describe-runtime-options", "--request", request],
                                 env=descriptor_env, capture_output=True, timeout=30)
    require(description.returncode == 0, description.stderr.decode(errors="replace")[:1500])
    role, = json.loads(description.stdout)["runtime_options"]
    require(role["role"] == "contestant" and role["source_language"] == "CPP20", "unexpected runtime-options role")
    model = {key:policy[key] for key in ("model_id", "model_version", "reference_id")}
    if policy.get("method"):
        model["method"] = policy["method"]
    model["coefficients"] = {key:value for key,value in policy.items() if key.endswith("_numerator") or key == "ns_denominator"}
    profile = {key:policy[key] for key in ("family", "engine_profile", "dispatch_profile", "model_id", "model_version")}
    if policy.get("cpuid_profile"):
        profile["cpuid_profile"] = policy["cpuid_profile"]
    profile.update(source_language="CPP20", runtime_image=args.runtime_image, qualification_id="local-boundary-experiment",
                   runtime_options=role["runtime_options"])
    registry = {"schema": 2, "models": [model], "profiles": [profile]}
    write_json(args.output / "registry.json", registry)
    judge = {**env, "AONOHAKO_DURANTE_JUDGE_MODE": "on", "AONOHAKO_DURANTE_JUDGE_FAMILIES": "cpp",
             "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(registry)}
    return env, judge, role["runtime_options_id"]


def execute(url, request, dest, verify):
    limits = request.get("work_measurement_limits", request["limits"])
    row = {"response_file": dest.name}
    try:
        response, _ = post(url, "/execute", request, timeout=limits["wall_time_ms"]/1000 + 30)
        write_json(dest, response)
        row.update(status=response["status"], response_sha256=digest(dest.read_bytes()))
        verify(response)
    except (OSError, http.client.HTTPException, RuntimeError, KeyError, ValueError, TypeError) as error:
        row["error"] = str(error)
        if isinstance(error, urllib.error.HTTPError):
            row["error"] += ": " + error.read(1024).decode(errors="replace")
    return row


def checkpoint(out, summary):
    partial = out / "summary.tmp"
    write_json(partial, summary)
    partial.replace(out / "summary.json")


def experiment(args, summary):
    manifest, policy, dataset = validate_inputs(args, summary)
    samples = defaultdict(list)
    for sample in sorted(dataset["samples"], key=lambda s:s["repetition"]):
        samples[sample["workload_id"], sample["scale"]].append(sample)
    hard = dataset["metered_limits"]
    native_limits = dataset["native_limits"]
    native_env, judge_env, options_id = judge_config(args, dataset, policy)
    installation = dataset["samples"][0]["metered"]["work_measurement"]["installation"]
    # Evidence stays owner-only. The server must create executable workspaces accessible to its sandbox UID.
    with server(args, 18763, False, operator_env=native_env, umask=0o022) as native, \
            server(args, 18764, False, operator_env=judge_env, umask=0o022) as judge:
        for workload in manifest["workloads"]:
            ident = workload["id"]
            try:
                binaries = compile_cpp(native, workload["source"], timeout=120, max_bytes=16 << 20)
                binary_sha = digest(base64.b64decode(binaries[0]["data_b64"], validate=True))
                first = samples[ident, workload["cases"][0]["scale"]][0]
                require(first["binary_sha256"] == binary_sha, "recompiled artifact differs from collection")
            except (OSError, http.client.HTTPException, RuntimeError, KeyError, ValueError) as error:
                summary["exclusions"].append({"workload_id": ident, "error": str(error)})
                checkpoint(args.output, summary)
                continue
            for case in workload["cases"]:
                scale = case["scale"]
                group = samples[ident, scale]
                ns = [s[phase]["cpu_time_ns"] for s in group for phase in ("native_before", "native_after")]
                max_nominal = min(hard["cpu_time_ms"] - 1, hard["wall_time_ms"] - 1, native_limits["wall_time_ms"])
                targets, excluded = plan_limits(ns, [s["metered"]["work_measurement"] for s in group], policy, max_nominal)
                for item in excluded:
                    summary["exclusions"].append({"workload_id": ident, "scale": scale, "native_controls_ns": ns, **item})
                if excluded:
                    checkpoint(args.output, summary)
                for target in targets:
                    label, nominal = target["boundary"], target["nominal_ms"]
                    limits = {**native_limits, "time_ms": nominal}
                    plain = {"lang": "CPP20", "binaries": binaries, "stdin": case["input"], "expected_stdout": case["expected_stdout"],
                             "limits": limits, "emit_logs": False}
                    measured = {**plain, "work_measurement": True, "work_measurement_judge": True, "work_measurement_time": True,
                                "work_measurement_limits": hard}
                    row = {"workload_id": ident, "scale": scale, **target,
                           "native_controls_ns": ns, "binary_sha256": binary_sha, "native": []}
                    summary["cases"].append(row)
                    prefix = f"{ident}-{scale}-{label}"
                    row["durante"] = execute(judge, measured, args.output / (prefix + "-durante.json"),
                                             lambda r: check_clock(r, measured, policy, options_id, dataset, installation))
                    for i in range(1, 4):
                        row["native"].append(execute(native, plain, args.output / f"{prefix}-native-{i}.json",
                                                     lambda r: check_native(r, nominal, limits)))
                    if any("error" in run for run in [row["durante"], *row["native"]]):
                        row["comparison"] = "invalid"
                    else:
                        row["comparison"] = classify(row["durante"]["status"], [r["status"] for r in row["native"]])
                    outcome = row["comparison"]
                    if outcome == "unanimous_opposition":
                        outcome = "durante_tle_native_ac" if row["durante"]["status"] == TLE else "durante_ac_native_tle"
                    summary["counts"][outcome] += 1
                    checkpoint(args.output, summary)
    results = [r["comparison"] for r in summary["cases"]]
    if summary["exclusions"] or "invalid" in results:
        return 1
    return 0 if all(r == "unanimous_agreement" for r in results) else 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("manifest", "frozen-policy", "dataset", "server", "reference", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        args.output.mkdir(mode=0o700)
    except OSError as error:
        print(f"create fresh output directory: {error}", file=sys.stderr)
        return 1
    summary = {
        "schema": 1, "exit_code": 1, "runtime_image": args.runtime_image,
        "inputs": {}, "cases": [], "exclusions": [],
        "counts": dict.fromkeys(("unanimous_agreement", "durante_tle_native_ac", "durante_ac_native_tle", "mixed", "invalid"), 0),
    }
    checkpoint(args.output, summary)
    try:
        require(os.geteuid() == 0 and set(os.listdir("/sys/class/net")) == {"lo"},
                "run in a Linux network-none validation container as root")
        args.reference, args.server = args.reference.resolve(strict=True), args.server.resolve(strict=True)
        for key in ("reference", "server"):
            summary["inputs"][key + "_sha256"] = digest(getattr(args, key).read_bytes())
        summary["exit_code"] = experiment(args, summary)
    except (OSError, RuntimeError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        summary["error"] = str(error)
        print(error, file=sys.stderr)
    checkpoint(args.output, summary)
    return summary["exit_code"]


if __name__ == "__main__":
    raise SystemExit(main())
