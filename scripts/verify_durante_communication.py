#!/usr/bin/env python3
"""Verify communication role timing with the real HTTP API and native processes.

Use the network-none container described in verify_durante_observation.py,
with two CPUs and a /work tmpfs allowing 1048576 inodes. Mount this scripts
directory and internal/execute/testdata/durante/communication.c read-only.
"""
import argparse
import base64
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import secrets
import urllib.request

from verify_durante_observation import artifact, post, require, server
from verify_durante_pipeline import MARKER, expect_marker_reads


MARKERS = [MARKER.with_name(name) for name in ("colorsys.py", "antigravity.py", "ftplib.py")]
WORKER = {
    "AONOHAKO_DEPLOYMENT_TARGET": "cloudrun", "AONOHAKO_TRUSTED_RUNNER_INGRESS": "true",
    "AONOHAKO_COMMUNICATION_ENABLED": "true", "AONOHAKO_COMMUNICATION_MEMORY_BUDGET_MB": "2048",
    "AONOHAKO_COMMUNICATION_CPU_COUNT": "2", "GOMAXPROCS": "2", "AONOHAKO_COMMUNICATION_WALL_BUDGET_MS": "60000",
    "AONOHAKO_REQUIRE_WORK_ROOT_TMPFS": "true", "AONOHAKO_WORK_ROOT_MAX_FILES": "1048576",
}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def compile_fixture(url, path):
    markers = ", ".join(json.dumps(str(p)) for p in MARKERS)
    source = f"#define COMMUNICATION_LAUNCH_MARKERS {{{markers}}}\n" + path.read_text()
    result, _ = post(url, "/compile", {"lang": "C11", "sources": [artifact("Main.c", source)], "target": "Main"})
    require(result["status"] == "OK", f"communication fixture compile failed: {result.get('stderr')}")
    binary, = result["artifacts"]
    return binary, sha(source.encode())


def request(version, binary, source_sha, fingerprint, scenario, measured):
    seat_ids = ["p", "p"] if version == 1 else ["alpha", "beta"]
    modes = ["stream", "stream", "stream"]
    limit = 3000
    if scenario == "forfeit":
        modes = ["exit", "wait", "forfeit"]
    elif scenario == "cpu-stop":
        modes = ["burn", "burn", "wait"]
        limit = 100
    elif scenario == "manager-cpu-stop":
        modes = ["wait", "wait", "burn"]
        limit = 1000
    identities = [*seat_ids, "manager"]
    programs = {}
    for index, (program_id, mode) in enumerate(zip(identities, modes)):
        marker = "manager" if index == 2 else "shared" if version == 1 else f"seat{index}"
        binaries = [binary, artifact("mode.dat", mode), artifact("marker.dat", marker)]
        programs[program_id] = {
            "id": program_id, "lang": "C11", "source_sha256": source_sha,
            "binaries": [{**b, "sha256": sha(base64.b64decode(b["data_b64"], validate=True))} for b in binaries],
        }
    spec = {"version": version, "manager_program_id": "manager", "participant_count": 2}
    if version == 1:
        spec.update(participant_program_id="p", result_protocol="manager-result-v1")
    else:
        spec.update(participant_program_ids=seat_ids, result_protocol="match-result-v1", match_seed="3" * 64,
                    input_sha256=sha(b"mismatched" if scenario == "unstarted" else b""), runtime_fingerprint=fingerprint)
    return {"programs": list(programs.values()), "work_measurement": measured, "communication": spec,
            "limits": {"time_ms": limit, "memory_mb": 512, "output_bytes": 512 << 10, "workspace_bytes": 8 << 20}}


