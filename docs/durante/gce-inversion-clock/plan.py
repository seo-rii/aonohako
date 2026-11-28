"""Freeze the inversion-clock coefficient, solver identities and prospective trial panel."""
import argparse
from collections import Counter
from fractions import Fraction
from functools import cache
import gzip
import hashlib
import json
from pathlib import Path
from statistics import median
import sys

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent / "gce-qualified-judge"
TRANSFER = HERE.parent / "gce-v13-judge-transfer"
WIDE_MS = 30_000
NEW_INPUTS = {"c": (300, 3584, 257, 2448477181), "d": (272, 7168, 32749, 927841413)}
NEW_ELFS = {
    "bottom_up": "e6fafd48f818d2ea67ccb4f80a701a28c15e317391b83c462a67f857d6336658",
    "reverse_bit": "e555df168256e052772a98afd2d91955e05196e6f039d5aadd4baf5193fedf83",
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def bracket(native_ns):
    """Whole-ms boundaries around the median of five completed native runs."""
    require(len(native_ns) == 5 and all(type(n) is int and n > 0 for n in native_ns),
            "expected five positive integer native CPU times")
    ns = sorted(native_ns)[2]
    denominator = 5_000_000
    low = 4 * ns // denominator
    high = (6 * ns + denominator - 1) // denominator
    require(0 < low < high < WIDE_MS, "native bracket outside diagnostic range")
    return {"low_ms": low, "high_ms": high}


def calibration(rows):
    """Pool the two original pilot median times and independently measured work."""
    roles = {}
    for role, status in (("fast", "Accepted"), ("wrong", "Wrong Answer")):
        observations = [r for r in rows if (r["problem"], r["role"], r["boundary"]) ==
                        ("inversions", role, "contest")]
        grouped = {}
        for kind, repetitions in (("native", [1, 2, 3]), ("judge", [1, 2])):
            selected = [r for r in observations if r["kind"] == kind]
            require(sorted(r["rep"] for r in selected) == repetitions, "missing calibration repetitions")
            values = []
            for row in selected:
                receipt = row["receipt"]
                require(row["limit_ms"] == 203 and receipt["status"] == status and receipt["exit_code"] == 0
                        and row["execs"] and all(e["cpu"] == 1 for e in row["execs"]),
                        "invalid calibration outcome or placement")
                if kind == "native":
                    require(receipt["cpu_accounting"]["selected_source"] and receipt["cpu_time_ns"] > 0
                            and not receipt.get("cpu_time_normalization") and not receipt.get("cpu_time_sampling")
                            and "work_measurement" not in receipt, "invalid native calibration time")
                    values.append(receipt["cpu_time_ns"])
                else:
                    measurement = receipt["work_measurement"]
                    rep = measurement["rep_breakdown"]
                    require(measurement["complete"] and measurement["quality"] == "uninterrupted"
                            and measurement["instructions"] > 0 and measurement["rep_bytes_enabled"]
                            and rep["movs_bytes"] + rep["stos_bytes"] == measurement["rep_bytes"],
                            "ineligible terminal calibration counters")
                    values.append(measurement["instructions"] + rep["movs_bytes"] + rep["stos_bytes"])
            middle = median(map(Fraction, values))
            require(middle.denominator == 1, "nonintegral archived median")
            grouped["native_median_ns" if kind == "native" else "work_median"] = int(middle)
        roles[role] = grouped

    native = sum(r["native_median_ns"] for r in roles.values())
    work = sum(r["work_median"] for r in roles.values())
    require((native, work) == (31933000, 187714408), "original pilot calibration changed")
    k = Fraction(native, work)
    return {"fit": "sum of native CPU medians divided by sum of work medians on original pilot primary",
            "roles": roles, "native_sum_ns": native, "work_sum": work,
            "candidate": {"numerator": k.numerator, "denominator": k.denominator}}


@cache
def outputs(parameters):
    # This oracle partitions keys by their most significant differing bit.
    # Equal-key pairs additionally determine the deliberately incorrect result.
    if str(PILOT) not in sys.path:
        sys.path.insert(0, str(PILOT))
    import corpus
    batches, n, alphabet, seed = parameters
    answer = corpus.inversions(*parameters)
    random = corpus.numbers(seed)
    ties = 0
    for _ in range(batches):
        counts = Counter(next(random) % alphabet for _ in range(n))
        ties += sum(count * (count - 1) // 2 for count in counts.values())
    return f"{answer}\n", f"{answer + ties}\n"


def build_registration():
    transfer = json.loads((TRANSFER / "registration.json").read_bytes())
    freeze_bytes = (PILOT / "refit/freeze.json").read_bytes()
    cases_bytes = (PILOT / "refit/case_manifest.json").read_bytes()
    require(digest(freeze_bytes) == transfer["plan"]["freeze_sha256"] and
            digest(cases_bytes) == transfer["plan"]["cases_sha256"], "historical boundaries or input changed")
    freeze = json.loads(freeze_bytes)
    archive = {c["case_id"]: c for c in json.loads(cases_bytes)["cases"] if c["problem"] == "inversions"}
    infra = json.loads((PILOT / "evidence/infrastructure.json").read_bytes())["primary"]
    raw = gzip.decompress((PILOT / "evidence/primary/trials.jsonl.gz").read_bytes())
    require(digest(raw) == infra["trials_sha256"], "different primary calibration archive")
    coeff = calibration([json.loads(line) for line in raw.splitlines()])
    coeff.update(boot_sha256=infra["boot_sha256"], trials_sha256=infra["trials_sha256"])

    arm = transfer["plan"]["arms"]["v13"]
    elfs = {r: transfer["payload"]["pilot/corpus/inversions-" + r] for r in ("reference", "fast", "wrong", "slow")}
    elfs.update(NEW_ELFS)
    cases = {}

    def add(case_id, split, role, parameters, limits):
        correct, wrong = outputs(parameters)
        stdin = " ".join(map(str, parameters)) + "\n"
        cases[case_id] = {"problem": "inversions", "role": role, "split": split, "stdin": stdin,
                          "expected_stdout": correct, "solver_stdout": wrong if role == "wrong" else correct,
                          "binary": "pilot/corpus/inversions-" + role, "binary_sha256": elfs[role],
                          "limits": limits}

    original = archive["inversions-original-reference"]
    parameters = tuple(map(int, original["stdin"].split()))
    bounds = freeze["candidate"]["cases"]["inversions"]
    for role in ("fast", "wrong", "slow", "reference"):
        if role == "reference":
            limits = {"low": bounds["low_ms"], "high": bounds["high_ms"], "ample": WIDE_MS}
        else:
            limits = {"contest": bounds["contest_ms"]}
            if role != "slow":
                limits["ample"] = WIDE_MS
            if role == "fast":
                limits["stress"] = bounds["stress_ms"]
        add("original-" + role, "original", role, parameters, limits)
    require(cases["original-reference"]["expected_stdout"] == original["expected_stdout"], "different original oracle")
    for seed in ("a", "b"):
        for role in ("fast", "wrong"):
            old_key = f"inversions-{seed}-{role}"
            entry, limits = archive[old_key], freeze["deadlines"][old_key]
            key = f"historical-{seed}-{role}"
            add(key, "historical", role, tuple(map(int, entry["stdin"].split())),
                {"low": limits["low_ms"], "high": limits["high_ms"], "ample": WIDE_MS})
            require(all(cases[key][f] == entry[f] for f in ("binary_sha256", "stdin", "expected_stdout", "solver_stdout")),
                    "historical solver, input or independent answer changed")
    for seed, values in NEW_INPUTS.items():
        for role in ("fast", "wrong", "bottom_up", "reverse_bit"):
            add(f"prospective-{seed}-{role}", "new", role, values, {"ample": WIDE_MS})

    return {"schema": 1, "image": arm["image"], "baseline_binding": arm["binding"],
            "calibration": coeff, "cases": cases,
            "new_brackets": "From five ample native completions on primary only, round 80% down and 120% up "
                            "to whole ms. Apply the same low/high limits to both boots and both clock arms.",
            "release_gates": [
                "Both boots must preserve original contest AC/WA/in-flight TLE and the 1 ms fast in-flight stop.",
                "All ample runs must complete with the registered status and independent expected solver output.",
                "Historical bracket opposition must strictly decrease overall. On each boot within each panel "
                "(including original references), neither false passes nor false timeouts may increase and "
                "unanimous comparable coverage must not decrease.",
                "For each prospective cell and boot on which baseline agrees with unanimous native results, "
                "candidate must also agree unanimously.",
                "Two independent CPU-1-pinned boots, live binding and CPU evidence, plus verified instance and disk cleanup.",
                "Normal-route smoke and all gates must pass before releasing an inversion-only environment for "
                "the original input, 203 ms, nominal 768 MiB and 60,000 ms wall. All diagnostic cases stay excluded.",
            ]}


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--output", type=Path, help="path for the prospective plan JSON (default: stdout)")
    cli.add_argument("--binaries", type=Path, help="verify the complete staged payload, including submitted ELF digests")
    args = cli.parse_args()
    registration = build_registration()
    if args.binaries:
        for case in registration["cases"].values():
            require(digest((args.binaries / case["binary"]).read_bytes()) == case["binary_sha256"], "wrong solver ELF")
    result = json.dumps(registration, indent=2, sort_keys=True, allow_nan=False) + "\n"
    if args.output:
        args.output.write_text(result)
    else:
        print(result, end="")
