"""Rebuild the long contest panel and its short-input-only calibration."""
import argparse
from collections import Counter, defaultdict
from fractions import Fraction
import gzip
import json
from pathlib import Path
import runpy
import shutil
from statistics import median
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from prepare import DURANTE, REPO, encoded, sha, write_verified

SHORT = "9a2104d062554789e0c4519672cbcd1442f30956"
SHORT_PATH = "docs/durante/gce-v12-clock-transfer"
SOURCE = "experiments/contest_scale"
SEED = "aonohako-v12-clock-transfer-scale-1"


def fit_short(cases, raw, weights):
    ids = sorted(c["case_id"] for c in cases if c["group"] == "contest_holdout")
    times, work = defaultdict(list), defaultdict(list)
    for line in raw.splitlines():
        trial = json.loads(line)
        case = trial["case_id"]
        if case not in ids:
            continue
        receipt = trial["receipt"]
        if trial["kind"] == "native":
            times[case].append(receipt["cpu_time_ns"])
        if trial["kind"] == "live":
            progress = receipt["work_measurement_execution"]["clock"]["progress"]
            rep = progress["movs_bytes"] + progress["stos_bytes"]
            work[case].append(progress["instructions"] * weights["instructions"] + rep * weights["rep_bytes"])
    if len(ids) != 12 or any(len(times[k]) != 7 or len(work[k]) != 2 for k in ids):
        raise ValueError("incomplete first-guest contest panel")
    ratios = [(sum(work[k]) / len(work[k])) / median(times[k]) for k in ids]
    factor = sum(ratios) / sum(r*r for r in ratios)
    denominator = 1_000_000_000
    numerators = {k: max(1, round(v * factor * denominator)) for k, v in weights.items()}
    return {
        "kind": "live", "method": "weighted-work-nanoseconds-v1",
        "fit": "Use only the 12 first-guest short contest inputs. For case i, r_i is the mean of the two "
               "exact unrounded v11_wide_live predictions divided by its seven-run native median. "
               "Multiply both coefficients by sum(r_i)/sum(r_i^2), then round to the nearest 1e-9 "
               "with ties to even and a positive minimum numerator of one.",
        "training_cases": ids, "independent_group": "scale_holdout", "factor": str(factor),
        "ns_denominator": denominator, "numerators": numerators,
        "weights_ns": {k: str(Fraction(n, denominator)) for k, n in numerators.items()},
    }


def make(out, durante_repo, binaries):
    sources = {"aonohako": {}, "durante": {}}

    def source(project, path):
        repo, rev = (REPO, SHORT) if project == "aonohako" else (durante_repo, DURANTE)
        raw = subprocess.check_output(["git", "-C", repo, "show", rev + ":" + path])
        sources[project][path] = sha(raw)
        return raw

    original = json.loads(source("aonohako", f"{SHORT_PATH}/plan.json"))
    short_cases = json.loads(source("aonohako", f"{SHORT_PATH}/cases.json"))
    short_trials = gzip.decompress(source("aonohako", f"{SHORT_PATH}/receipts.jsonl.gz"))
    models = original["models"].copy()
    base = {k: Fraction(w) for k, w in models["v11_wide_live"]["weights_ns"].items()}
    models["short_contest_live"] = fit_short(short_cases, short_trials, base)

    registration = json.loads(source("durante", f"{SOURCE}/plan.json"))
    cases = []
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        generator = tmp / "variants.py"
        generator.write_bytes(source("durante", f"{SOURCE}/variants.py"))
        make_input = runpy.run_path(str(generator))["GENERATE"]
        for program, directory in (("wall", "contest_accuracy"), ("scrivener", "contest_warm"), ("money", "contest_accuracy")):
            cpp = tmp / f"{program}.cpp"
            cpp.write_bytes(source("durante", f"experiments/{directory}/{program}.cpp"))
            if binaries:
                elf = (binaries / program).read_bytes()
            else:
                dest = tmp / program
                subprocess.run(["g++", "-std=c++20", "-O2", "-static", "-s", "-march=x86-64-v2",
                                "-Wl,--build-id=none", cpp, "-o", dest], check=True)
                elf = dest.read_bytes()
            rows = [r for r in registration["cases"] if r["program"] == program and r["cohort"] == "prospective"]
            for row in rows:
                name = row["case_id"]
                stdin, stdout = make_input[program](row["variant"])
                binary_path = f"corpus/bin/{program}"
                stdin_path = f"corpus/stdin/{name}.txt"
                stdout_path = f"corpus/stdout/{name}.txt"
                write_verified(out, binary_path, elf, row["binary_sha256"], True)
                write_verified(out, stdin_path, stdin.encode(), row["stdin_sha256"])
                write_verified(out, stdout_path, stdout.encode(), row["stdout_sha256"])
                cases.append(dict(program=program, case_id=name, group="scale_holdout", binary_path=binary_path,
                                  stdin_path=stdin_path, stdout_path=stdout_path, binary_sha256=row["binary_sha256"],
                                  stdin_sha256=row["stdin_sha256"], stdout_sha256=row["stdout_sha256"]))

    ids = [c["case_id"] for c in cases]
    if len(set(ids)) != 6 or Counter(r["program"] for r in cases) != {"wall": 2, "money": 2, "scrivener": 2}:
        raise ValueError("expected the complete prospective long-input registration")
    (out / "cases.json").write_bytes(encoded(cases))
    rounds = dict.fromkeys((s["kind"], s["rep"]) for s in original["schedule"])
    schedule = [dict(case_id=k, kind=kind, rep=rep) for kind, rep in rounds
                for k in sorted(ids, key=lambda s: sha(f"{SEED}/{kind}/{rep}/{s}".encode()))]
    return {
        **original, "source_panel_commit": SHORT, "cases_sha256": sha(encoded(cases)),
        "groups": {"scale_holdout": 6}, "models": models, "sources_sha256": sources,
        "capture_stdout_bytes": 1048576,
        "capture_reason": "Retain the entire expected stdout for hashing; the largest Scrivener answer is "
                          "960000 bytes. The worker uses the same capture budget for all six inputs and modes.",
        "additional_comparison": "Compare short_contest_live to v11_wide_live and the unchanged primary v7 "
                                 "clock on all six longer inputs. The short-guest times prompted this second "
                                 "experiment. Its coefficients and schedule precede all second-guest results.",
        "scale_transfer_programs": {"seen_in_short_fit": ["wall", "money"], "unseen_in_short_fit": ["scrivener"]},
        "lower_bound": original["lower_bound"].replace("contest holdout pairs", "scale holdout pairs"),
        "schedule_seed": SEED, "schedule": schedule,
    }


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--durante-repo", type=Path, required=True)
    cli.add_argument("--contest-binaries", type=Path, help="exact registered ELFs; default rebuilds from source with g++")
    cli.add_argument("--output", type=Path, required=True)
    args = cli.parse_args()
    args.output.mkdir(exist_ok=True, parents=True)
    for name in ("corpus", "cases.json", "plan.json"):
        if (args.output / name).exists():
            raise FileExistsError(args.output / name)
    with tempfile.TemporaryDirectory(dir=args.output) as td:
        stage = Path(td)
        raw = encoded(make(stage, args.durante_repo, args.contest_binaries))
        if raw != (HERE / "plan.json").read_bytes() or (stage / "cases.json").read_bytes() != (HERE / "cases.json").read_bytes():
            raise ValueError("registration differs from the frozen scale study")
        (stage / "plan.json").write_bytes(raw)
        for name in ("corpus", "cases.json", "plan.json"):
            shutil.move(stage / name, args.output / name)


if __name__ == "__main__":
    main()
