#!/usr/bin/env python3
"""Qualify staged Durante clocks on embedded HTTP and an authenticated remote worker."""
import argparse
import json
import os
from pathlib import Path
import secrets
import urllib.request

from verify_durante_legacy_steps import PROGRAM as LEGACY_PROGRAM, request as legacy_request
from verify_durante_observation import compile_cpp, post, require, server
from verify_durante_peer_clocks import INTERACTIVE, profile_rows
from verify_durante_pipeline import LIMITS, PROGRAM as PIPELINE_PROGRAM, make_pipeline, resource


# A successful interactive stage publishes the same two artifacts that the
# ordinary pipeline consumer verifies. Both roles still synchronize on EOF.
INTERACTIVE_STAGE = "#define main run_peer\n" + INTERACTIVE + r"""
#undef main
int main(int argc, char **argv) {
    int rc = run_peer(argc, argv);
    if (rc) return rc;
    FILE *out = fopen(argc == 1 ? "handoff.txt" : argv[2], "w");
    if (!out) return 10;
    fputs(argc == 1 ? "PRIVATE_PIPE_FILE 22 0\n" : "PRIVATE_PIPE_STDOUT 20 0\n", out);
    return fclose(out) != 0;
}
"""
CHECKER = r"""#include <cstdio>
int main(int argc, char **argv) {
    if (argc != 4) return 7;
    FILE *actual = fopen(argv[2], "rb"), *answer = fopen(argv[3], "rb");
    if (!actual || !answer) return 8;
    int c;
    do { c = fgetc(actual); if (fgetc(answer) != c) return 1; } while (c != EOF);
    puts("0.375");
    return 0;
}
"""
BUSY = "int main() { for (volatile unsigned long i = 0; ; i = i + 1) {} }"
WAIT = "#include <unistd.h>\nint main() { sleep(20); }"
FAIL = "Container Initialization Failed"
TLE = "Time Limit Exceeded"


def stage_request(kind, mode, art):
    is_pipeline = kind == "pipeline"
    req = make_pipeline(art[kind]) if is_pipeline else legacy_request(art[kind])
    stages = req["pipeline"] if is_pipeline else req
    programs, steps = stages["programs"], stages["steps"]
    roles = [{"step_id": "write", "role": "contestant", "program_id": "producer", "time_ms": 10}]
    if mode.startswith("interactive-"):
        guest_mode = {"interactive-ok": "f", "interactive-contestant": "c", "interactive-interactor": "j"}[mode]
        stages["resources"]["input"] = resource(guest_mode)
        programs[0]["binaries"] = art["interactive"]
        programs.append({"id": "judge", "lang": "CPP20", "binaries": art["interactive"]})
        steps[0]["executor"] = {"kind": "interactive", "participant_program_id": "producer",
                                "interactor_program_id": "judge", "interactor_limits": LIMITS}
        steps[0]["outputs"][0]["source"] = {"kind": "interactor_output"}
        roles.append({"step_id": "write", "role": "interactor", "program_id": "judge", "time_ms": 15})
    if mode in ("first", "wait"):
        programs[0]["binaries"] = art["wait" if mode == "wait" else "busy"]
        if mode == "wait":
            steps[0]["limits"] = {**LIMITS, "time_ms": 850, "wall_time_ms": 1100}
    if mode == "second":
        programs[1]["binaries"] = art["busy"]
    roles.append({"step_id": "read", "role": "contestant", "program_id": "consumer", "time_ms": 20})
    if mode != "diff":
        checker = {"lang": "CPP20", "binary": art["busy" if mode == "checker" else "checker"][0],
                   "limits": LIMITS, "emit_score": True}
        if is_pipeline:
            stages["final_judge"].update(kind="spj", spj=checker)
        else:
            req["spj"] = checker
        roles.append({"role": "checker", "program_id": "checker", "time_ms": 30})
    req.update(work_measurement=True, work_measurement_peer_time={"version": 2, "budgets": roles})
    return req


