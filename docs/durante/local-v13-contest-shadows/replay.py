"""Replay the 108 archived public HTTP responses and compare with the Go summary."""
import argparse
from collections import Counter
from hashlib import sha256
import json
from pathlib import Path
from statistics import median
import subprocess
import tarfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
PANELS = ("lcs-layout", "maxsub-floor", "path-mwis")
ARCHIVE_SHA256 = "200ed7039be730e07a72d14f70152ba128fcaa70d67412beff104148f48aef58"
AC, TLE = "Accepted", "Time Limit Exceeded"
AUTH = "diagnostic_only"
CPU_STOPS = {"cpu_time", "cpu_time_final"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return sha256(data).hexdigest()


def ceil_div(numerator, denominator):
    return (numerator + denominator - 1) // denominator


def schedule(plan):
    groups = plan["inputs"]
    ref = plan["reference"]
    for rep in range(3):
        for group in groups[::1 if rep % 2 == 0 else -1]:
            yield "before", rep + 1, f"{group}-{ref}", 0
    cases = [(f"{group}-{alt}", factor) for group in groups
             for alt in plan["alternatives"] for factor in (1, 2)]
    for rep in range(3):
        rotated = cases[rep:] + cases[:rep]
        for name, factor in rotated[::1 if rep % 2 == 0 else -1]:
            yield "shadow", rep + 1, name, factor
    for rep in range(3):
        for group in groups[::-1 if rep % 2 == 0 else 1]:
            yield "after", rep + 1, f"{group}-{ref}", 0


def diagnostic(receipt, limit_ms, runtime):
    shadow = receipt["work_measurement_shadow"]
    inner = shadow["response"]
    work, run = inner["work_measurement"], inner["work_measurement_execution"]
    estimate, judge = run["time_estimate"], inner["work_measurement_judgment"]
    require(shadow["authority"] == run["authority"] == work["authority"] ==
            estimate["authority"] == judge["authority"] == AUTH, "unexpected diagnostic authority")
    installation = work["installation"]
    require(installation["unchanged_before_after"] and installation["source_commit"] == runtime["upstream"] and
            installation["client_sha256"] == runtime["files"]["lib/libdurante.so"] and
            installation["launcher_sha256"] == runtime["files"]["dynamorio/bin64/drrun"] and
            installation["release_core_sha256"] == runtime["files"]["dynamorio/lib64/release/libdynamorio.so"],
            "different sealed installation")
    require(work["complete"] and work["quality"] == "uninterrupted" and
            estimate["engine_profile"] == work["engine_profile"] == runtime["engine"] and
            estimate["runtime_id"] == "CPP20@" + runtime["image"] and
            run["state"] == judge["state"] == "complete" and run["output_matches"] and
            run["termination_source"] == judge["termination_source"] == "stdout" and
            judge["nominal_time_ms"] == run["nominal_limits"]["time_ms"] == limit_ms and
            estimate["method"] == "weighted-work-nanoseconds-v1", "different completed diagnostic")
    ns = ceil_div(work["instructions"] * estimate["instruction_ns_numerator"] +
                  work["rep_bytes"] * estimate["rep_byte_ns_numerator"], estimate["ns_denominator"])
    verdict = AC if ns <= limit_ms * 1_000_000 else TLE
    require(ns == estimate["nanoseconds"] == judge["estimated_time_ns"] and
            inner["verdict_source"] == "durante_judge" and
            inner["status"] == judge["verdict"] == shadow["shadow_status"] == verdict and
            shadow["primary_status"] == receipt["status"] and
            shadow["agreement"] == ("agree" if verdict == receipt["status"] else "disagree"),
            "score or verdict differs")
    return ns, verdict


def panel(archive, name, runtime, public_rows):
    def read(member):
        return archive.extractfile(f"{name}/{member}").read()

    plan = json.loads(read("plan.json"))
    meta = json.loads(read("complete.json"))
    frozen = json.loads(read("frozen.json"))
    reg = json.loads(read("registration.json"))
    require(digest(read("registration.json")) == plan["registration_sha256"] and
            digest(read("frozen.json")) == plan["manifest_sha256"] == reg["payload"]["plan.json"] and
            reg["archive_sha256"] == plan["payload_sha256"], "changed workload registration")
    require(plan["problem"] == "registered-" + name and plan["cpu"] == 5 and meta["cpu"] == [5] and
            meta["registry"] == {"schema": 2, "cases": []} and meta["runtime"] == runtime["image"] and
            meta["controller_sha"] == "8d7743569feac47ffd3d6c20e64d54d9af16a3a99eb333edd078d0aa56d6e0f5" and
            plan["factors"] == [1, 2] and plan["reference_limit_ms"] == 500 and
            plan["alternative_repeats"] == plan["reference_repeats_each_side"] == 3, "different collection")
    controls = {g: {"before": [], "after": []} for g in plan["inputs"]}
    for group, names in frozen["protocol"]["groups"].items():
        require(names == [f"{group}-{s}" for s in [plan["reference"], *plan["alternatives"]]], "different group")
        for case in names:
            source = frozen["cases"][case]
            require(source["elf_sha256"] == reg["payload"]["bin/" + case] and
                    source["stdin_sha256"] == reg["payload"][source["stdin"]] == digest(read(source["stdin"])) and
                    source["expected_sha256"] == digest(plan["answers"][group].encode()), "different ELF, input or answer")

    rows = [json.loads(line) for line in read("responses.jsonl").splitlines()]
    require(len(rows) == meta["responses"] == 36, "missing HTTP receipt")
    decisions, completed, bounds = Counter(), Counter(), Counter()
    cells = {}
    for row, expected in zip(rows, schedule(plan), strict=True):
        stage, rep, case, factor = expected
        group = case.split("-")[0]
        require(tuple(row[k] for k in ("stage", "rep", "case", "factor")) == expected and
                row["http"] == 200, f"different response order or HTTP status in {name} {expected}")
        response = row["receipt"]
        status, source, native_ns = response["status"], response["verdict_source"], response["cpu_time_ns"]
        limit = row["limit_ms"]
        require(native_ns > 0 and
                ((status == AC and source == "stdout" and response["exit_code"] == 0) or
                 (status == TLE and source in CPU_STOPS and native_ns > limit * 1_000_000)) and
                "cpu_time_normalization" not in response, "unexpected public native authority or time")
        if stage != "shadow":
            require(limit == 500 and status == AC and not any(k.startswith("work_measurement") for k in response),
                    "different native reference")
            controls[group][stage].append(native_ns)
            continue

        require(limit == meta["limits"][group][str(factor)] ==
                ceil_div(factor * median(controls[group]["before"]), 1_000_000), "different derived deadline")
        ns, predicted = diagnostic(response, limit, runtime)
        native, shadow = ("ac" if status == AC else "tle"), ("ac" if predicted == AC else "tle")
        outcome = f"native_{native}_shadow_{shadow}"
        decisions[outcome] += 1
        cells.setdefault(f"{case}/{factor}", Counter())[outcome] += 1
        if status == AC:
            error = 5 * (ns - native_ns)
            bucket = ("under_by_more_than_20_percent" if error < -native_ns else
                      "over_by_more_than_20_percent" if error > native_ns else "within_20_percent")
            completed[bucket] += 1
        else:
            bucket = ("sampled" if response.get("cpu_time_sampling") else
                      "under_by_more_than_20_percent" if ns <= 800_000 * limit else "inconclusive")
            bounds[bucket] += 1
        public_rows.append({"problem_id": name, "stdin_source": "inline", "response": response})
    return dict(requests=len(rows), controls_ns=controls, limits_ms=meta["limits"], cells=cells,
                decisions=decisions, completed_native_time_accuracy=completed, native_tle_time_bound=bounds)


def replay():
    runtime = json.loads((HERE.parent / "gce-v13-judge-transfer/registration.json").read_bytes())["plan"]["arms"]["v13"]
    archive = HERE / "evidence.tar.gz"
    require(digest(archive.read_bytes()) == ARCHIVE_SHA256, "different evidence archive")
    public_rows = []
    with tarfile.open(archive) as source:
        results = {name: panel(source, name, runtime, public_rows) for name in PANELS}
    stream = "".join(json.dumps(row) + "\n" for row in public_rows)
    public = json.loads(subprocess.check_output(["go", "run", "-tags=durante_v13", "./cmd/durante-shadow-summary"],
                                               input=stream, text=True, cwd=ROOT))["problems"]
    require(public.keys() == results.keys(), "unexpected public summary problem set")
    for name, result in results.items():
        block = public[name]["cpu_comparison"]
        require(public[name]["testcases"] == 24 and not block["excluded"] and len(block["cohorts"]) == 1,
                "public summary excluded a paired response or used a different cohort")
        cohort, = block["cohorts"].values()
        actual = Counter({k: v for k, v in cohort.items() if k.startswith(("native_ac_shadow_", "native_tle_shadow_"))})
        require(actual == result["decisions"] and all(Counter(cohort[k]) == result[k] for k in
                ("completed_native_time_accuracy", "native_tle_time_bound")), "public summary accounting differs")
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--verify", action="store_true", help="compare with the checked-in exact result")
    args = parser.parse_args()
    result = replay()
    if args.verify:
        require(result == json.loads((HERE / "results.json").read_bytes()), "different publication")
        print("Verified 108 HTTP responses, 36 reference runs, 72 shadow pairs, and all public summary counts.")
    else:
        print(json.dumps(result, indent=2, sort_keys=True))
