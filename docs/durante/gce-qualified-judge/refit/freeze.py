"""Freeze the one-coefficient refit and both live-judge comparison registries."""
import argparse
from collections import defaultdict
from copy import deepcopy
from fractions import Fraction
import importlib.util
import json
from pathlib import Path
from statistics import median
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
spec = importlib.util.spec_from_file_location("original_pilot_verifier", HERE.parent / "verify.py")
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)

FEATURE_COMMIT = "357994c1f4cd4fc650848624bc3e355f7bfa06d8"
PILOT_COMMIT = "0f11b8c17ff7e1b11072649a1145853f338e6253"
FEATURE_RESULT = "ff4aa91067982967c435c17838895916a1b02174c31b6af1590fdaff948453cb"
ORIGINAL_RESULT = "19aa5e19d87c6cf5fbf2da859046424b07f64492cd75d85af1620aa4939484b0"
U64_MAX = 2**64 - 1


def wire_coefficient(exact):
    if max(exact.numerator, exact.denominator) <= U64_MAX:
        return exact
    # Round once, to the nearest billionth of a nanosecond/work (ties to even).
    value = Fraction(round(exact * 10**9), 10**9)
    pilot.require(0 < value and max(value.numerator, value.denominator) <= U64_MAX and
                  abs(value - exact) <= Fraction(1, 2*10**9), "refit cannot be represented in the live model")
    return value


def ratio(value):
    return {"numerator": value.numerator, "denominator": value.denominator}


def derive(cases, trials):
    counts, cpu = defaultdict(list), defaultdict(list)
    calibration = {c["case_id"] for c in cases if c["split"] == "calibration"}
    validation = {c["case_id"] for c in cases if c["split"] == "held_out"}
    pilot.require(len(calibration) == 3 and len(validation) == 12, "registered feature panel differs")
    for r in trials:
        case_id = r["case_id"]
        if r["kind"] == "native":
            cpu[case_id].append(r["receipt"]["cpu_time_ns"])
        elif case_id in calibration:
            m = r["receipt"]["work_measurement"]
            counts[case_id].append(m["instructions"] + m["rep_bytes"])
    pilot.require(all(len(cpu[k]) == 5 for k in calibration | validation) and all(len(counts[k]) == 2 for k in calibration),
                  "missing native or reference counter measurements")
    # Single through-origin absolute-time least squares on three references.
    work = {k: median(map(Fraction, counts[k])) for k in calibration}
    slope = sum(w * median(cpu[k]) for k,w in work.items()) / sum(w*w for w in work.values())
    deadlines = {}
    for k in sorted(validation):
        ns = median(cpu[k])
        deadlines[k] = dict(native_median_ns=ns, low_ms=max(1, 4*ns//5_000_000), high_ms=-(-6*ns//5_000_000))
    return slope, deadlines


def extend_scope(clock, cases, deadlines):
    binding = clock["model_binding"]
    entries = {(r["problem_id"], r["stdin_sha256"], r["time_ms"]) for r in clock["judge_cases"]["cases"]}
    for case in cases:
        if case["case_id"] in deadlines:
            entries.update((case["problem"], case["stdin_sha256"], deadlines[case["case_id"]][b + "_ms"])
                           for b in ("low", "high"))
    clock["judge_cases"] = {"schema":1, "cases":[dict(problem_id=p, source_language="CPP20", stdin_sha256=stdin,
                                                       time_ms=ms, model_binding=binding) for p,stdin,ms in sorted(entries)]}


def build(feature_root):
    original = HERE.parent
    result_bytes = (feature_root / "results.json").read_bytes()
    old_bytes = (original / "results.json").read_bytes()
    pilot.require(pilot.sha(result_bytes) == FEATURE_RESULT and pilot.sha(old_bytes) == ORIGINAL_RESULT, "wrong published results")
    published, old = json.loads(result_bytes), json.loads(old_bytes)
    manifest = (feature_root / "case_manifest.json").read_bytes()
    trials = pilot.evidence(feature_root / "evidence","trials.jsonl")
    source = (original / "evidence/calibrate/frozen.json").read_bytes()
    pilot.require(pilot.sha(manifest) == published["case_manifest_sha256"] and pilot.sha(trials) == published["trials_sha256"] and
                  pilot.sha(source) == old["frozen_sha256"], "source manifest, raw measurements or baseline clock changed")
    cases = json.loads(manifest)["cases"]
    exact, deadlines = derive(cases, map(json.loads, trials.splitlines()))
    pilot.require(ratio(exact) == published["models"]["absolute_work"]["nanoseconds_per_unit"]["work"],
                  "three-reference-only refit differs from the registered result")
    weight = wire_coefficient(exact)
    baseline = json.loads(source)
    candidate = deepcopy(baseline)
    new_identity = dict(model_id="pinned-judge-absolute-work", reference_id="three-reference-absolute-time-ls")
    coeffs = dict(instruction_ns_numerator=weight.numerator, rep_byte_ns_numerator=weight.numerator, ns_denominator=weight.denominator)
    candidate["policy"].update(**new_identity, **coeffs)
    model, = candidate["time_models"]["models"]
    profile, = candidate["time_models"]["profiles"]
    model.update(**new_identity, coefficients=coeffs)
    profile["model_id"] = new_identity["model_id"]
    candidate["model_binding"] = pilot.model_binding(candidate["policy"], candidate["runtime_options_id"])
    candidate["calibration_boot_sha256"] = json.loads(pilot.evidence(feature_root / "evidence", "platform.json"))["boot_sha256"]
    candidate["calibration_trials_sha256"] = published["trials_sha256"]
    for clock in (baseline, candidate):
        extend_scope(clock, cases, deadlines)
    pilot.require(baseline["model_binding"] != candidate["model_binding"], "indistinguishable judge models")
    return dict(schema=1, pilot_commit=PILOT_COMMIT, feature_commit=FEATURE_COMMIT,
                pilot_results_sha256=ORIGINAL_RESULT, pilot_frozen_sha256=old["frozen_sha256"],
                feature_results_sha256=FEATURE_RESULT, feature_trials_sha256=published["trials_sha256"],
                case_manifest_sha256=published["case_manifest_sha256"], exact_ns_per_work=ratio(exact),
                wire_ns_per_work=ratio(weight), absolute_quantization_error_ns_per_work=ratio(abs(weight-exact)),
                deadlines=deadlines, baseline=baseline, candidate=candidate)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--feature-study", required=True, type=Path)
    cli.add_argument("--output", type=Path)
    args = cli.parse_args()
    text = json.dumps(build(args.feature_study), indent=2, sort_keys=True, allow_nan=False) + "\n"
    if args.output:
        args.output.write_text(text)
    else:
        print(text, end="")