def execute(url, req, token):
    if "pipeline" not in req:
        return post(url, "/execute", req, token=token)[0]
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url + "/execute", data=json.dumps(req).encode(), headers=headers)
    with urllib.request.urlopen(request, timeout=75) as response:
        require(response.headers.get_content_type() == "text/event-stream", "missing pipeline stream")
        body = response.read(8 << 20).decode()
    require("PRIVATE_PIPE_" not in body, "private artifact or protocol message reached the public stream")
    events = []
    for frame in body.split("\n\n"):
        lines = frame.splitlines()
        if len(lines) == 2 and lines[0].startswith("event: ") and lines[1].startswith("data: "):
            events.append((lines[0][7:], json.loads(lines[1][6:])))
    names = [name for name, _ in events]
    require(names.count("result") == 1 and names[-1] == "result" and not {"log", "image"}.intersection(names),
            "unexpected pipeline stream events")
    result = events[-1][1]
    require(not result.get("sidecar_outputs") and all(not row.get("stdout") and not row.get("stderr")
            for row in [result, *result.get("steps", [])]), "private pipeline output was returned")
    return result


def verify(kind, mode, req, result):
    label = f"{kind}-{mode}"
    expected = req["work_measurement_peer_time"]["budgets"]
    rows = result.get("work_measurements", [])
    steps = result.get("steps", [])
    identities = lambda roles: [(r.get("step_id"), r["role"], r["program_id"]) for r in roles]
    require(identities(rows) == identities(expected),
            f"{label}: wrong role order or missing receipts ({result.get('reason')})")
    first_failed = mode in ("first", "wait", "interactive-contestant", "interactive-interactor")
    count = 1 if first_failed else 2
    require([(s["id"], s["program_id"]) for s in steps] == [("write", "producer"), ("read", "consumer")][:count],
            f"{label}: incorrect executed prefix")
    require((steps[0].get("handoff_bytes", 0) > 0) == (not first_failed), f"{label}: incorrect handoff")
    ended = mode in ("diff", "spj", "interactive-ok")
    if ended:
        source = ("final:" if kind == "pipeline" else "step:read:") + (
            ("diff" if kind == "pipeline" else "stdout") if mode == "diff" else "spj")
        verdict = ("Accepted", source, None if mode == "diff" else 0.375)
    else:
        source = {"first": "step:write:durante_work_clock", "second": "step:read:durante_work_clock",
                  "wait": "step:write:durante_peer_clock_error",
                  "checker": ("final:" if kind == "pipeline" else "step:read:") + "checker:durante_work_clock",
                  "interactive-contestant": "step:write:contestant:durante_work_clock",
                  "interactive-interactor": "step:write:interactor:durante_work_clock"}[mode]
        verdict = (TLE if mode in ("first", "second", "interactive-contestant") else FAIL, source, None)
    require((result["status"], result.get("verdict_source"), result.get("score")) == verdict,
            f"{label}: unexpected verdict {result['status']} {result.get('verdict_source')} "
            f"score={result.get('score')} {result.get('reason')}")
    crossed = []
    for row, requested in zip(rows, expected):
        b = row["time_budget"]
        clock = b["clock"]
        require(b["version"] == 2 and b["budget_ms"] == requested["time_ms"] and clock["model_binding"] and
                clock["budget"] == requested["time_ms"] * 1_000_000,
                f"{label}: role budget or model mismatch")
        if clock.get("progress"):
            progress = clock["progress"]
            ns = sum(progress[key] for key in ("instructions", "movs_bytes", "stos_bytes"))
            require(b["exceeded"] == (ns > clock["budget"]), f"{label}: test model disagrees with role cutoff")
        step, role = row.get("step_id"), row["role"]
        unstarted = (step == "read" and first_failed or role == "checker" and not ended and mode != "checker")
        if unstarted:
            require(row["execution_status"] == FAIL and row.get("termination_source") == "stage_not_started" and
                    b.get("unavailable") == "role_not_started" and b["clock"]["state"] == "unavailable" and
                    all(b.get(k) is None for k in ("raw_usage", "estimate", "exceeded")) and
                    not b["clock"].get("progress") and not row.get("report") and row.get("exit_code") is None,
                    f"{label}: unstarted {step}/{role} credited")
        elif b.get("exceeded") is True:
            crossed.append((step, role))
            require(row["execution_status"] == TLE and row["termination_source"] == "durante_work_clock" and
                    b["clock"].get("progress") and not b.get("estimate") and
                    not (row.get("report") or {}).get("complete"), f"{label}: live cutoff lacks role evidence")
        elif mode == "wait" and step == "write":
            require(b["raw_source"] == "wall_time" and b.get("unavailable") and not b.get("estimate") and
                    b.get("exceeded") is None, f"{label}: raw waiting time was credited")
        elif first_failed and mode.startswith("interactive-") and step == "write":
            require(b.get("exceeded") is not True, f"{label}: the sibling also exceeded")
        else:
            require(row.get("exit_code") == 0 and b.get("exceeded") is False and b.get("estimate") and
                    row["report"]["complete"] and row["report"]["instructions"] > 0, f"{label}: no terminal measurement")
    decisive = {"first": ("write", "contestant"), "second": ("read", "contestant"), "checker": (None, "checker"),
                "interactive-contestant": ("write", "contestant"), "interactive-interactor": ("write", "interactor")}
    require(crossed == ([decisive[mode]] if mode in decisive else []), f"{label}: incorrect clock attribution")
    if ended and mode != "diff":
        require(rows[-1].get("checker_score") == 0.375, f"{label}: checker score evidence missing")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a root network-none container")
    with server(args, 18763, False) as compiler:
        sources = {"pipeline": PIPELINE_PROGRAM, "legacy": LEGACY_PROGRAM, "interactive": INTERACTIVE_STAGE,
                   "checker": CHECKER, "busy": BUSY, "wait": WAIT}
        art = {name: compile_cpp(compiler, code) for name, code in sources.items()}
        baseline, _ = post(compiler, "/execute", {"lang": "CPP20", "binaries": art["legacy"], "stdin": "P stable\n",
                            "work_measurement": True, "limits": LIMITS})
        engine = baseline["work_measurement"]["engine_profile"]
    cases, profiles = [], []
    for kind in ("pipeline", "legacy"):
        modes = ["diff", "spj", "first", "second", "wait", "checker"]
        if kind == "pipeline":
            modes += ["interactive-ok", "interactive-contestant", "interactive-interactor"]
        for mode in modes:
            req = stage_request(kind, mode, art)
            cases.append((kind, mode, req))
            for row in profile_rows(args, req, engine):
                if row not in profiles:
                    profiles.append(row)
    registry = {"schema": 2, "profiles": profiles, "models": [{"model_id": "peer-fixture", "model_version": 1,
                "reference_id": "integration-only", "coefficients": {"instruction_ns_numerator": 1,
                "rep_byte_ns_numerator": 1, "ns_denominator": 1}}]}
    policy = {"AONOHAKO_DURANTE_JUDGE_MODE": "on", "AONOHAKO_DURANTE_JUDGE_FAMILIES": "cpp",
              "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(registry)}
    token = secrets.token_urlsafe(32)
    with server(args, 18764, False, runner_token=token, operator_env=policy) as worker:
        with server(args, 18765, False, remote=worker, runner_token=token, operator_env=policy) as controller:
            for transport, url, auth in (("direct", worker, token), ("remote", controller, None)):
                for kind, mode, req in cases:
                    result = execute(url, req, auth)
                    print(json.dumps({"transport": transport, "case": f"{kind}-{mode}", "status": result["status"],
                                      "source": result.get("verdict_source")}), flush=True)
                    verify(kind, mode, req, result)
    print(json.dumps({"complete": True, "cases": 2 * len(cases)}), flush=True)


if __name__ == "__main__":
    main()
