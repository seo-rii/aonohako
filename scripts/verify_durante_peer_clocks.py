#!/usr/bin/env python3
"""Exercise interactive and communication role clocks through the HTTP API."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import urllib.request

from verify_durante_communication import WORKER, compile_fixture, request as communication_request
from verify_durante_observation import check_clock_receipt, compile_cpp, post, require, server


# The same executable plays either side; its argc distinguishes the trusted
# interactor from the contestant. Synchronize on EOF so a normal exchange
# finishes both processes and has two terminal measurements.
INTERACTIVE = """#include <cstdio>
#include <unistd.h>
void busy() { for (volatile unsigned long i=0;;i=i+1) {} }
int main(int argc, char **argv) {
    if (argc == 1) {
        int mode = getchar();
        if (mode == 'c') busy();
        if (mode == 'w') sleep(20);
        return puts("ok") < 0;
    }
    FILE *input = fopen(argv[1], "r");
    if (!input) return 7;
    int mode = fgetc(input);
    fclose(input);
    if (mode == 'j') busy();
    putchar(mode); fflush(stdout);
    char line[16];
    if (!fgets(line, sizeof(line), stdin)) return 8;
    return getchar() == EOF ? 0 : 9;
}
"""


def budgets(roles):
    return {"version": 2, "budgets": [
        {"role": role, "program_id": program, "time_ms": limit,
         **({"participant_index": index} if index is not None else {})}
        for role, program, index, limit in roles]}


def interactive(binary, mode):
    limits = {"time_ms": 3000, "wall_time_ms": 10000, "memory_mb": 128}
    guest_limits = limits if mode != "w" else {**limits, "time_ms": 800, "wall_time_ms": 1000}
    return {"lang": "CPP20", "binaries": binary, "work_measurement": True, "limits": guest_limits,
            "stdin": mode, "interactor": {"lang": "CPP20", "binaries": binary, "limits": limits},
            "work_measurement_peer_time": budgets([("contestant", "contestant", None, 2),
                                                   ("interactor", "interactor", None, 2)])}


def communication(art, source_sha, fingerprint, protocol, mode):
    req = communication_request(protocol, art, source_sha, fingerprint, mode, True)
    req["limits"].update(time_ms=4000, memory_mb=128)
    spec = req["communication"]
    ids = spec.get("participant_program_ids", [spec.get("participant_program_id")] * 2)
    req["work_measurement_peer_time"] = budgets([("participant", p, i, 3 if mode == "cpu-stop" and i == 0 else 1000)
        for i, p in enumerate(ids)] + [("manager", "manager", None, 3 if mode == "manager-cpu-stop" else 200)])
    return req


def profile_rows(args, req, engine):
    env = {"PATH": os.defpath, "AONOHAKO_INBOUND_AUTH": "none", "AONOHAKO_DEPLOYMENT_TARGET": "dev",
           "AONOHAKO_DURANTE_FAMILIES": "cpp", "AONOHAKO_DURANTE_CPUID_PROFILE": "x86-64-v2",
           "AONOHAKO_EXECUTION_TRANSPORT": "embedded", "AONOHAKO_SANDBOX_BACKEND": "helper",
           "AONOHAKO_DURANTE_RUNTIME_IMAGE": args.runtime_image,
           "AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT": args.runtime_image}
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "request.json"
        path.write_text(json.dumps(req))
        output = subprocess.check_output([args.reference, "describe-runtime-options", "--request", path], env=env)
    for row in json.loads(output)["runtime_options"]:
        yield {"source_language": row["source_language"], "family": "cpp", "runtime_image": args.runtime_image,
               "engine_profile": engine, "dispatch_profile": "default", "cpuid_profile": "x86-64-v2",
               "model_id": "peer-fixture", "model_version": 1, "qualification_id": "integration-only",
               "runtime_options": row["runtime_options"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--no-latency-bound", dest="latency_bound", action="store_false",
                        help="exercise the ordinary instruction and REP clock without static latency accounting")
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a root network-none container")
    with server(args, 18763, False) as compiler:
        pair = compile_cpp(compiler, INTERACTIVE)
        art, src = compile_fixture(compiler, args.fixture)
        report, _ = post(compiler, "/execute", {"lang": "CPP20", "binaries": pair, "stdin": "x",
            "expected_stdout": "ok\n", "work_measurement": True, "limits": {"time_ms": 1000, "memory_mb": 128},
            "work_measurement_limits": {"cpu_time_ms": 3000, "wall_time_ms": 9000, "memory_mb": 128}})
        check_clock_receipt(report["work_measurement"], args.latency_bound, "initial measurement")
        engine = report["work_measurement"]["engine_profile"]
    with server(args, 18764, False, runner_token=secrets.token_urlsafe(), operator_env=WORKER) as worker:
        with urllib.request.urlopen(worker + "/capabilities", timeout=5) as caps:
            fingerprint = json.load(caps)["communication_v2"]["runtime_fingerprint"]
    cases = [(f"interactive-{m}", interactive(pair, m)) for m in ("f", "c", "j", "w")]
    for proto in (1, 2):
        for mode in ("complete", "manager-cpu-stop", "cpu-stop"):
            cases.append((f"communication-{proto}-{mode}", communication(art, src, fingerprint, proto, mode)))
    profiles = []
    for _, req in cases:
        for row in profile_rows(args, req, engine):
            if row not in profiles:
                profiles.append(row)
    registry = {"schema": 2, "models": [{"model_id": "peer-fixture", "model_version": 1,
        "reference_id": "integration-only", "coefficients": {"instruction_ns_numerator": 1,
        "rep_byte_ns_numerator": 1, "ns_denominator": 1}}], "profiles": profiles}
    policy = {"AONOHAKO_DURANTE_JUDGE_FAMILIES": "cpp", "AONOHAKO_DURANTE_JUDGE_MODE": "on",
              "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(registry)}
    token = secrets.token_urlsafe(32)
    with server(args, 18764, False, runner_token=token, operator_env={**WORKER, **policy}) as worker:
        with server(args, 18765, False, remote=worker, runner_token=token, operator_env=policy) as control:
            for url, auth in ((worker, token), (control, None)):
                for name, req in cases:
                    result, _ = post(url, "/execute", req, token=auth)
                    print(json.dumps({"case": name, "url": url, "result": result}), flush=True)
                    rows = result.get("work_measurements", [])
                    require(len(rows) == len(req["work_measurement_peer_time"]["budgets"]),
                            f"{name}: missing role receipts ({result.get('reason')})")
                    completed_case = name.endswith("complete") or name == "interactive-f"
                    for row in rows:
                        b = row["time_budget"]
                        require(b["version"] == 2 and b["clock"]["model_binding"], f"{name}: role clock binding")
                        report = row.get("report") or {}
                        if completed_case or report.get("complete"):
                            check_clock_receipt(report, args.latency_bound, f"{name}: {row['role']}")
                    crossed = [r["role"] for r in rows if r.get("termination_source") == "durante_work_clock"]
                    if completed_case:
                        require(result["status"] == "Accepted" and not crossed, f"{name}: completion")
                    elif name == "interactive-c":
                        require(result["status"] == "Time Limit Exceeded" and crossed == ["contestant"],
                                f"{name}: virtual TLE attribution")
                    elif name.startswith("communication-") and name.endswith("-cpu-stop") and "manager" not in name:
                        seat = rows[0]
                        require(crossed == ["participant"] and seat["participant_index"] == 0 and
                                seat["termination_source"] == "durante_work_clock", f"{name}: incorrect seat")
                        if req["communication"]["version"] == 2:
                            require(result["match"]["outcome"] == "forfeit" and result["match"]["wins"] == [0, 20],
                                    f"{name}: wrong forfeit outcome")
                        else:
                            require(result["status"] == "Time Limit Exceeded", f"{name}: missing participant TLE")
                    elif name == "interactive-w":
                        require(not crossed and result.get("score") is None and
                                result["status"] == "Container Initialization Failed", f"{name}: raw wait credited")
                    else:
                        role = "interactor" if name == "interactive-j" else "manager"
                        require(crossed == [role] and result.get("score") is None and
                                result["status"] != "Time Limit Exceeded", f"{name}: helper work charged to contestant")


if __name__ == "__main__":
    main()
