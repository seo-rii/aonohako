"""Post-hoc diagnostic of one reference-fitted work coefficient per problem.

Run python3 -B scalar_bounds.py --output scalar_bounds.json in this directory.
The fit uses only the calibration references. The primary and repeat guests
are scored separately on the completed fast and wrong held-out solvers.
"""
import argparse
from fractions import Fraction
import json
from pathlib import Path
from statistics import mean, median

import verify


def summarize_samples(rows):
    native, work = [], []
    for row in rows:
        receipt = row["receipt"]
        verify.require(receipt["exit_code"] == 0, "a censored run cannot supply a terminal sample")
        if row["kind"] == "native":
            verify.require(receipt["status"] in (verify.AC, verify.WA), "expected a completed native run")
            native.append(receipt["cpu_time_ns"])
        else:
            report = receipt["work_measurement"]
            verify.require(report["complete"], "missing terminal counters")
            work.append(report["instructions"] + report["rep_bytes"])
    return {"native_cpu_ns": [min(native), median(map(Fraction, native)), max(native)],
            "work_units": [min(work), median(map(Fraction, work)), max(work)]}


def ratio_interval(sample):
    cpu_min, _, cpu_max = sample["native_cpu_ns"]
    work_min, _, work_max = sample["work_units"]
    return Fraction(cpu_min, work_max), Fraction(cpu_max, work_min)


def lower_bound(solvers):
    intervals = {role: ratio_interval(sample) for role, sample in solvers.items()}
    high = max(intervals, key=lambda role: intervals[role][0])
    low = min(intervals, key=lambda role: intervals[role][1])
    top, bottom = intervals[high][0], intervals[low][1]
    # To fit both witnesses within relative error e the same coefficient must
    # be at least (1-e)*top and at most (1+e)*bottom.
    floor = max(Fraction(0), (top - bottom) / (top + bottom))
    return dict(minimax_relative_error_floor_pct=percentage(floor),
                highest_ratio_lower_endpoint_solver=high, lowest_ratio_upper_endpoint_solver=low)


def percentage(value):
    return round(float(100 * value), 4)


def errors(values):
    absolute = list(map(abs, values))
    return dict(groups=len(values), mean_absolute_pct=percentage(mean(absolute)),
                median_absolute_pct=percentage(median(absolute)), median_signed_pct=percentage(median(values)),
                max_absolute_pct=percentage(max(absolute)))


def published_sample(sample):
    result = {k: [int(v) if v == int(v) else float(v) for v in series] for k, series in sample.items()}
    result["ns_per_work_interval"] = [round(float(v), 9) for v in ratio_interval(sample)]
    return result


def analyze(root):
    evidence = root / "evidence"
    registration = json.loads((root / "registration.json").read_bytes())
    phases = ("primary", "repeat")
    validated = verify.verify(registration, evidence / "calibrate", [evidence / p for p in phases],
                              infrastructure=json.loads((evidence / "infrastructure.json").read_bytes()))
    verify.require(validated == json.loads((root / "results.json").read_bytes()), "published pilot summary differs")
    frozen = json.loads(verify.evidence(evidence / "calibrate", "frozen.json"))
    policy = frozen["policy"]
    global_coefficient = Fraction(policy["instruction_ns_numerator"], policy["ns_denominator"])
    all_rows = {p: verify.read_guest(evidence / p, registration)[1].values() for p in ("calibrate", *phases)}
    statistics = {p: {"shared": [], "per_problem": []} for p in phases}
    held_out_samples = {phase: {} for phase in phases}
    problems = {}
    for problem in verify.corpus.PROBLEMS:
        reference = summarize_samples(r for r in all_rows["calibrate"] if r["problem"] == problem)
        local = reference["native_cpu_ns"][1] / reference["work_units"][1]
        guests = {}
        for phase in phases:
            held_out = {}
            problem_errors = {}
            for role in ("fast", "wrong"):
                sample = summarize_samples(r for r in all_rows[phase] if
                    (r["problem"], r["role"], r["boundary"]) == (problem, role, "contest"))
                held_out[role] = sample
                held_out_samples[phase][f"{problem}/{role}"] = sample
                signed = {}
                for model, coefficient in (("shared", global_coefficient), ("per_problem", local)):
                    error = coefficient * sample["work_units"][1] / sample["native_cpu_ns"][1] - 1
                    statistics[phase][model].append(error)
                    signed[model] = percentage(error)
                problem_errors[role] = {**published_sample(sample), "signed_error_pct": signed}
            guests[phase] = dict(held_out=problem_errors, within_guest_bound=lower_bound(held_out),
                                 including_calibration_reference_bound=lower_bound({"reference": reference, **held_out}))
        problems[problem] = dict(reference=published_sample(reference),
                                 coefficient_ns_per_work={"numerator": local.numerator, "denominator": local.denominator}, guests=guests)
    summaries = {phase: {model: errors(values) for model, values in statistics[phase].items()} for phase in phases}
    summaries["both_guests"] = {model: errors(statistics["primary"][model] + statistics["repeat"][model])
                                for model in ("shared", "per_problem")}
    return dict(schema=1, purpose="Post-hoc scalar diagnostic from calibration references and completed held-out submissions.",
                sample_order=["min", "median", "max"], work_unit="one instruction or one MOVS/STOS byte",
                signed_error="100 * (coefficient * median work / median native CPU ns - 1)",
                lower_bound=("Each solver admits [min CPU / max work, max CPU / min work]. With H the largest lower "
                             "endpoint and L the smallest upper endpoint, max(0, (H-L)/(H+L)) bounds the best possible "
                             "maximum relative error even using the most favorable values in those observed intervals."),
                frozen_sha256=validated["frozen_sha256"], held_out_contest_errors=summaries,
                shared_coefficient_bounds={p: lower_bound(held_out_samples[p]) for p in phases}, problems=problems)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--root", type=Path, default=Path(__file__).resolve().parent)
    cli.add_argument("--output", type=Path)
    args = cli.parse_args()
    output = json.dumps(analyze(args.root), indent=2, allow_nan=False) + "\n"
    if args.output:
        args.output.write_text(output)
    else:
        print(output, end="")
