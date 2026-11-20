#!/usr/bin/env python3
"""Verify the published primary CPP20 v8/v9 Cloud Run performance comparison."""
import argparse
from datetime import datetime
import json
from pathlib import Path
import re
from statistics import median

from verify_durante_native_boundaries import AC, TLE, digest, model_binding, require

ROOT = Path(__file__).resolve().parents[1]
PROTOCOL_SHA256 = "4a923f4c49ef7f489389166088d2feb39c02a6f6949d4b01265ee9d77325597f"
ARMS = ("baseline", "candidate")
WEIGHTS = ("instruction_ns_numerator", "movs_byte_ns_numerator", "stos_byte_ns_numerator")
COUNTERS = ("instructions", "movs_bytes", "stos_bytes")
COLUMNS = ["order", "round", "arm", "nominal_ms", "aonohako_cpu_ns", "aonohako_wall_ms", *COUNTERS, "estimated_ns"]


def encoded(obj):
    return (json.dumps(obj, indent=2, allow_nan=False) + "\n").encode()


def check_arms(data, protocol):
    policy = data["shared_policy"]
    clock = protocol["clock"]
    require(all(policy[k] == clock[k] for k in (*WEIGHTS, "ns_denominator", "method"))
            and policy["family"] == "cpp" and policy["dispatch_profile"] == "default"
            and policy["cpuid_profile"] == "x86-64-v2", "incorrect shared CPP20 policy")
    require(set(data["arms"]) == set(ARMS) and re.fullmatch(r"sha256:[0-9a-f]{64}", data["image_digest"]),
            "invalid arms or image digest")
    lock = ROOT / "third_party/durante/toolkit/sdk.lock.json"
    for arm, tag in zip(ARMS, ("v8", "v9")):
        info, expected = data["arms"][arm], protocol[arm]
        vendor = (ROOT / f"third_party/durante-{tag}/UPSTREAM.json").read_bytes()
        manifest = json.loads(vendor)
        require(digest(vendor) == info["upstream_manifest_sha256"] and
                digest(lock.read_bytes()) == manifest["files"]["toolkit/sdk.lock.json"], f"{arm}: vendor pin mismatch")
        inst = info["installation"]
        require(inst["source_commit"] == expected["durante_source"] == info["source_marker"] == manifest["commit"]
                and inst["client_sha256"] == expected["ordinary_client_sha256"]
                and info["client_text_sha256"] == expected["client_text_sha256"] and inst["unchanged_before_after"] is True
                and inst["release_core_sha256"] == protocol["platform"]["dynamorio_core_sha256"]
                and inst["runtime_source_commit"] == json.loads(lock.read_bytes())["source_commit"]
                and inst["runtime_patch_sha256"] == manifest["files"]["toolkit/patches/dynamorio-vfork-lifetime.patch"]
                and inst["runtime_code_replay_patch_sha256"] == manifest["files"]["toolkit/patches/dynamorio-code-replay.patch"],
                f"{arm}: source, client or runtime mismatch")
        require(info["engine_profile"] == f"dynamorio-11.3-durante-{tag}-vm32m"
                and info["aonohako_build_tag"] == f"durante_{tag}"
                and all(re.fullmatch(r"[0-9a-f]{64}", info[k]) for k in ("worker_sha256", "reference_sha256"))
                and re.fullmatch(r"[0-9a-f]{40}", info["aonohako_source_commit"]), f"{arm}: invalid worker identity")
        bound_policy = {**policy, "runtime_id": "CPP20@" + data["image_digest"], "engine_profile": info["engine_profile"]}
        require(info["model_binding"] == model_binding(bound_policy, info["runtime_options_id"]),
                f"{arm}: model is not bound to the observed image, profile and runtime options")
    baseline, candidate = (data["arms"][arm] for arm in ARMS)
    require(baseline["installation"]["launcher_sha256"] == candidate["installation"]["launcher_sha256"]
            and re.fullmatch(r"runtime-options-v1:sha256:[0-9a-f]{64}", baseline["runtime_options_id"])
            and baseline["runtime_options_id"] == candidate["runtime_options_id"], "different execution options")
    link = data["package_link"]
    require(link["all_executions_loaded_patched_core"] is True and link["v9_text_identical"] is True
            and link["matching_patched_toolchain_rebuild_v8_sha256"] == baseline["installation"]["client_sha256"]
            and link["matched_candidate_sha256"] == candidate["installation"]["client_sha256"]
            and link["initial_candidate_sha256"] != link["matched_candidate_sha256"], "incorrect primary package link")


def check_input_seal(cases, protocol):
    inputs, request_hashes = {}, []
    for name, case in cases.items():
        identity = case["identity"]
        inputs[name] = {k: identity[k] for k in ("elf_sha256", "stdin_sha256", "stdout_sha256", "request_sha256")}
        requests = identity["request_sha256"]
        limits = {str(protocol["full"]["nominal_ms"])}
        if name in protocol["cutoff"]["nominal_ms"]:
            limits.add(str(protocol["cutoff"]["nominal_ms"][name]))
        require(set(requests) == limits, f"{name}: incorrect request limit set")
        request_hashes.extend(requests.values())
        for field in ("source", "stdin", "stdout"):
            if field in identity:
                require(digest(identity[field].encode()) == identity[field + "_sha256"], f"{name}: {field} hash mismatch")
    require(digest(encoded(inputs)) == protocol["input_manifest_sha256"]
            and digest(("\n".join(request_hashes) + "\n").encode()) == protocol["allowlist_sha256"],
            "program, oracle or request allowlist differs from the sealed input manifest")