def check_cpu(result, roles, calibrated):
    normalization = result.get("cpu_time_normalization")
    require(bool(normalization) == calibrated, "unexpected normalization metadata")
    ns = [role.get("cpu_time_ns") for role in roles]
    require(sum(role["cpu_time_ms"] for role in roles) == result["cpu_time_ms"], "incorrect CPU millisecond total")
    if all(value is not None for value in ns):
        require(result.get("cpu_time_ns", 0) == sum(ns), "incorrect precise CPU total")
    else:
        require(not result.get("cpu_time_ns"), "precise total contains missing samples")
    for role in roles:
        cpu = role.get("cpu_time_ns")
        if cpu is not None:
            require(role["cpu_time_ms"] == (cpu + 999999) // 1000000, "wrong role CPU rounding")
        if calibrated and cpu is not None:
            raw = role["raw_cpu_time_ns"]
            den, num = normalization["observed_time_ns"], normalization["reference_time_ns"]
            require(cpu == (raw * num + den - 1) // den and role["raw_cpu_time_ms"] == (raw + 999999) // 1000000,
                    "incorrect raw or calibrated CPU attribution")
        if not calibrated:
            require("raw_cpu_time_ns" not in role and "raw_cpu_time_ms" not in role, "unexpected raw accounting")
    if calibrated and all(r.get("raw_cpu_time_ns") is not None for r in roles):
        require(result["raw_cpu_time_ns"] == sum(r["raw_cpu_time_ns"] for r in roles) and
                result["raw_cpu_time_ms"] == sum(r["raw_cpu_time_ms"] for r in roles), "incorrect raw totals")


def exercise(url, label, token, fingerprint, binary, source_sha, calibrated):
    for version, scenario, measured in ((1, "complete", False), (2, "complete", False),
                                        (2, "complete", True), (1, "cpu-stop", False),
                                        (2, "forfeit", False), (2, "manager-cpu-stop", False),
                                        (2, "unstarted", False)):
        req = request(version, binary, source_sha, fingerprint, scenario, measured)
        name = f"{label}-v{version}-{scenario}" + ("-measured" if measured else "")
        with ExitStack() as watching:
            if scenario in {"complete", "unstarted"}:
                for path in MARKERS:
                    watching.enter_context(expect_marker_reads(int(scenario == "complete"), path))
            result, _ = post(url, "/execute", req, token=token)
        roles = result.get("communication_cpu_by_role", [])
        match = result.get("match", {})
        if scenario == "unstarted":
            require(not roles and not result.get("started_participants") and match.get("error_code") == "input_integrity",
                    f"{name}: preflight attributed CPU to an unstarted role")
        else:
            ids = ["p", "p", "manager"] if version == 1 else ["alpha", "beta", "manager"]
            role_keys = [("participant", ids[0], 0), ("participant", ids[1], 1), ("manager", ids[2], None)]
            require([(r["role"], r["program_id"], r.get("participant_index")) for r in roles] == role_keys,
                    f"{name}: lost role identities; status={result['status']}, reason={result.get('reason')}")
            limit = req["limits"]["time_ms"]
            require([r["cpu_limit_ms"] for r in roles] == [limit, limit, limit + 1000], f"{name}: wrong role budget")
            check_cpu(result, roles, calibrated)
            reports = result.get("work_measurements", [])
            require(bool(reports) == measured, f"{name}: unexpected measurement state")
            if measured:
                require([(r["role"], r["program_id"], r.get("participant_index")) for r in reports] == role_keys and
                        all(r["report"]["complete"] and r["report"]["instructions"] > 0 for r in reports),
                        f"{name}: missing independent work counters")
            if scenario == "complete":
                require(result["status"] == "Accepted" and all(r["started"] and not r.get("interrupted") and
                        r["execution_status"] == "OK" and r.get("exit_code") == 0 and r["cpu_time_ns"] > 0
                        for r in roles), f"{name}: did not complete every role")
                require(result.get("score") == 1 if version == 1 else match.get("wins") == [7, 13],
                        f"{name}: changed native match result")
            elif scenario == "forfeit":
                require(result["status"] == "Accepted" and match.get("outcome") == "forfeit" and
                        match.get("wins") == [0, 20] and roles[0].get("exit_code") == 17 and roles[1]["interrupted"],
                        f"{name}: invalid forfeit attribution")
            elif scenario == "manager-cpu-stop":
                manager = roles[2]
                require(match.get("outcome") == "judge_error" and not match.get("wins") and
                        manager["execution_status"] == "Time Limit Exceeded" and
                        manager.get("cpu_time_ns", 0) > manager["cpu_limit_ms"] * 1000000 and
                        all(r["cpu_time_ns"] < r["cpu_limit_ms"] * 1000000 for r in roles[:2]),
                        f"{name}: manager CPU exhaustion attributed to a player")
            else:
                require(result["status"] == "Time Limit Exceeded" and any(r["execution_status"] == "Time Limit Exceeded"
                        and r.get("cpu_time_ns", 0) > limit * 1000000 for r in roles[:2]),
                        f"{name}: CPU stop not attributed to a participant")
        print(json.dumps({"case": name, "status": result["status"], "outcome": match.get("outcome"),
                          "roles": roles, "normalized": calibrated}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    parser.add_argument("--fixture", type=Path,
                        default=Path(__file__).resolve().parents[1] / "internal/execute/testdata/durante/communication.c")
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a network-none root container")
    with server(args, 18774, False) as compiler:
        binary, source_sha = compile_fixture(compiler, args.fixture)
    token = secrets.token_urlsafe(32)
    for calibrated in (False, True):
        config = {**WORKER, "AONOHAKO_CPU_NORMALIZATION": str(calibrated).lower(),
                  "AONOHAKO_CPU_NORMALIZATION_REFERENCE_MS": "60"}
        with server(args, 18774, False, runner_token=token, operator_env=config) as worker:
            with urllib.request.urlopen(worker + "/capabilities", timeout=5) as caps:
                fingerprint = json.load(caps)["communication_v2"]["runtime_fingerprint"]
            mode = "calibrated" if calibrated else "raw"
            exercise(worker, "embedded-" + mode, token, fingerprint, binary, source_sha, calibrated)
            with server(args, 18775, False, remote=worker, runner_token=token) as controller:
                exercise(controller, "remote-" + mode, None, fingerprint, binary, source_sha, calibrated)
    print(json.dumps({"complete": True}), flush=True)


if __name__ == "__main__":
    main()
