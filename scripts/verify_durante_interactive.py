#!/usr/bin/env python3
"""Verify standalone interactive contestant observations over the offline HTTP API.

Use the server, Durante bundle and runtime image documented in
verify_durante_observation.py. Mount this script and its two helpers together.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets

from verify_durante_observation import compile_cpp, post, require, server
from verify_durante_pipeline import (
    INTERACTIVE_PROGRAM, LIMITS, PARTICIPANT_MARKER, expect_marker_reads, three_round_interactor,
)


def check_receipt(receipt, state, client_sha):
    if state is None:
        require(receipt is None, "disabled observation returned a receipt")
        return None
    require(receipt and receipt.get("version") == 2 and receipt.get("authority") == "diagnostic_only" and
            receipt.get("state") == "interactive" and not receipt.get("report"), "missing interactive receipt")
    rows = receipt["steps"]
    require(len(rows) == 1 and rows[0]["step_id"] == rows[0]["program_id"] == "contestant" and
            rows[0]["state"] == state, "unexpected contestant observation identity or outcome")
    require("PRIVATE_PIPE_" not in json.dumps(receipt), "interactive receipt contained conversation bytes")
    row = rows[0]
    if state == "unavailable":
        require(row.get("reason") and not row.get("report") and not row.get("output_check"),
                "unavailable observation contained unchecked evidence")
        return None
    report = row["report"]
    require(row["output_check"] == "interactive_transcript" and not row.get("reason") and
            report["complete"] and report["family"] == "cpp" and report["instructions"] > 0 and
            report["cpuid_profile"] == "x86-64-v2" and report["installation"]["client_sha256"] == client_sha,
            "incomplete or incompatible contestant evidence")
    return report["instructions"]


def execute(url, name, binary, mode, state, client_sha, *, sidecar=False):
    replays = state is not None and not sidecar and mode != "grace"
    with expect_marker_reads(1 + int(replays), PARTICIPANT_MARKER), expect_marker_reads() as marker:
        payload = {"lang": "CPP20", "binaries": binary, "stdin": mode + "\n", "limits": LIMITS, "emit_logs": True,
                   "interactor": {"lang": "PYTHON3", "binaries": [three_round_interactor(marker)], "limits": LIMITS}}
        if sidecar:
            payload["sidecar_outputs"] = [{"path": "handoff.txt"}]
        result, stdout = post(url, "/execute", payload)
    rejected = mode == "judge-wa"
    status = "Wrong Answer" if rejected else "Accepted"
    source = "interactor:exit_code" if rejected else "interactor"
    require(result["status"] == status and result["verdict_source"] == source and
            result["score"] == int(not rejected), f"{name}: native judge verdict changed")
    roles = result["steps"]
    require([(r["id"], r["program_id"]) for r in roles] == [("contestant", "contestant"), ("interactor", "interactor")],
            f"{name}: incorrect native roles")
    participant, interactor = roles
    require(interactor["status"] == status and interactor["exit_code"] == int(rejected) and
            (participant["status"] == "Accepted" and participant.get("exit_code") == 0) == (mode != "grace"),
            f"{name}: unexpected native role status")
    require(all(r["cpu_time_ns"] > 0 for r in roles) and
            sum(r["cpu_time_ns"] for r in roles) == result["cpu_time_ns"] and
            sum(r["cpu_time_ms"] for r in roles) == result["cpu_time_ms"], f"{name}: native CPU totals differ")
    require(stdout.count("PRIVATE_PIPE_REPLY ") == 3 and stdout.count("PRIVATE_PIPE_DONE\n") == 1 and
            stdout == participant["stdout"], f"{name}: expected one native conversation in the public log")
    if sidecar:
        output, = result["sidecar_outputs"]
        require(output["path"] == "handoff.txt" and
                base64.b64decode(output["data_b64"], validate=True) == b"PRIVATE_PIPE_FILE 22 0\n",
                f"{name}: native sidecar changed")
    require(not {"work_measurement", "work_measurements", "work_measurement_execution"} & result.keys(),
            f"{name}: an instrumented run replaced native judging")
    count = check_receipt(result.get("work_measurement_observation"), state, client_sha)
    print(json.dumps({"case": name, "verdict": status, "source": source, "score": result["score"], "state": state,
                      "native_role_cpu_ns": {r["id"]: r["cpu_time_ns"] for r in roles},
                      "native_cpu_ns": result["cpu_time_ns"], "instructions": count,
                      "contestant_invocations": 1 + int(replays), "interactor_invocations": 1}), flush=True)


def exercise(url, transport, binary, client_sha):
    for mode, state in (("stable", "complete"), ("judge-wa", "complete"),
                        ("vary-reply", "unavailable"), ("grace", "unavailable")):
        execute(url, f"{transport}-{mode}", binary, mode, state, client_sha)
    execute(url, transport + "-native-sidecar", binary, "stable", "unavailable", client_sha, sidecar=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a network-none root container")
    client_sha = hashlib.sha256(Path("/opt/durante/lib/libdurante.so").read_bytes()).hexdigest()
    with server(args, 18770, False) as off:
        source = f"#define INVOCATION_MARKER {json.dumps(str(PARTICIPANT_MARKER))}\n" + INTERACTIVE_PROGRAM
        binary = compile_cpp(off, source)
        execute(off, "embedded-disabled", binary, "stable", None, client_sha)
    with server(args, 18770, True) as worker:
        exercise(worker, "embedded", binary, client_sha)
    token = secrets.token_urlsafe(32)
    with server(args, 18770, True, runner_token=token) as worker:
        with server(args, 18771, False, remote=worker, runner_token=token) as control:
            execute(control, "remote-disabled", binary, "stable", None, client_sha)
        with server(args, 18771, True, remote=worker, runner_token=token) as control:
            exercise(control, "remote", binary, client_sha)
    print(json.dumps({"complete": True}), flush=True)


if __name__ == "__main__":
    main()
