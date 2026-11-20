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

# reference.CorpusForLanguage(language, false) at protocol.json's source commit.
# Keep these independent of future edits to the collector and its fixture files.
SOURCE_SHA256 = {
    "CPP20": {
        "integer": "2bb33dfeec1d2f82fad2075a45e5a1a8c72362e8d61438f6eb147fe9f4c1025d",
        "rep_copy": "bbe56822498df565555d46c9352af2e5d521e052771e2e1ac89998ef50564691",
        "branch": "200fa292952892480907001cf1a795984c46a3e53f402d906dadef412b0b16dc",
        "sort": "ba6464e65deeb8bde261b12d324b91a0bf6f4d456979090e41fa877c28dd3da5",
        "rep_fill": "2c6a019d195860141be505862f39c081b860130b649a257c0c0a17708d2e5513"},
    "PYTHON3": {
        "integer": "6cf08803fab507fc2bb45236cd85ba4eab804188785c739d7f16a3ab49d51b15",
        "copy": "894a8faa7b7f3e592ab2a039ae66391a0f2a213cce1b072df89e62d15a6685f4",
        "branch": "65776a4306c9a94e8ab4067520cd2065f5943125866163e721538c9d50ecefc4",
        "sort": "1f51a18f888757ade1b0007d9dfbfe23a2f2bdc281c0fa04a2580e9e7f3cbfce",
        "fill": "f836506d77ee9ffbc43924ed69a8f5096ec480a0340e9e53b4edcf0eb7a794c3"},
    "RUST2024": {
        "integer": "dcb8ad3d0654e5a531975b07146828161c9f20dfd1a96a39d503dbd10dd297b9",
        "copy": "6c5b252b1f0135c454c594891969b456db3f9e3e0d419bbb7a48a86702ad2c98",
        "branch": "468db446e268202968ba0dae4f552eb218ef65dc64f1fb775f41ed9fde7527ce",
        "sort": "e3f307e0c849aae0598a54a74f89465adfb6393158c06b9f93ca7cda0379f1f5",
        "fill": "ab2e488cda2708649d80fde97a8de3e64d54cb8f0db406bbdf5a876c7633e2da"},
}
# (stdin, expected stdout) at scales 1, 2 and 4. Each value ends in a newline.
CORPUS_CASES = {
    "cpp-reference-v1": {
        "integer": (("33554432 81985529216486895", 7648774604356046319),
                    ("67108864 81985529216486895", 5095975316794101231),
                    ("134217728 81985529216486895", 6525099800984800751)),
        "rep_copy": (("4194304 512", 1121503238200285), ("4194304 1024", 1121503280051933),
                     ("4194304 2048", 1121503497579741)),
        "branch": (("16777216 44257", 15692098880723300352), ("33554432 44257", 12937453687737049088),
                   ("67108864 44257", 7428163301764546560)),
        "sort": (("524288", 48038533464326144), ("1048576", 384307717958270976), ("2097152", 3074459544641863680)),
        "rep_fill": (("4194304 512", 1776811231089408), ("4194304 1024", 1776811281491456),
                     ("4194304 2048", 1776811482565632))},
    "runtime-reference-v1": {
        "integer": (("262144 81985529216486895", 16902750251959438831),
                    ("524288 81985529216486895", 154191601532653039),
                    ("1048576 81985529216486895", 14675978275453128175)),
        "copy": (("262144 128", 4380953020189), ("262144 256", 4380956200541), ("262144 512", 4380968524765)),
        "branch": (("262144 44257", 3415723182680130752), ("524288 44257", 6831446365360261504),
                   ("1048576 44257", 13662892730720523008)),
        "sort": (("65536", 93827139731456), ("131072", 750608527851520), ("262144", 6004833862942720)),
        "fill": (("262144 128", 2542631291968), ("262144 256", 6940697938304), ("262144 512", 6940710605568))},
}


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
    cpu_ns = run["cpu_time_ns"]
    accounting = run["cpu_accounting"]
    require(type(cpu_ns) is int and cpu_ns > 0 and run["cpu_time_ms"] == ceil_div(cpu_ns, 1_000_000) and
            accounting["selected_source"] in ("process_clock", "wait_rusage", "cgroup") and
            (accounting["selected_source"] != "wait_rusage" or accounting["wait_target_ns"] == cpu_ns) and
            not any(k in run for k in ("cpu_time_normalization", "cpu_time_sampling", "raw_cpu_time_ns", "raw_cpu_time_ms")),
            "inconsistent diagnostic process CPU")
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
        ("rep_spread", spread(rep) > thresholds["max_work_spread_ppm"]),
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
            not dataset.get("corpus_sha256") and not dataset.get("frozen_policy_sha256") and
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
    rows, groups, binaries = {}, {}, {}
    native_cpu_ns, diagnostic_cpu_ns = 0, 0
    sources = SOURCE_SHA256["PYTHON3" if language == "PYPY3" else language]
    fixtures = CORPUS_CASES[dataset["corpus_id"]]
    for i, sample in enumerate(samples):
        name, scale, _ = observed[i]
        require(sample["split"] == splits[name], "relabelled workload")
        stdin, expected = fixtures[name][protocol["scales"].index(scale)]
        require(sample["source_sha256"] == sources[name] and sample["input_sha256"] == digest(f"{stdin}\n".encode()) and
                sample["expected_sha256"] == digest(f"{expected}\n".encode()), "incorrect fixed corpus fixture")
        binary = sample["binary_sha256"]
        require(re.fullmatch(r"[0-9a-f]{64}", binary) and binaries.setdefault(name, binary) == binary,
                "invalid or changed workload binary")
        # The collector appends a row before running the phases. A final failed
        # triplet remains in the archive even when native_after was never called.
        if not dataset["complete"] and i == len(samples) - 1 and sample["native_after"].get("status") != AC:
            break
        pair = trial(sample, dataset, previous)
        native_cpu_ns += sum(pair[0])
        diagnostic_cpu_ns += sample["metered"]["cpu_time_ns"]
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
            "native_control_cpu_sum_ns": native_cpu_ns, "diagnostic_cpu_sum_ns": diagnostic_cpu_ns,
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
    # The fitted policy uses aggregate REP bytes and its own prediction, while
    # the frozen transfer clock also gates the separate MOVS and STOS counts.
    gates = protocol["thresholds"]
    passed = bool(candidate) and all(
        row["native_min_ns"] >= gates["min_native_ns"] and
        row["native_spread_ppm"] <= gates["max_native_spread_ppm"] and
        row["instruction_spread_ppm"] <= gates["max_work_spread_ppm"] and
        row["rep_spread_ppm"] <= gates["max_work_spread_ppm"] and
        fits[label]["absolute_error_ppm"] <= gates["max_error_ppm"]
        for label, row in result["cases"].items())
    require(ev["passed"] is passed, "fit outcome does not match measured gates")
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
        return {**result, "image": dataset["runtime_image"], "recorded_triplets": 0, "completed_triplets": 0,
                "cases": {}}
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
        instances = {req["instance_id_sha256"] for req in deployment["requests"]}
        require(len(instances) == 1, "expected one instance for each private revision")
        retrieved = 0
        for req in deployment["requests"]:
            rid = req["request_id_hash"]
            require(re.fullmatch(r"[0-9a-f]{64}", rid) and rid not in request_ids and
                    re.fullmatch(r"[0-9a-f]{64}", req["instance_id_sha256"]), "invalid or duplicate request identity")
            request_ids.add(rid)
            require(req["language"] in languages and type(req["status"]) is int and 100 <= req["status"] <= 599,
                    "invalid logged request language or HTTP status")
            available = req.get("result_available", True)
            require(type(available) is bool, "invalid result availability")
            if not available:
                require(isinstance(req.get("transport_failure"), str) and req["transport_failure"].strip() and
                        not ({"complete", "collector_exit", "dataset_sha256", "triplets"} & req.keys()),
                        "unrecovered response cannot attest a collector outcome")
                continue
            if req["status"] == 202:
                receipt_id = req.get("receipt_request_id_hash", "")
                require(deployment["cpu_always_allocated"] is True and req.get("response_http") == 200 and
                        re.fullmatch(r"[0-9a-f]{64}", receipt_id) and receipt_id not in request_ids,
                        "an accepted job needs its own completed receipt request")
                request_ids.add(receipt_id)
                retrieved += 1
            else:
                require(req["status"] == 200 and "receipt_request_id_hash" not in req,
                        "unexpected synchronous collector status")
            require("transport_failure" not in req and type(req["triplets"]) is int and
                    0 <= req["triplets"] <= 45 and type(req["collector_exit"]) is int and req["collector_exit"] >= 0 and
                    req["complete"] is (req["collector_exit"] == 0), "inconsistent collector outcome")
            if req["complete"]:
                require(req["triplets"] == 45 and re.fullmatch(r"[0-9a-f]{64}", req["dataset_sha256"]),
                        "incomplete successful collector response")
            by_language[req["language"]].append((image, req))
        statuses = deployment["receipt_get_status_counts"]
        require(set(statuses) <= {"200", "202"} and all(type(n) is int and n > 0 for n in statuses.values()) and
                statuses.get("200", 0) == retrieved and sum(statuses.values()) == deployment["receipt_get_count"],
                "receipt retrieval counts do not match the completed jobs")
    for lang, result in languages.items():
        requests = by_language[lang]
        completed = [(image, req) for image, req in requests if req["complete"]]
        require(requests and len(completed) == int(result["status"] == "collected"),
                f"{lang}: absent or duplicate successful collection")
        exit_code = 0 if completed else result["failure"]["exit_code"]
        if "dataset_sha256" in result:
            matches = [req for image, req in (completed or requests) if req["collector_exit"] == exit_code and
                       req.get("dataset_sha256") == result["dataset_sha256"] and
                       req["triplets"] == result["recorded_triplets"] and image == result["image"]]
        else:
            require(not completed and all(req["triplets"] == 0 and "dataset_sha256" not in req for _, req in requests),
                    f"{lang}: missing partial archive for a recorded dataset")
            matches = [req for _, req in requests if req["collector_exit"] == exit_code]
        require(len(matches) == 1, f"{lang}: dataset or failure does not match deployed image and collector response")


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
