"""Reproduce the pinned C++ corpus, frozen model weights and GCE trial order."""
import argparse
from collections import Counter
from fractions import Fraction
import gzip
import hashlib
import io
import json
from pathlib import Path
import runpy
import shutil
import subprocess
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
AONO = "0a64275ddf939d89717274d94a3b5eb357716466"
DURANTE = "b43685c76b94a365886031359b032899b009cd1b"
V11 = "docs/durante/gce-v11-cpp20-accuracy"
CONTEST = "experiments/contest_accuracy"
V7 = "docs/durante/gce-cpp20-v7-qualification.json"
SEED = "aonohako-v12-clock-transfer-1"


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def encoded(obj):
    return (json.dumps(obj, indent=2) + "\n").encode()


def write_verified(root, name, raw, digest, executable=False):
    if sha(raw) != digest:
        raise ValueError(f"{name} differs from the registered digest {digest}")
    path = root / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    if executable:
        path.chmod(0o755)


def make(out, durante, binaries):
    sources = {"aonohako": {}, "durante": {}}

    def read(project, path):
        repo, revision = (REPO, AONO) if project == "aonohako" else (durante, DURANTE)
        raw = subprocess.check_output(["git", "-C", repo, "show", revision + ":" + path])
        sources[project][path] = sha(raw)
        return raw

    def case(program, case_id, group, stdin, stdout, stdin_hash, stdout_hash, binary_hash):
        relative = {"binary_path": f"corpus/bin/{program}", "stdin_path": f"corpus/stdin/{case_id}.txt",
                    "stdout_path": f"corpus/stdout/{case_id}.txt"}
        write_verified(out, relative["stdin_path"], stdin.encode(), stdin_hash)
        write_verified(out, relative["stdout_path"], stdout.encode(), stdout_hash)
        return dict(program=program, case_id=case_id, group=group, **relative, stdin_sha256=stdin_hash,
                    stdout_sha256=stdout_hash, binary_sha256=binary_hash)

    archive = read("aonohako", f"{V11}/corpus/elfs.tar.xz")
    build = json.loads(read("aonohako", f"{V11}/corpus/build.json"))
    historical = json.loads(read("aonohako", f"{V11}/corpus/cases.json"))
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:xz") as tar:
        for program, digest in build["executables"].items():
            member = tar.getmember("bin/" + program)
            if not member.isfile():
                raise ValueError(f"expected an ELF file for {program}")
            write_verified(out, "corpus/bin/" + program, tar.extractfile(member).read(), digest, True)
    cases = []
    for row in historical:
        group = {"train": "historical_training", "heldout": "historical_holdout"}[row["split"]]
        if row["binary_sha256"] != build["executables"][row["program"]]:
            raise ValueError(f"unregistered binary for {row['case_id']}")
        cases.append(case(row["program"], row["case_id"], group, row["stdin"], row["stdout"],
                          row["input_sha256"], row["expected_sha256"], row["binary_sha256"]))

    registration = json.loads(read("durante", f"{CONTEST}/plan.json"))
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        for program in ("rice", "wall", "money", "plane"):
            generator = tmp / f"{program}.py"
            generator.write_bytes(read("durante", f"{CONTEST}/{program}.py"))
            generate = runpy.run_path(str(generator))["make"]
            cpp = tmp / f"{program}.cpp"
            cpp.write_bytes(read("durante", f"{CONTEST}/{program}.cpp"))
            if binaries:
                binary = binaries / program
            else:
                binary = tmp / program
                subprocess.run(["g++", "-std=c++20", "-O2", "-static", "-s", "-march=x86-64-v2",
                                "-Wl,--build-id=none", cpp, "-o", binary], check=True)
            raw = binary.read_bytes()
            rows = [r for r in registration["cases"] if r["program"] == program and r["split"] == "holdout"]
            for i, row in enumerate(rows):
                if sha(raw) != row["binary_sha256"]:
                    raise ValueError(f"{program} does not match the registered contest ELF")
                if i == 0:
                    write_verified(out, "corpus/bin/" + program, raw, row["binary_sha256"], True)
                stdin, stdout = generate(row["variant"])
                cases.append(case(program, row["case_id"], "contest_holdout", stdin, stdout, row["stdin_sha256"],
                                  row["stdout_sha256"], row["binary_sha256"]))

    groups = dict(Counter(c["group"] for c in cases))
    ids = [c["case_id"] for c in cases]
    if groups != {"historical_training": 6, "historical_holdout": 15, "contest_holdout": 12} or len(set(ids)) != 33:
        raise ValueError("unexpected registered corpus")
    (out / "cases.json").write_bytes(encoded(cases))

    schedule = []
    rounds = [("warmup", 0), ("native", 1), ("live", 1), ("native", 2), ("counters", 1),
              ("native", 3), ("native", 4), ("native", 5), ("counters", 2), ("native", 6),
              ("live", 2), ("native", 7)]
    for kind, rep in rounds:
        for name in sorted(ids, key=lambda s: sha(f"{SEED}/{kind}/{rep}/{s}".encode())):
            schedule.append(dict(case_id=name, kind=kind, rep=rep))

    v7 = json.loads(read("aonohako", V7))["frozen_external_rounds"]["split-gce"]["policy"]
    v11 = json.loads(gzip.decompress(read("aonohako", f"{V11}/evaluation.json.gz")))["models"]["cpp20_train_only"]
    weights = v11["latency_memory"]["weights_ns"]
    features = {"instructions": "instruction", "movs_bytes": "movs_byte", "stos_bytes": "stos_byte"}
    models = {
        "v7_split_gce": {"kind": "live", "field": "frozen_external_rounds.split-gce.policy",
                         "weights_ns": {key: str(Fraction(v7[f"{field}_ns_numerator"], v7["ns_denominator"]))
                                        for key, field in features.items()}},
        "v11_latency_memory": {"kind": "counters", "field": "models.cpp20_train_only.latency_memory.weights_ns",
                               "weights_ns": {"cycles_x16": str(Fraction(weights["latency_cycles"]) / 16),
                                              "memory_operand_bytes": weights["memory_bytes"]}},
        "v11_split": {"kind": "live", "field": "models.cpp20_train_only.instruction_movs_stos.weights_ns",
                      "weights_ns": v11["instruction_movs_stos"]["weights_ns"]},
    }
    # Use archived v11 labels exclusively. Every case is weighted equally regardless of duration.
    reference_data = json.loads(gzip.decompress(read("aonohako", f"{V11}/measurements.json.gz")))
    reference_plan = json.loads(read("aonohako", f"{V11}/analysis-plan.json"))
    program_splits = {row["program"]: row["split"] for row in historical}
    if {c["case_id"] for c in reference_data["cases"]} != {c["case_id"] for c in historical}:
        raise ValueError("unexpected archived training corpus")
    with tempfile.TemporaryDirectory() as tmp:
        fitter = Path(tmp) / "models.py"
        fitter.write_bytes(read("aonohako", f"{V11}/models.py"))
        implementation = runpy.run_path(str(fitter))
    archived = [implementation["checked_case"](row, reference_plan, program_splits) for row in reference_data["cases"]]
    fitted, rank = implementation["fit"](archived, ("instructions", "rep_bytes"))
    if len(archived) != 21 or rank != 2:
        raise ValueError("expected the full-rank 21-case historical fit")
    denominator = 1_000_000_000
    numerators = {k: max(1, round(v * denominator)) for k, v in fitted.items()}
    models["v11_wide_live"] = {
        "kind": "live", "method": "weighted-work-nanoseconds-v1",
        "fit": "Nonnegative no-intercept least squares minimizing equal-case squared relative error over all "
               "21 historical v11 native medians and componentwise metered medians. Round to nearest numerator "
               "with ties to even, at least one. All 21 historical inputs are training repeats for this model. "
               "Evaluate transfer independently on the 12 contest holdouts.",
        "training_cases": sorted(c["id"] for c in archived), "independent_group": "contest_holdout",
        "ns_denominator": denominator, "numerators": numerators,
        "weights_ns": {k: str(Fraction(v, denominator)) for k, v in numerators.items()},
    }
    vendor = json.loads(read("aonohako", "third_party/durante-v12/UPSTREAM.json"))
    if vendor["commit"] != DURANTE:
        raise ValueError("wrong Durante client revision")
    return dict(schema=1, authority="diagnostic_only", aonohako_commit=AONO, durante_commit=DURANTE,
                cases_sha256=sha(encoded(cases)), groups=groups, sources_sha256=sources,
                primary_model="v7_split_gce", models=models,
                native_reference="Median of seven unnormalized native Aonohako cpu_time_ns receipts, excluding warmup. "
                                 "Retain cpu_accounting.selected_source, wait4 user/system and process-clock evidence.",
                prediction="Ceil the exact dot product in ns for each metered repetition. Freeze all coefficients.",
                accuracy="A case passes at 20 percent only if both predictions lie within the inclusive band.",
                deadlines="Floor 80 percent and ceil 120 percent of the native median to whole milliseconds. "
                          "Strictly greater than the limit is a timeout. Compare against all seven native votes, "
                          "report unanimous false passes, unanimous false timeouts and mixed native decisions.",
                stability="Retain every repetition, component deltas, native minimum, median, maximum, and "
                          "native (maximum minus minimum) divided by median. Describe the subset with a native "
                          "median of at least 20 ms and spread at most 10 percent. Retain all cases for primary scoring.",
                lower_bound="Among ordered contest holdout pairs, take R = min(native slow) / max(native fast). "
                            "For instructions, MOVS bytes and STOS bytes on live samples, take C = max over features "
                            "of max(slow) / min(fast). Ignore 0/0, skip a pair with positive/zero. Report the "
                            "largest positive (R-C)/(R+C) as a retrospective lower bound on worst relative error "
                            "of a nonnegative homogeneous split clock, without selecting a model.",
                schedule_seed=SEED, schedule=schedule)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--durante-repo", type=Path, required=True)
    parser.add_argument("--contest-binaries", type=Path, help="directory of the four exact registered ELFs; default rebuilds with g++")
    parser.add_argument("--output", type=Path, required=True, help="payload directory; corpus, cases.json and plan.json must be new")
    args = parser.parse_args()
    out = args.output
    out.mkdir(parents=True, exist_ok=True)
    for name in ("cases.json", "plan.json", "corpus"):
        if (out / name).exists():
            raise FileExistsError(out / name)
    with tempfile.TemporaryDirectory(dir=out) as stage:
        stage = Path(stage)
        plan = encoded(make(stage, args.durante_repo, args.contest_binaries))
        if plan != (HERE / "plan.json").read_bytes():
            raise ValueError("corpus/weights/trial order differs from the registered plan")
        (stage / "plan.json").write_bytes(plan)
        for name in ("corpus", "cases.json", "plan.json"):
            shutil.move(stage / name, out / name)


if __name__ == "__main__":
    main()
