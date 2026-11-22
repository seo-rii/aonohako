#!/usr/bin/env python3
"""Replay the v11 CPP20 accuracy comparison using the frozen protocol and raw CPU/counter samples."""

import collections
from fractions import Fraction
import gzip
import hashlib
import itertools
import json
import math
from statistics import median


MILLION = 1_000_000
FEATURES = ("instructions", "rep_bytes", "movs_bytes", "stos_bytes", "latency_cycles", "memory_bytes")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load(path):
    raw = path.read_bytes()
    if raw.startswith(b"\x1f\x8b"):
        raw = gzip.decompress(raw)
    return json.loads(raw), hashlib.sha256(raw).hexdigest()


def center(values):
    return median([Fraction(v) for v in values])


def ppm(ratio):
    return round(ratio * MILLION)


def integer(value, low=0):
    return type(value) is int and low <= value < 2**64


def dot(left, right):
    return sum(x * y for x, y in zip(left, right, strict=True))


def solve(matrix, rhs):
    """Exact elimination on a principal Gram submatrix. Return None if singular."""
    rows = [list(entries) + [b] for entries, b in zip(matrix, rhs, strict=True)]
    for i in range(len(rows)):
        pivot = next((k for k in range(i, len(rows)) if rows[k][i]), None)
        if pivot is None:
            return None
        rows[i], rows[pivot] = rows[pivot], rows[i]
        rows[i] = [x / rows[i][i] for x in rows[i]]
        for j, row in enumerate(rows):
            if j != i:
                rows[j] = [x - row[i] * y for x, y in zip(row, rows[i], strict=True)]
    return [row[-1] for row in rows]


def fit(cases, columns):
    """NNLS on at most three columns. Singular faces have an optimum on a smaller face."""
    x = [[case["center"][col] / case["target"] for col in columns] for case in cases]
    vectors = list(zip(*x, strict=True))
    gram = [[dot(a, b) for b in vectors] for a in vectors]
    rhs = [sum(v) for v in vectors]
    best = [Fraction(0)] * len(columns)
    best_loss = Fraction(len(x))
    rank = 0
    for size in range(1, len(columns) + 1):
        for subset in itertools.combinations(range(len(columns)), size):
            solution = solve([[gram[i][j] for j in subset] for i in subset], [rhs[i] for i in subset])
            if solution is None:
                continue
            rank = size
            if min(solution) <= 0:
                continue
            weights = [Fraction(0)] * len(columns)
            for i, value in zip(subset, solution, strict=True):
                weights[i] = value
            loss = sum((dot(row, weights) - 1)**2 for row in x)
            # Size and feature-order tie breaks follow iteration order.
            if loss < best_loss:
                best, best_loss = weights, loss
    return dict(zip(columns, best, strict=True)), rank


def verify_inputs(data, protocol, plan_hash, protocol_hash):
    require(data["protocol_sha256"] == protocol_hash and protocol["analysis_plan_sha256"] == plan_hash,
            "dataset or analysis plan differs from the collection protocol")
    scheduled = {i: item for i, item in enumerate(protocol["schedule"])}
    actual = {}
    require(len(data["cases"]) == len(protocol["cases"]), "missing case")
    for raw, fixture in zip(data["cases"], protocol["cases"], strict=True):
        require(all(raw.get(k) == v for k, v in fixture.items()), f"{fixture['case_id']}: different fixture")
        for kind in ("warmup", "native", "metered"):
            for row in ([raw[kind]] if kind == "warmup" else raw[kind]):
                seq = row["sequence"]
                require(integer(seq) and seq not in actual and row["exit_code"] == 0 and
                        row["stdout_sha256"] == fixture["stdout_sha256"], f"{fixture['case_id']}: invalid trial")
                actual[seq] = {"case_id": fixture["case_id"], "kind": kind, "rep": row["rep"]}
    require(scheduled == actual, "missing, duplicate, or unscheduled trial")


def checked_case(raw, plan, program_splits):
    name = raw["program"]
    case_id = raw["case_id"]
    require(isinstance(case_id, str) and case_id and name in program_splits, "unknown case or program")
    require(raw["split"] == ("train" if program_splits[name] == "train" else "heldout"),
            f"{case_id}: unexpected training designation")
    native = []
    for trial in raw["native"]:
        require(all(integer(trial[k]) for k in ("user_us", "sys_us", "cpu_us")) and
                trial["cpu_us"] > 0 and trial["cpu_us"] == trial["user_us"] + trial["sys_us"],
                f"{case_id}: invalid native wait4 CPU time")
        native.append(trial["cpu_us"] * 1000)
    rows = []
    for trial in raw["metered"]:
        require(all(integer(trial[k]) for k in ("instructions", "rep_bytes", "movs_bytes", "stos_bytes",
                                                 "cycles_x16", "memory_operand_bytes")) and
                trial["instructions"] > 0 and trial["cycles_x16"] >= 4 * trial["instructions"] and
                trial["rep_bytes"] == trial["movs_bytes"] + trial["stos_bytes"], f"{case_id}: invalid counters")
        rows.append({"instructions": trial["instructions"], "rep_bytes": trial["rep_bytes"],
                     "movs_bytes": trial["movs_bytes"], "stos_bytes": trial["stos_bytes"],
                     "latency_cycles": Fraction(trial["cycles_x16"], 16), "memory_bytes": trial["memory_operand_bytes"]})
    for part, count in (("native", plan["native_repetitions"]), ("metered", plan["metered_repetitions"])):
        reps = [r["rep"] for r in raw[part]]
        require(len(reps) == count and all(integer(rep) for rep in reps) and len(set(reps)) == count,
                f"{case_id}: incomplete or duplicate {part} repetitions")
    return {"id": case_id, "program": name, "split": program_splits[name], "native": native,
            "target": center(native), "rows": rows, "center": {k: center(r[k] for r in rows) for k in FEATURES}}


