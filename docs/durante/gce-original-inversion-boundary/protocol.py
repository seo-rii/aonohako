"""Freeze correct submissions and the limits of the original v13 inversion case."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent / "gce-qualified-judge"
PREVIOUS = HERE.parent / "gce-inversion-clock"
sys.path.insert(0, str(PILOT))
import corpus

VARIANTS = {**{f"sqrt{n}": f"-DBLOCK_SIZE={n}" for n in (32, 128, 512, 1024, 2048)},
            **{f"hybrid{n}": f"-DINSERTION_CUTOFF={n}" for n in (16, 128, 512, 1024)}}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(path.read_bytes())


def make_plan(binaries):
    previous = read(PREVIOUS / "plan.json")
    cases = {key: dict(previous["cases"][key]) for key in ("original-fast", "original-wrong", "original-slow")}
    cases["original-fast"]["limits"] = {"ample": 30_000, "contest": 203}
    template = cases["original-fast"]
    if (template["stdin"], template["expected_stdout"]) != ("256 3072 4096 658417\n", "603431314\n"):
        raise ValueError("unexpected original input or output")
    for role in VARIANTS:
        name = "inversions-" + role
        cases["original-" + role] = {**template, "binary": "pilot/corpus/" + name, "role": role, "split": "variant",
                                     "binary_sha256": digest((binaries / name).read_bytes())}
    return {
        "schema": 1, "image": previous["image"], "baseline_binding": previous["baseline_binding"],
        "original_input_sha256": digest(template["stdin"].encode()), "cases": cases,
        "build": {"flags": list(corpus.FLAGS), "variants": VARIANTS,
                  "source_sha256": digest((HERE / "variants.cpp").read_bytes()),
                  "generator_sha256": digest((PILOT / "corpus/generator.hpp").read_bytes())},
        "decision": {
            "scope": "Existing inversions v13 model on the original C++20 input at 203 ms, 768 MiB and 60000 ms wall.",
            "controls": "Both boots must reproduce the original fast AC, wrong WA and slow in-flight TLE at 203 ms. "
                        "Every scheduled ample run must complete with its independently expected output and verdict.",
            "counterexample": "For a registered variant at 203 ms on each boot, require all three native trials to "
                              "agree and both deciding-clock trials to agree. One false pass or false timeout in "
                              "the same direction for the same variant on both fresh boots disqualifies automatic "
                              "clock routing of the original case, provided controls pass. A mixed cell is inconclusive.",
            "no_expansion": "Agreement on this panel supplies no new inputs, languages or limits for release.",
        },
    }


def build(binaries):
    binaries.mkdir(parents=True, exist_ok=True)
    for role, define in VARIANTS.items():
        subprocess.run(["g++", *corpus.FLAGS, define, HERE / "variants.cpp", "-o", binaries / ("inversions-" + role)],
                       check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binaries", required=True, type=Path)
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.build:
        build(args.binaries)
    registration = json.dumps(make_plan(args.binaries), indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(registration)
    else:
        print(registration, end="")