def schedule(protocol):
    names = protocol["full"]["ids"]
    for phase in ("full", "cutoff"):
        limits = ({name: protocol["full"]["nominal_ms"] for name in names}
                  if phase == "full" else protocol["cutoff"]["nominal_ms"])
        ids = list(limits)
        for index in range(protocol[phase]["rounds"]):
            shift = index % len(ids)
            for name in ids[shift:] + ids[:shift]:
                arms = ARMS if (index + 1 + names.index(name)) % 2 else reversed(ARMS)
                for arm in arms:
                    yield phase, name, index + 1, arm, limits[name]


def check_trials(data, protocol):
    cases = data["cases"]
    require(set(cases) == set(protocol["full"]["ids"]) and data["trial_columns"] == COLUMNS, "wrong cases or columns")
    check_input_seal(cases, protocol)
    fields = dict(verdict_source="durante_judge", authority="diagnostic_only", clock_version=1,
                  clock_metric="model_weighted_nanoseconds_v1", progress_overflow=0)
    outcomes = {"full": dict(fields, status=AC, state="complete", termination="stdout", quality="uninterrupted",
                             complete=True, output_matches=True, exit_code=0, score=1, cpuid_profile="x86-64-v2"),
                "cutoff": dict(fields, status=TLE, state="budget_exceeded", termination="durante_work_clock",
                               quality="incomplete", complete=False, output_matches=None, exit_code=None, score=None,
                               cpuid_profile=None)}
    require(json.dumps(data["fields_identical_for_every_trial_in_group"], sort_keys=True) ==
            json.dumps(outcomes, sort_keys=True), "incorrect completion or cutoff record")
    observed = {}
    clock, hard = protocol["clock"], protocol["watchdogs"]
    for name, case in cases.items():
        full = {arm: {} for arm in ARMS}
        for phase in outcomes:
            for row in case[phase]:
                order, n, arm, budget, cpu, wall, instr, movs, stos, estimate = row
                require(order not in observed and arm in ARMS and all(type(v) is int for v in
                        (order, n, budget, cpu, wall, instr, movs, stos)), f"{name}: invalid trial identifiers/values")
                observed[order] = phase, name, n, arm, budget
                counts = (instr, movs, stos)
                require(0 < cpu <= hard["cpu_ms"] * 1_000_000 and 0 <= wall <= hard["wall_ms"]
                        and all(0 <= v < 2**64 for v in counts), f"{name}/{order}: invalid timing or progress")
                weighted = sum(v * clock[k] for v, k in zip(counts, WEIGHTS))
                expected_ns = -(-weighted // clock["ns_denominator"])
                crossed = weighted > budget * 1_000_000 * clock["ns_denominator"]
                if phase == "full":
                    require(not crossed and type(estimate) is int and estimate == expected_ns, f"{name}/{order}: terminal estimate")
                    require(n not in full[arm], f"{name}: duplicate full repetition")
                    full[arm][n] = cpu, wall, counts
                else:
                    require(crossed and estimate is None, f"{name}/{order}: live prefix did not establish cutoff")
        samples = {arm: [full[arm][n] for n in range(1, protocol["full"]["rounds"] + 1)] for arm in ARMS}
        med = {arm: median(s[0] for s in rows) for arm, rows in samples.items()}
        ranges = {arm: {k: [min(s[2][i] for s in rows), max(s[2][i] for s in rows)]
                        for i, k in enumerate(COUNTERS)} for arm, rows in samples.items()}
        same = len({s[2] for rows in samples.values() for s in rows}) == 1
        require(case["median_cpu_ns"] == med and case["median_wall_ms"] ==
                {arm: median(s[1] for s in rows) for arm, rows in samples.items()}
                and case["paired_cpu_differences_ns"] == [b[0]-a[0] for a,b in zip(samples["baseline"], samples["candidate"])]
                and case["full_counter_ranges"] == ranges and case["exact_counters_across_all_full_runs"] is same
                and case["median_cpu_change_percent"] == round(100 * (med["candidate"]/med["baseline"] - 1), 4),
                f"{name}: summary does not match individual pairs")
    expected = dict(enumerate(schedule(protocol), 1))
    require(observed == expected and len(observed) == 102, "trial count, order, arms or nominal budgets differ")


def verify(data):
    protocol = data["protocol"]
    require(data["schema"] == 1 and digest(encoded(protocol)) == data["protocol_sha256"] == PROTOCOL_SHA256,
            "not the sealed protocol for the primary linked build")
    check_arms(data, protocol)
    check_trials(data, protocol)
    placement, cleanup = data["placement"], data["cleanup"]
    instances = placement["instance_id_sha256_counts"]
    require(placement["submitted"] == placement["request_logs"] == 102 and placement["complete"] is True
            and placement["http_status_counts"] == {"200": 102} and placement["unknown_requests"] == 0
            and placement["distinct_instances"] == len(instances) == 1
            and all(re.fullmatch(r"[0-9a-f]{64}", k) and v == 102 for k, v in instances.items())
            and placement["outcomes_by_arm"] == {arm: {AC: 45, TLE: 6} for arm in ARMS}, "incomplete placement evidence")
    require(placement["revision"] and cleanup["revision"] == placement["revision"]
            and all(cleanup[k] == 404 for k in ("service_http", "revision_http", "artifact_registry_package_http"))
            and datetime.fromisoformat(data["started_utc"]) <= datetime.fromisoformat(data["completed_utc"])
            <= datetime.fromisoformat(cleanup["verified_utc"]), "incomplete or mismatched cleanup evidence")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("receipt", type=Path)
    args = parser.parse_args()
    try:
        verify(json.loads(args.receipt.read_bytes()))
    except (OSError, KeyError, TypeError, ValueError, RuntimeError) as error:
        parser.exit(1, f"invalid v9 performance receipt: {error}\n")
    print("Verified 90 completed and 12 cutoff requests on one Cloud Run instance, with both arms and cleanup reconciled.")
