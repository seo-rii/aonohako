"""Counterexamples to a nonnegative instruction/MOVS/STOS clock on native CPU ranges."""
from fractions import Fraction
from itertools import permutations
from math import floor


def cpu_envelopes(samples, weights):
    """Compare all predictions with all native controls of the same case."""
    cases = {}
    for row in samples:
        label = (row["workload_id"], row["scale"])
        case = cases.setdefault(label, {"native": [], "features": [], "predicted": []})
        for arm in ("native_before", "native_after"):
            case["native"].append(row[arm]["cpu_time_ns"])
        work = row["metered"]["work_measurement"]
        rep = work["rep_breakdown"]
        counts = (work["instructions"], rep["movs_bytes"], rep["stos_bytes"])
        case["features"].append(counts)
        numerator = sum(x * w for x, w in zip(counts, weights[:3], strict=True))
        case["predicted"].append(-(-numerator // weights[3]))
    return cases


def pair_bound(slower, faster):
    # For every pair of observed work vectors a,b, each a_i <= ratio*b_i.
    # For all nonnegative coefficients this yields w.a <= ratio*w.b.
    # Allow 1 ns for the final ceiling in the slow prediction.
    ratios = []
    for a, b in zip(zip(*slower["features"]), zip(*faster["features"]), strict=True):
        if min(b):
            ratios.append(Fraction(max(a), min(b)))
        elif max(a):
            return None
    if not ratios:
        return None
    ratio = max(ratios)
    low, high = min(slower["native"]), max(faster["native"])
    # (1-e)*low <= predicted_slow <= ratio*(1+e)*high + 1.
    bound = Fraction(low - ratio * high - 1, low + ratio * high)
    if bound <= 0:
        return None
    return bound, {
        "slow_min_native_ns": low,
        "fast_max_native_ns": high,
        "max_feature_ratio": str(ratio),
        "minimum_possible_worst_error_ppm": floor(bound * 1_000_000),
    }


def diagnose(samples, weights):
    cases = cpu_envelopes(samples, weights)
    beyond = []
    for (name, scale), case in sorted(cases.items()):
        low, high = min(case["native"]), max(case["native"])
        predicted_low, predicted_high = min(case["predicted"]), max(case["predicted"])
        if 5 * predicted_high < 4 * low or 5 * predicted_low > 6 * high:
            beyond.append({"workload": name, "scale": scale, "native_ns": [low, high],
                           "estimate_ns": [predicted_low, predicted_high],
                           "direction": "under" if predicted_high < low else "over"})
    best = None
    witness = None
    for (left, slower), (right, faster) in permutations(sorted(cases.items()), 2):
        result = pair_bound(slower, faster)
        if result is not None and (best is None or result[0] > best):
            best, details = result
            witness = {"slow_case": list(left), "fast_case": list(right), **details}
    return {"outside_native_range_with_20pct_margin": beyond, "strongest_pair": witness}
