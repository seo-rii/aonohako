"""Compute necessary single-coefficient inequalities for observed native shortest-path verdicts."""
from fractions import Fraction
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys

HERE = Path(__file__).resolve().parent
CURRENT = HERE.parent
OLD = CURRENT.parent / "gce-v13-judge-transfer"
PHASES = ("primary", "repeat")


def read(path):
    return json.loads(path.read_bytes())


def digest(data):
    return hashlib.sha256(data).hexdigest()


def require(ok, message):
    if not ok:
        raise ValueError(message)


def validate(study, entrypoint):
    actual = subprocess.check_output([sys.executable, "-B", str(study / entrypoint)])
    require(json.loads(actual) == read(study / "results.json"), f"{study.name} published replay differs")


def rows(path):
    with gzip.open(path, "rt") as source:
        return [json.loads(line) for line in source]


def constraint(limit_ms, verdict, counts):
    require(counts and all(type(w) is int and w > 0 for w in counts), "invalid completed work")
    if verdict == "Accepted":
        return "upper_inclusive", Fraction(limit_ms * 1_000_000, min(counts))
    if verdict == "Time Limit Exceeded":
        return "lower_exclusive", Fraction(limit_ms * 1_000_000, max(counts))
    raise ValueError("a unanimous native AC or TLE is required")


def summarize(cell, limit_ms, stdin, elf, native, full, oracle):
    require(len(native) == 6 and len(full) == 4, "changed two-boot sample size")
    verdicts = {row["receipt"]["status"] for row in native}
    require(len(verdicts) == 1, "native decisions disagree")
    status, = verdicts
    values = []
    for row in native + full:
        require(row["binary_sha256"] == elf, "the deadline and completion must use the same ELF")
    for row in full:
        receipt = row["receipt"]
        meter = receipt["work_measurement"]
        require(receipt["status"] == "Accepted" and row["stdout"] == oracle and meter["complete"] is True
                and receipt["work_measurement_execution"]["termination_source"] == "stdout", "incomplete counter source")
        rep = meter["rep_breakdown"]
        values.append(meter["instructions"] + rep["movs_bytes"] + rep["stos_bytes"])
    side, coefficient = constraint(limit_ms, status, values)
    return dict(cell=cell, native_verdict=status, native_repetitions=len(native), limit_ms=limit_ms,
                native_request_sha256=sorted({r["request_sha256"] for r in native}),
                complete_request_sha256=sorted({r["request_sha256"] for r in full}), full_work=values,
                input_sha256=digest(stdin.encode()), binary_sha256=elf, constraint=side,
                ns_per_work={"numerator": coefficient.numerator, "denominator": coefficient.denominator})


def compare(lower, upper):
    require(lower["constraint"] == "lower_exclusive" and upper["constraint"] == "upper_inclusive",
            "need a strict lower and inclusive upper bound")
    lo, hi = (Fraction(**x["ns_per_work"]) for x in (lower, upper))
    return dict(compatible=lo < hi, lower_cell=lower["cell"], upper_cell=upper["cell"])


def run():
    # Verify full receipts, HTTP identities, CPU affinity, model and cleanup with both original scorers.
    validate(OLD, "verify.py")
    validate(CURRENT, "replay.py")
    old_reg = read(OLD / "registration.json")
    new_reg = read(CURRENT / "registration.json")
    # Recover the exact historical stdin, ELF and answer for each paired deadline.
    spec = importlib.util.spec_from_file_location("shortest_paths_transfer_protocol", OLD / "protocol.py")
    protocol = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(protocol)
    pilot = CURRENT.parent / "gce-qualified-judge"
    cells = protocol.panel(read(pilot / "registration.json")["corpus"], read(pilot / "refit/case_manifest.json"),
                           read(pilot / "refit/freeze.json"))
    studies = [read(root / "evidence/infrastructure.json") for root in (OLD, CURRENT)]
    for field in ("instance_id_sha256", "disk_id_sha256", "boot_sha256"):
        require(len({guest[field] for study in studies for guest in study.values()}) == 4, "reused study or boot")
    old_rows = {engine: [r for phase in PHASES for r in rows(OLD / "evidence" / phase / engine / "trials.jsonl.gz")]
                for engine in ("v12", "v13")}
    out = {}
    for engine in ("v12", "v13"):
        plan = new_reg["plans"][engine]
        require(plan["baseline_binding"] == old_reg["plan"]["arms"][engine]["binding"], "different deciding clock")
        registered = plan["cases"]["block8"]
        fresh = [r for p in PHASES for r in rows(CURRENT / "evidence" / engine / p / "trials.jsonl.gz")
                 if r["case_id"] == "block8"]
        upper = summarize("block8-contest", registered["limits"]["contest"], registered["stdin"], registered["binary_sha256"],
                          [r for r in fresh if r["boundary"] == "contest" and r["kind"] == "native"],
                          [r for r in fresh if r["boundary"] == "ample" and r["kind"] == "shared"],
                          registered["expected_stdout"])
        opposed = {}
        for label, prefix, full_boundary in (("same_input", "shortest_paths-reference", "high"),
                                             ("held_out_input", "shortest_paths-b-fast", "ample")):
            at, complete = prefix + "-low", prefix + "-" + full_boundary
            low, full = cells[at], cells[complete]
            require(low["problem"] == full["problem"] == "shortest_paths" and
                    all(low[k] == full[k] for k in ("role","stdin","binary_sha256","expected_stdout")),
                    "different graph, solver or expected output for historical completion")
            require((low["stdin"] == registered["stdin"]) == (label == "same_input"), "unexpected graph identity")
            # The transfer used a single common native arm and both sealed deciding arms.
            bound = summarize(at, low["time_ms"], low["stdin"], low["binary_sha256"],
                              [r for r in old_rows["v12"] if r["cell"] == at and r["kind"] == "native"],
                              [r for r in old_rows[engine] if r["cell"] == complete and r["kind"] == "judge"],
                              low["expected_stdout"])
            opposed[label] = dict(completion_limit_ms=full["time_ms"], lower=bound, **compare(bound, upper))
        out[engine] = dict(upper=upper, comparisons=opposed, model_binding=plan["baseline_binding"])
    return dict(schema=1, sources={r.name: digest((r / "registration.json").read_bytes()) for r in (CURRENT, OLD)},
                engines=out)


if __name__ == "__main__":
    print(json.dumps(run(), indent=2, sort_keys=True))
