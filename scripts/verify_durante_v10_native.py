#!/usr/bin/env python3
"""Compare the frozen v10 trial clock with bracketed native CPU times and replay language fits."""
import argparse
from collections import defaultdict
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


def summarize_language(directory, language, protocol, history, reference):
    source = directory / "dataset.json.gz"
    failure = directory / "failure.json"
    if failure.is_file():
        record = json.loads(failure.read_bytes())
        require(record["phase"] == "collect" and type(record["exit_code"]) is int and record["exit_code"] > 0 and
                isinstance(record["reason"], str) and record["reason"].strip() and
                not (directory / "evaluation.json").exists(), "invalid incomplete collection record")
        result = {"status": "collection_failed", "failure": record}
        if source.exists():
            raw = gzip.decompress(source.read_bytes())
            dataset = json.loads(raw)
            require(dataset["source_language"] == language and dataset["complete"] is False and dataset.get("failures"),
                    "failure receipt does not match incomplete dataset")
            result.update(dataset_sha256=digest(raw), recorded_triplets=len(dataset.get("samples") or []))
        return result
    raw = gzip.decompress(source.read_bytes())
    dataset = json.loads(raw)
    previous = history["arms"]["candidate"]
    require(dataset["schema"] == 1 and dataset["source_language"] == language and
            dataset["corpus_id"] == protocol["languages"][language] and dataset["complete"] is True and
            dataset["smoke"] is False and not dataset.get("failures") and dataset["repetitions"] == protocol["repetitions"] and
            dataset["runner_sha256"] == previous["reference_sha256"] and dataset["dispatch_profile"] == "default" and
            dataset["cpuid_profile"] == "x86-64-v2" and not dataset.get("latency_bound_version") and
            re.fullmatch(r"sha256:[0-9a-f]{64}", dataset["runtime_image"]), "incorrect v10 dataset identity")
    require(dataset["environment"]["runtime_family"] == FAMILY[language] and
            dataset["environment"]["source_language"] == language, "incorrect runtime family")

    receipt = json.loads((directory / "evaluation.json").read_bytes())
    ev = receipt["evaluation"]
    runtime_id = f"{language}@{dataset['runtime_image']}"
    gates = protocol["thresholds"]
    options = ev["options"]
    require(receipt["dataset_sha256"] == digest(raw) and ev["schema"] == 1 and
            ev["authority"] == "diagnostic_only" and ev["runtime_id"] == runtime_id and
            options.get("method", WEIGHTED) == WEIGHTED and options["min_repetitions"] == dataset["repetitions"] and
            all(options[k] == v for k, v in gates.items()), "incorrect fit input, options or binding")
    if reference:
        replay_fit(reference, raw, receipt, ev)

    split = {"integer": "train", "branch": "validation", "sort": "validation"}
    split.update({"rep_copy": "train", "rep_fill": "validation"} if language == "CPP20"
                 else {"copy": "train", "fill": "validation"})
    groups = defaultdict(dict)
    for sample in dataset["samples"]:
        name, scale, repetition = sample["workload_id"], sample["scale"], sample["repetition"]
        require(sample["split"] == split[name] and repetition not in groups[name, scale], "duplicate or relabelled trial")
        groups[name, scale][repetition] = trial(sample, dataset, previous)
    expected = {(name, scale) for name in split for scale in protocol["scales"]}
    fits = {(r["workload_id"], r["scale"]): r for r in ev["cases"]}
    require(set(groups) == set(fits) == expected and len(fits) == len(ev["cases"]), "incomplete case set")
    candidate = ev.get("candidate_policy")
    require(ev["passed"] == (not ev.get("failures")) and (not ev["passed"] or candidate), "invalid fit outcome")
    if candidate:
        require(candidate.get("method", WEIGHTED) == WEIGHTED and candidate["runtime_id"] == runtime_id and
                candidate["family"] == FAMILY[language] and candidate["engine_profile"] == previous["engine_profile"] and
                candidate["instruction_ns_numerator"] > 0 and candidate["rep_byte_ns_numerator"] > 0 and
                candidate["ns_denominator"] > 0, "mismatched fitted coefficients")
    cases = {}
    for (name, scale), repetitions in sorted(groups.items()):
        order = range(1, dataset["repetitions"] + 1)
        require(set(repetitions) == set(order), "incomplete repetitions")
        rows = [repetitions[n] for n in order]
        row = score_case(rows, history["protocol"]["clock"], gates)
        official = fits[name, scale]
        require(official["split"] == split[name] and all(official[k] == row[k] for k in
                ("native_median_ns", "native_spread_ppm", "instruction_spread_ppm", "rep_spread_ppm")),
                "fit native or work spread does not reconcile")
        if candidate:
            center = row["native_median_ns"]
            predictions = [estimate(candidate, counts) for _, counts in rows]
            worst = max(predictions, key=lambda p: abs(p - center))
            error = abs(worst - center)
            require((worst, error, ppm(error, center), worst < center) ==
                    (official["estimated_ns"], official["absolute_error_ns"], official["absolute_error_ppm"],
                     official["underestimated"]), "fit error does not reconcile")
        else:
            require(official["estimated_ns"] == official["absolute_error_ns"] == official["absolute_error_ppm"] == 0,
                    "unsolved fit cannot supply an estimate")
        cases[f"{name}/{scale}"] = {"split": split[name], **row}
    clock = history["protocol"]["clock"]
    weights = tuple(clock[k] for k in ("instruction_ns_numerator", "movs_byte_ns_numerator",
                                       "stos_byte_ns_numerator", "ns_denominator"))
    return {"status": "collected", "image": dataset["runtime_image"], "dataset_sha256": digest(raw),
            "counter_diagnostics": diagnose(dataset["samples"], weights),
            "evaluation_sha256": digest((directory / "evaluation.json").read_bytes()),
            "fit_passed": ev["passed"], "fit_produced_coefficients": candidate is not None, "cases": cases}


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
    return {"protocol_sha256": PROTOCOL_SHA256, "languages": languages}


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
