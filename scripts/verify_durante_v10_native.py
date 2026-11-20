#!/usr/bin/env python3
"""Compare the frozen v10 trial clock with bracketed native CPU times and replay language fits."""
import argparse
import gzip
import json
from pathlib import Path
import re
import subprocess
import tempfile

from durante_native_envelope import diagnose
from verify_durante_native_boundaries import AC, check_native, digest, require

ROOT = Path(__file__).resolve().parents[1]
PROTOCOL_SHA256 = "4348aa0e0dfc254e4097442851b035772329a008860be3537f5be14f03ee331b"
FAMILY = {"CPP20": "cpp", "RUST2024": "native", "PYTHON3": "cpython", "PYPY3": "pypy"}
WEIGHTED = "weighted-work-nanoseconds-v1"


def ceil_div(n, d):
    return (n + d - 1) // d


def median_ns(values):
    values = sorted(values)
    n = len(values)
    return ceil_div(values[(n - 1) // 2] + values[n // 2], 2)


def ppm(n, d):
    if d == 0:
        return 0 if n == 0 else (1 << 64) - 1
    return min((1 << 64) - 1, ceil_div(n * 1_000_000, d))


def spread(values):
    return ppm(max(values) - min(values), median_ns(values))


def estimate(weights, work):
    ins, movs, stos = work
    numerator = ins * weights["instruction_ns_numerator"]
    if weights.get("method", WEIGHTED) == WEIGHTED:
        numerator += (movs + stos) * weights["rep_byte_ns_numerator"]
    else:
        require(weights["method"] == "weighted-rep-operations-nanoseconds-v2", "unexpected time formula")
        numerator += movs * weights["movs_byte_ns_numerator"] + stos * weights["stos_byte_ns_numerator"]
    return ceil_div(numerator, weights["ns_denominator"])


def trial(sample, dataset, previous):
    native = []
    limits = dataset["native_limits"]
    for phase in ("native_before", "native_after"):
        control = sample[phase]
        check_native(control, limits["time_ms"], limits)
        require(control["status"] == AC, "reference CPU requires an accepted native execution")
        native.append(control["cpu_time_ns"])
    run = sample["metered"]
    execution = run["work_measurement_execution"]
    work = run["work_measurement"]
    require(run["status"] == "Container Initialization Failed" and run["verdict_source"] == "durante_complete" and
            run.get("exit_code") == 0 and execution["state"] == "complete" and
            execution["authority"] == work["authority"] == "diagnostic_only" and execution["output_matches"] is True and
            execution["termination_source"] == "stdout" and execution["raw_limits"] == dataset["metered_limits"] and
            execution["nominal_limits"] == limits and "time_estimate" not in execution, "incorrect diagnostic receipt")
    require(work["complete"] is True and work["quality"] == "uninterrupted" and work["rep_bytes_enabled"] is True and
            work["installation"] == previous["installation"] and work["engine_profile"] == previous["engine_profile"] and
            work["family"] == FAMILY[dataset["source_language"]] and work["dispatch_profile"] == dataset["dispatch_profile"] and
            work.get("runtime_determinism", "") == dataset.get("runtime_determinism", "") and
            work["cpuid_profile"] == dataset["cpuid_profile"] and "latency_bound" not in work,
            "incomplete or mismatched v10 work report")
    rep = work["rep_breakdown"]
    values = [work["instructions"], rep["movs_bytes"], rep["stos_bytes"]]
    require(all(type(v) is int and v >= 0 for v in values) and values[0] > 0 and rep["version"] == 1 and
            work["rep_bytes"] == values[1] + values[2] and work["unsupported_rep_entries"] == 0 and
            work["rep_entries"] == rep["movs_entries"] + rep["stos_entries"], "invalid typed work counters")
    return native, values


def score_case(rows, clock, thresholds):
    native = [n for pair, _ in rows for n in pair]
    work = [counts for _, counts in rows]
    predictions = [estimate(clock, counts) for counts in work]
    center = median_ns(native)
    # Keep the first worst actual repetition, as in the reference fitter.
    worst = max(predictions, key=lambda p: abs(p - center))
    ins = [row[0] for row in work]
    rep = [row[1] + row[2] for row in work]
    result = {"native_min_ns": min(native), "native_median_ns": center, "native_max_ns": max(native),
              "native_spread_ppm": spread(native), "instruction_spread_ppm": spread(ins),
              "rep_spread_ppm": spread(rep), "trial_clock_ns": predictions,
              "worst_signed_error_ns": worst - center, "worst_error_ppm": ppm(abs(worst - center), center)}
    result["failed_gates"] = [gate for gate, failed in (
        ("native_duration", min(native) < thresholds["min_native_ns"]),
        ("native_spread", spread(native) > thresholds["max_native_spread_ppm"]),
        ("instruction_spread", spread(ins) > thresholds["max_work_spread_ppm"]),
        ("movs_spread", spread([row[1] for row in work]) > thresholds["max_work_spread_ppm"]),
        ("stos_spread", spread([row[2] for row in work]) > thresholds["max_work_spread_ppm"]),
        ("prediction_error", result["worst_error_ppm"] > thresholds["max_error_ppm"]),
    ) if failed]
    return result


def replay_fit(reference, raw, receipt, evaluation):
    options = evaluation["options"]
    with tempfile.TemporaryDirectory() as directory:
        data = Path(directory) / "dataset.json"
        out = Path(directory) / "fit"
        data.write_bytes(raw)
        cmd = [str(reference), "fit", "--input", str(data), "--output", str(out),
               "--model-id", options["model_id"], "--model-version", str(options["model_version"]),
               "--reference-id", options["reference_id"]]
        if options.get("method"):
            cmd.extend(["--method", options["method"]])
        proc = subprocess.run(cmd, capture_output=True, text=True)
        require(proc.returncode == (0 if evaluation["passed"] else 2) and
                json.loads((out / "evaluation.json").read_bytes()) == receipt,
                "official reference fit does not reproduce the recorded evaluation")
        require((out / "policy.json").exists() == evaluation["passed"], "unexpected policy export")


def collect_cases(dataset, language, protocol, history):
    previous = history["arms"]["candidate"]
    require(dataset["schema"] == 1 and dataset["source_language"] == language and
            dataset["corpus_id"] == protocol["languages"][language] and dataset["smoke"] is False and
            dataset["repetitions"] == protocol["repetitions"] and dataset["runner_sha256"] == previous["reference_sha256"] and
            dataset["dispatch_profile"] == "default" and dataset["cpuid_profile"] == "x86-64-v2" and
            not dataset.get("latency_bound_version") and re.fullmatch(r"sha256:[0-9a-f]{64}", dataset["runtime_image"]),
            "incorrect v10 dataset identity")
    require(dataset["environment"]["runtime_family"] == FAMILY[language] and
            dataset["environment"]["source_language"] == language, "incorrect runtime family")
    copy, fill = ("rep_copy", "rep_fill") if language == "CPP20" else ("copy", "fill")
    splits = {"integer": "train", copy: "train", "branch": "validation", "sort": "validation", fill: "validation"}
    order = [(name, scale, rep) for name in splits for scale in protocol["scales"]
             for rep in range(1, dataset["repetitions"] + 1)]
    samples = dataset["samples"] or []
    observed = [(s["workload_id"], s["scale"], s["repetition"]) for s in samples]
    require(observed == order[:len(samples)] and (not dataset["complete"] or len(samples) == len(order)),
            "missing, reordered or duplicate trials")
    rows, groups = {}, {}
    for i, sample in enumerate(samples):
        name, scale, _ = observed[i]
        require(sample["split"] == splits[name], "relabelled workload")
        # The collector appends a row before running the phases. A final failed
        # triplet remains in the archive even when native_after was never called.
        if not dataset["complete"] and i == len(samples) - 1 and sample["native_after"].get("status") != AC:
            break
        pair = trial(sample, dataset, previous)
        label = f"{name}/{scale}"
        rows.setdefault(label, []).append(pair)
        groups.setdefault(label, []).append(sample)
    complete = {k: v for k, v in rows.items() if len(v) == dataset["repetitions"]}
    clock = history["protocol"]["clock"]
    cases = {k: {"split": groups[k][0]["split"], **score_case(v, clock, protocol["thresholds"])}
             for k, v in complete.items()}
    weights = tuple(clock[k] for k in ("instruction_ns_numerator", "movs_byte_ns_numerator",
                                       "stos_byte_ns_numerator", "ns_denominator"))
    diagnostics = diagnose([s for key in complete for s in groups[key]], weights)
    return {"image": dataset["runtime_image"], "recorded_triplets": len(samples),
            "completed_triplets": sum(map(len, rows.values())), "cases": cases,
            "counter_diagnostics": diagnostics}, complete


def check_fit(raw, dataset, receipt, result, groups, protocol, history):
    ev = receipt["evaluation"]
    language = dataset["source_language"]
    runtime_id = f"{language}@{dataset['runtime_image']}"
    options = ev["options"]
    require(receipt["dataset_sha256"] == digest(raw) and ev["schema"] == 1 and
            ev["authority"] == "diagnostic_only" and ev["runtime_id"] == runtime_id and
            options.get("method", WEIGHTED) == WEIGHTED and options["min_repetitions"] == dataset["repetitions"] and
            all(options[k] == v for k, v in protocol["thresholds"].items()), "incorrect fit input, options or binding")
    fits = {f"{r['workload_id']}/{r['scale']}": r for r in ev["cases"]}
    require(set(groups) == set(fits) and len(fits) == len(ev["cases"]), "incomplete fit case set")
    candidate = ev.get("candidate_policy")
    require(ev["passed"] == (not ev.get("failures")) and (not ev["passed"] or candidate), "invalid fit outcome")
    if candidate:
        require(candidate.get("method", WEIGHTED) == WEIGHTED and candidate["runtime_id"] == runtime_id and
                candidate["family"] == FAMILY[language] and candidate["engine_profile"] == history["arms"]["candidate"]["engine_profile"] and
                candidate["instruction_ns_numerator"] > 0 and candidate["rep_byte_ns_numerator"] > 0 and
                candidate["ns_denominator"] > 0, "mismatched fitted coefficients")
    for label, official in fits.items():
        row = result["cases"][label]
        require(all(official[k] == row[k] for k in
                    ("split", "native_median_ns", "native_spread_ppm", "instruction_spread_ppm", "rep_spread_ppm")),
                "fit native or work spread does not reconcile")
        if candidate:
            center = row["native_median_ns"]
            predictions = [estimate(candidate, counts) for _, counts in groups[label]]
            worst = max(predictions, key=lambda p: abs(p - center))
            error = abs(worst - center)
            require((worst, error, ppm(error, center), worst < center) ==
                    (official["estimated_ns"], official["absolute_error_ns"], official["absolute_error_ppm"],
                     official["underestimated"]), "fit error does not reconcile")
        else:
            require(official["estimated_ns"] == official["absolute_error_ns"] == official["absolute_error_ppm"] == 0,
                    "unsolved fit cannot supply an estimate")
    return ev


def summarize_language(directory, language, protocol, history, reference):
    source = directory / "dataset.json.gz"
    failure = directory / "failure.json"
    result = {}
    if failure.is_file():
        record = json.loads(failure.read_bytes())
        require(record["phase"] == "collect" and type(record["exit_code"]) is int and record["exit_code"] > 0 and
                isinstance(record["reason"], str) and record["reason"].strip() and
                not (directory / "evaluation.json").exists(), "invalid incomplete collection record")
        result = {"status": "collection_failed", "failure": record}
        if not source.exists():
            return result
    raw = gzip.decompress(source.read_bytes())
    dataset = json.loads(raw)
    require(dataset["source_language"] == language and dataset["complete"] is (not failure.exists()) and
            bool(dataset.get("failures")) == failure.exists(), "collection status does not match source dataset")
    result["dataset_sha256"] = digest(raw)
    if failure.exists() and not dataset.get("samples"):
        return {**result, "recorded_triplets": 0, "completed_triplets": 0, "cases": {}}
    scored, groups = collect_cases(dataset, language, protocol, history)
    result.update(scored)
    if failure.exists():
        return result
    fit = (directory / "evaluation.json").read_bytes()
    receipt = json.loads(fit)
    ev = check_fit(raw, dataset, receipt, result, groups, protocol, history)
    if reference:
        replay_fit(reference, raw, receipt, ev)
    return {**result, "status": "collected", "fit_passed": ev["passed"],
            "fit_produced_coefficients": bool(ev.get("candidate_policy")), "evaluation_sha256": digest(fit)}


def check_platform(platform, languages, history):
    previous = history["arms"]["candidate"]
    request_ids, revisions = set(), set()
    by_language = {lang: [] for lang in languages}
    for deployment in platform["deployments"]:
        revision = deployment["revision"]
        image = deployment["image_digest"]
        require(revision and revision not in revisions and re.fullmatch(r"sha256:[0-9a-f]{64}", image) and
                re.fullmatch(r".+@sha256:[0-9a-f]{64}", deployment["base_image"]), "missing or duplicate revision identity")
        revisions.add(revision)
        require(deployment["region"] == "asia-northeast3" and deployment["gen"] == 2 and
                deployment["cpu"] == deployment["concurrency"] == 1 and deployment["memory_mib"] == 2048 and
                deployment["private"] is True, "unexpected deployment configuration")
        binaries = deployment["binaries"]
        require(all(binaries[k] == previous[k] for k in ("reference_sha256", "worker_sha256")) and
                binaries["client_sha256"] == previous["installation"]["client_sha256"] and
                binaries["commit"] == previous["installation"]["source_commit"], "incorrect deployed v10 binaries")
        require(all(deployment["cleanup"][k] == 404 for k in ("revision_http", "service_http", "package_http")),
                "incomplete deployment cleanup")
        for req in deployment["requests"]:
            rid = req["request_id_hash"]
            require(re.fullmatch(r"[0-9a-f]{64}", rid) and rid not in request_ids and
                    re.fullmatch(r"[0-9a-f]{64}", req["instance_id_sha256"]), "invalid or duplicate request identity")
            request_ids.add(rid)
            require(req["language"] in languages and req["status"] == 200 and type(req["triplets"]) is int and
                    0 <= req["triplets"] <= 45 and type(req["collector_exit"]) is int and req["collector_exit"] >= 0 and
                    req["complete"] is (req["collector_exit"] == 0), "inconsistent collector outcome")
            if req["complete"]:
                require(req["triplets"] == 45 and re.fullmatch(r"[0-9a-f]{64}", req["dataset_sha256"]),
                        "incomplete successful collector response")
            by_language[req["language"]].append((image, req))
    for lang, result in languages.items():
        requests = by_language[lang]
        completed = [(image, req) for image, req in requests if req["complete"]]
        require(requests and len(completed) == int(result["status"] == "collected"),
                f"{lang}: absent or duplicate successful collection")
        if result["status"] == "collected" or "dataset_sha256" in result:
            matches = [(image, req) for image, req in (completed or requests)
                       if req.get("dataset_sha256") == result["dataset_sha256"] and
                       req["triplets"] == result["recorded_triplets"] and result.get("image", image) == image]
            require(len(matches) == 1, f"{lang}: dataset does not match deployed image and collector response")


def summarize(directory, reference=None):
    raw = (directory / "protocol.json").read_bytes()
    require(digest(raw) == PROTOCOL_SHA256, "unexpected study protocol")
    protocol = json.loads(raw)
    raw_history = (ROOT / "docs/durante/cloud-run-cpp20-v10-performance.json").read_bytes()
    require(digest(raw_history) == protocol["previous_clock_receipt_sha256"], "incorrect frozen clock receipt")
    history = json.loads(raw_history)
    if reference is not None:
        reference = reference.resolve()
        require(digest(reference.read_bytes()) == protocol["reference_sha256"], "incorrect reference executable")
    languages = {lang: summarize_language(directory / lang, lang, protocol, history, reference)
                 for lang in protocol["languages"]}
    raw_platform = (directory / "platform.json").read_bytes()
    check_platform(json.loads(raw_platform), languages, history)
    return {"protocol_sha256": PROTOCOL_SHA256, "platform_sha256": digest(raw_platform), "languages": languages}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, help="directory containing protocol.json and per-language results")
    parser.add_argument("--reference", type=Path, help="also rerun the official fit with the pinned v10 reference executable")
    parser.add_argument("--verify", type=Path, help="recompute and compare a published summary")
    args = parser.parse_args()
    try:
        summary = summarize(args.directory, args.reference)
        if args.verify:
            require(summary == json.loads(args.verify.read_bytes()), "published summary differs from measurements")
            print("Verified native controls, typed counters and scores for each recorded language.")
        else:
            print(json.dumps(summary, indent=2, allow_nan=False))
    except (KeyError, OSError, RuntimeError, ValueError, TypeError, ZeroDivisionError) as error:
        parser.exit(1, f"Verification failed: {error}\n")
