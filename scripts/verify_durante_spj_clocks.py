#!/usr/bin/env python3
"""Run a special judge and its contestant with independent measured clocks."""
import argparse
import json
import os
from pathlib import Path
import secrets

from verify_durante_observation import compile_cpp, post, require, server
from verify_durante_peer_clocks import budgets, profile_rows


PROGRAM = r"""
#include <cstdio>
#include <unistd.h>
int main(int argc, char **argv) {
    const bool checker = argc == 4;
    FILE *input = checker ? fopen(argv[1], "r") : stdin;
    if (!input) return 9;
    const int mode = fgetc(input);
    if ((checker && mode == 'j') || (!checker && mode == 'c'))
        for (volatile unsigned long i = 0; ; i = i + 1) {}
    if ((checker && mode == 'z') || (!checker && mode == 'w')) sleep(20);
    if (!checker) {
        if (mode == 'r') return 7;
        puts("answer");
        return 0;
    }
    FILE *got = fopen(argv[2], "r");
    FILE *expected = fopen(argv[3], "r");
    if (!got || !expected || fgetc(got) != 'a' || fgetc(expected) != 'a') return 8;
    if (mode == 'n') return 1;
    puts(mode == 'x' ? "invalid score" : "0.375");
    return 0;
}
"""


def request(artifacts, mode):
    limits = {"time_ms": 3000, "wall_time_ms": 10000, "memory_mb": 128}
    short = {**limits, "time_ms": 850, "wall_time_ms": 1100}
    return {"lang": "CPP20", "binaries": artifacts, "stdin": mode, "expected_stdout": "answer",
            "limits": short if mode == "w" else limits, "work_measurement": True,
            "spj": {"lang": "CPP20", "binary": artifacts[0], "emit_score": True,
                    "limits": short if mode == "z" else limits},
            "work_measurement_peer_time": budgets([("contestant", "contestant", None, 10),
                                                   ("checker", "checker", None, 10)])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a root network-none container")
    with server(args, 18763, False) as compiler:
        art = compile_cpp(compiler, PROGRAM)
        baseline, _ = post(compiler, "/execute", {"lang": "CPP20", "binaries": art,
            "expected_stdout": "answer", "work_measurement": True, "limits": {"time_ms": 1000, "memory_mb": 128}})
        engine = baseline["work_measurement"]["engine_profile"]
    profiles = list(profile_rows(args, request(art, "f"), engine))
    registry = {"schema": 2, "profiles": [r for i, r in enumerate(profiles) if r not in profiles[:i]],
                "models": [{"model_id": "peer-fixture", "model_version": 1, "reference_id": "integration-only",
                            "coefficients": {"instruction_ns_numerator": 1, "rep_byte_ns_numerator": 1,
                                             "ns_denominator": 1}}]}
    policy = {"AONOHAKO_DURANTE_JUDGE_MODE": "on", "AONOHAKO_DURANTE_JUDGE_FAMILIES": "cpp",
              "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(registry)}
    failed = "Container Initialization Failed"
    cases = [("f", "Accepted", "spj", 0.375), ("n", "Wrong Answer", "spj", 0),
             ("c", "Time Limit Exceeded", "contestant:durante_work_clock", None),
             ("j", failed, "checker:durante_work_clock", None),
             ("w", failed, "contestant:durante_peer_clock_error", None),
             ("z", failed, "checker:durante_peer_clock_error", None),
             ("r", "Runtime Error", "contestant:exit_code", None), ("x", failed, "checker:spj", None)]
    token = secrets.token_urlsafe(32)
    with server(args, 18764, False, runner_token=token, operator_env=policy) as worker:
        with server(args, 18765, False, remote=worker, runner_token=token, operator_env=policy) as controller:
            for url, auth in ((worker, token), (controller, None)):
                for mode, status, source, score in cases:
                    result, _ = post(url, "/execute", request(art, mode), token=auth)
                    rows = result.get("work_measurements", [])
                    print(json.dumps({"case": mode, "url": url, "status": result.get("status"),
                                      "source": result.get("verdict_source"), "score": result.get("score"),
                                      "roles": [(r["role"], r.get("termination_source")) for r in rows]}), flush=True)
                    require((result["status"], result.get("verdict_source"), result.get("score")) == (status, source, score),
                            f"{mode}: {result.get('reason')}")
                    require([r["role"] for r in rows] == ["contestant", "checker"], f"{mode}: role identities")
                    require(all(r["time_budget"]["clock"]["model_binding"] for r in rows), f"{mode}: model bindings")
                    if mode in ("c", "r", "w"):
                        require(rows[1]["time_budget"]["unavailable"] == "role_not_started" and not rows[1].get("report"),
                                f"{mode}: checker was credited after contestant failure")
                    if mode == "f":
                        require(rows[1].get("checker_score") == score, "checker's parsed score missing")


if __name__ == "__main__":
    main()