def boundary(native, predictions, budget_ms):
    limit = budget_ms * MILLION
    native_tle = sum(value > limit for value in native)
    model_tle = sum(value > limit for value in predictions)
    if native_tle not in (0, len(native)):
        result = "mixed_native"
    elif native_tle == 0 and model_tle > 0:
        result = "false_tle"
    elif native_tle == len(native) and model_tle < len(predictions):
        result = "false_pass"
    else:
        result = "agreement"
    return {"limit_ms": budget_ms, "native_tle_reps": native_tle, "model_tle_reps": model_tle,
            "mixed_predictions": 0 < model_tle < len(predictions), "classification": result}


def score(case, weights):
    target = case["target"]
    preds = [sum(row[k] * w for k, w in weights.items()) for row in case["rows"]]
    low = max(1, math.floor(target * 4 / (5 * MILLION)))
    high = math.ceil(target * 6 / (5 * MILLION))
    result = {"prediction_ns": [round(p) for p in preds]}
    for name, ref in (("median", target), ("minimum", min(case["native"]))):
        errors = [pred / ref - 1 for pred in preds]
        result[name] = {"signed_error_ppm": [ppm(e) for e in errors],
                        "within_20pct": all(abs(e) <= Fraction(1, 5) for e in errors)}
    result["boundaries"] = ([boundary(case["native"], preds, b) for b in (low, high)]
                            if low * MILLION < target < high * MILLION else [])
    return result


def summarize(items):
    boundaries = [b for x in items for b in x["boundaries"]]
    summary = {"cases": len(items), "boundaries": len(boundaries),
               "excluded_boundaries": len(items) * 2 - len(boundaries)}
    for outcome in ("agreement", "false_tle", "false_pass", "mixed_native"):
        summary[outcome] = sum(b["classification"] == outcome for b in boundaries)
    summary["mixed_predictions"] = sum(b["mixed_predictions"] for b in boundaries)
    for name in ("median", "minimum"):
        signed = [e for x in items for e in x[name]["signed_error_ppm"]]
        summary[name] = {"within_20pct": sum(x[name]["within_20pct"] for x in items),
                         "worst_signed_error_ppm": max(signed, key=abs),
                         "median_case_max_abs_error_ppm": round(center(max(map(abs, x[name]["signed_error_ppm"])) for x in items))}
    return summary


def evaluate(dataset, plan):
    counts = {"train": (plan["training_programs"], plan["training_cases_per_program"]),
              "original": (plan["original_holdouts"], plan["original_cases_per_program"]),
              "additional": (plan["additional_holdouts"], plan["additional_cases_per_program"])}
    splits = {p: split for split, (programs, _) in counts.items() for p in programs}
    expected = {p: count for programs, count in counts.values() for p in programs}
    cases = [checked_case(raw, plan, splits) for raw in dataset["cases"]]
    require(len(set(c["id"] for c in cases)) == len(cases) and
            collections.Counter(c["program"] for c in cases) == expected, "different corpus or duplicate case IDs")
    train = [case for case in cases if case["split"] == "train"]
    support = {k: (min(c["center"][k] for c in train), max(c["center"][k] for c in train)) for k in FEATURES}
    sections = {"cpp20_train_only": {}, "frozen_rust_transfer": {}}
    for name, cols in (plan["columns"] | plan["cpp20_only_columns"]).items():
        weights, rank = fit(train, cols)
        sections["cpp20_train_only"][name] = {"rank": rank, "rank_deficient": rank < len(cols),
                                              "weights_ns": {k: str(w) for k, w in weights.items()}}
    for name, weights in plan["secondary_weights"].items():
        require(set(weights) == set(plan["columns"][name]) and all(Fraction(w) >= 0 for w in weights.values()),
                "invalid frozen weights")
        sections["frozen_rust_transfer"][name] = {"weights_ns": weights}
    inputs = {}
    for c in cases:
        native = c["native"]
        spread = Fraction(max(native) - min(native), c["target"])
        fields = {key: {"min": str(min(row[key] for row in c["rows"])), "median": str(c["center"][key]),
                        "max": str(max(row[key] for row in c["rows"]))} for key in FEATURES}
        inputs[c["id"]] = {"program": c["program"], "split": c["split"], "native_min_ns": min(native),
                           "native_median_ns": int(c["target"]), "native_max_ns": max(native),
                           "native_spread_ppm": ppm(spread),
                           "duration_floor_pass": min(native) >= plan["native"]["min_duration_ns"],
                           "spread_pass": spread <= Fraction(plan["native"]["max_spread_ppm"], MILLION),
                           "counters": fields, "outside_cpp20_training_range":
                           [k for k in FEATURES if not support[k][0] <= c["center"][k] <= support[k][1]]}
    for models in sections.values():
        for model in models.values():
            weights = {k: Fraction(v) for k, v in model["weights_ns"].items()}
            scores = {case["id"]: score(case, weights) for case in cases}
            model["cases"] = scores
            by_split = {s: [scores[c["id"]] for c in cases if c["split"] == s] for s in counts}
            by_split["held_out"] = by_split["original"] + by_split["additional"]
            model["by_split"] = {s: summarize(items) for s, items in by_split.items()}
            model["by_program"] = {p: summarize([scores[c["id"]] for c in cases if c["program"] == p]) for p in expected}
    return {"schema": 1, "authority": "diagnostic_only", "cpp20_training_ranges":
            {k: [str(a), str(b)] for k, (a, b) in support.items()}, "cases": inputs, "models": sections}
