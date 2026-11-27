#!/usr/bin/env python3
"""Verify model-bound work-clock grading through embedded and forwarded HTTP."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.error

from verify_durante_observation import RESOURCE_POLICY, check_clock_receipt, compile_cpp, post, require, server


PROGRAM = """#include <csignal>
#include <cstdio>
#include <sys/time.h>
#include <unistd.h>
volatile sig_atomic_t delivered;
void handled(int) { delivered = 1; }
int main() {
    int ch = getchar();
    if (ch == 'w') sleep(30);
    if (ch == 'b') for (volatile unsigned long i = 0; ; i = i + 1) {}
    if (ch == 's' || ch == 'c') {
        if (ch == 's') {
            itimerval timer = {{0, 0}, {0, 10000}};
            if (signal(SIGALRM, handled) == SIG_ERR || setitimer(ITIMER_REAL, &timer, nullptr) != 0) return 1;
            while (!delivered) usleep(10000);
        } else {
            char byte = 0;
            char *lhs = &byte, *rhs = &byte;
            unsigned long count = 1;
            asm volatile("repe cmpsb" : "+S"(lhs), "+D"(rhs), "+c"(count) :: "cc", "memory");
        }
        // Cross the synthetic budget after the observation becomes ineligible.
        for (volatile unsigned long i = 0; i < 4000000; i = i + 1) {}
        if (getchar() == 'w') sleep(30);
    }
    puts("ok");
}
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    parser.add_argument("--no-latency-bound", dest="latency_bound", action="store_false",
                        help="exercise the ordinary instruction and REP clock without static latency accounting")
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a network-none validation container")
    hard = {"cpu_time_ms": 5000, "wall_time_ms": 12000, "memory_mb": 256}
    with server(args, 18763, False) as compiler:
        artifacts = compile_cpp(compiler, PROGRAM)
        template = {"lang": "CPP20", "binaries": artifacts, "stdin": "f", "expected_stdout": "ok\n",
                    "limits": {"time_ms": 3, "wall_time_ms": 50, "memory_mb": 64},
                    "work_measurement": True, "work_measurement_limits": hard}
        response, _ = post(compiler, "/execute", template)
        report = response["work_measurement"]
        check_clock_receipt(report, args.latency_bound, "initial measurement")
    with tempfile.TemporaryDirectory() as folder:
        reqfile = Path(folder) / "request.json"
        reqfile.write_text(json.dumps(template))
        env = {**os.environ, "AONOHAKO_INBOUND_AUTH": "none", "AONOHAKO_DEPLOYMENT_TARGET": "dev",
               "AONOHAKO_EXECUTION_TRANSPORT": "embedded", "AONOHAKO_SANDBOX_BACKEND": "helper",
               "AONOHAKO_DURANTE_FAMILIES": "cpp", "AONOHAKO_DURANTE_CPUID_PROFILE": "x86-64-v2",
               "AONOHAKO_DURANTE_RUNTIME_IMAGE": args.runtime_image,
               "AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT": args.runtime_image}
        description = subprocess.check_output([args.reference, "describe-runtime-options", "--request", reqfile], env=env)
        options = json.loads(description)["runtime_options"][0]["runtime_options"]

    # Deliberately synthetic coefficients distinguish judge routing; this
    # fixture supplies no qualification for a native-time model.
    model = {"model_id": "work-clock-fixture", "model_version": 1, "reference_id": "integration",
             "method": "weighted-rep-operations-nanoseconds-v2",
             "coefficients": {"instruction_ns_numerator": 1, "movs_byte_ns_numerator": 2,
                              "stos_byte_ns_numerator": 3, "ns_denominator": 1}}
    binding = {"source_language": "CPP20", "family": "cpp", "runtime_image": args.runtime_image,
               "engine_profile": report["engine_profile"], "dispatch_profile": report["dispatch_profile"],
               "cpuid_profile": "x86-64-v2", "model_id": model["model_id"], "model_version": 1,
               "qualification_id": "integration-only", "runtime_options": options}
    registry = {"schema": 2, "models": [model], "profiles": [binding]}
    operator = {"AONOHAKO_DURANTE_JUDGE_MODE": "on", "AONOHAKO_DURANTE_JUDGE_FAMILIES": "cpp",
                "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(registry)}

    worker_max = {**hard, "cpu_time_ms": 12000, "wall_time_ms": 30000}
    controller_max = {**hard, "cpu_time_ms": 9000, "wall_time_ms": 20000}

    def with_limits(maximum):
        return {**operator, "AONOHAKO_DURANTE_RESOURCE_POLICY": json.dumps({**RESOURCE_POLICY, "max_limits": maximum})}

    def run(url, mode, status, state, raw, *, omitted=False):
        request = {**template, "stdin": mode, "work_measurement_judge": True,
                   "work_measurement_time": True, "work_measurement_limits": raw}
        if omitted:
            del request["work_measurement_limits"]
        result, _ = post(url, "/execute", request)
        print(json.dumps({"case": mode, "url": url, "result": result}), flush=True)
        require(result.get("status") == status and result["work_measurement_judgment"]["state"] == state,
                f"{mode}: {result.get('reason')}")
        envelope = result["work_measurement_execution"]
        require(envelope["raw_limits"] == raw, f"{mode}: wrong raw watchdog envelope")
        clock = envelope["clock"]
        require(clock["budget"] == 3_000_000 and clock["model_binding"] and
                clock["metric"] == "model_weighted_nanoseconds_v1", f"{mode}: model binding")
        if mode == "b":
            progress = clock["progress"]
            weighted = progress["instructions"] + 2*progress["movs_bytes"] + 3*progress["stos_bytes"]
            require(weighted > clock["budget"] and envelope["termination_source"] == "durante_work_clock" and
                    not result["work_measurement"]["complete"] and not envelope.get("time_estimate") and
                    result["wall_time_ms"] < raw["wall_time_ms"], "busy loop did not stop on live virtual work")
        elif mode == "f":
            require(result["score"] == 1, "unexpected score for completed work")
            check_clock_receipt(result["work_measurement"], args.latency_bound, "completed work")
        else:
            require("wall" in envelope["termination_source"] and not result.get("score"),
                    "idle watchdog misattributed to measured work")

    def check_eligibility(url, mode, quality_field):
        event = {**template, "stdin": mode}
        # A generous raw budget lets this same workload finish. Check the
        # event occurred and that the remaining work crosses the test budget.
        budget = 3_000_000
        raw = {**event, "work_measurement_clock": {"version": 1, "budget": 1_000_000_000}}
        natural, _ = post(url, "/execute", raw)
        measured = natural["work_measurement"]
        envelope = natural["work_measurement_execution"]
        require(envelope["state"] == "complete" and measured["complete"] and measured[quality_field] > 0,
                f"{mode}: missing natural raw observation of {quality_field}: {natural}")
        progress = envelope["clock"]["progress"]
        work_ns = progress["instructions"] + 2*progress["movs_bytes"] + 3*progress["stos_bytes"]
        require(budget < work_ns < 500_000_000, f"{mode}: workload outside test budgets")

        raw["work_measurement_clock"]["budget"] = budget
        crossing, _ = post(url, "/execute", raw)
        envelope = crossing["work_measurement_execution"]
        require(envelope["state"] == "budget_exceeded" and envelope["termination_source"] in
                ("durante_work_clock", "durante_work_clock_final") and envelope["clock"]["state"] == "observed",
                f"{mode}: raw budget lost an observed crossing")
        judged = {**event, "work_measurement_judge": True, "work_measurement_time": True}
        for limit_ms in (3, 500):
            limits = {**template["limits"], "time_ms": limit_ms, "wall_time_ms": max(50, limit_ms)}
            weighted, _ = post(url, "/execute", {**judged, "limits": limits})
            envelope = weighted["work_measurement_execution"]
            require(weighted["status"] == "Container Initialization Failed" and weighted.get("exit_code") == 0 and
                    envelope["termination_source"] == "durante_work_clock_final_unavailable" and
                    envelope["state"] == "incomplete" and envelope["clock"]["state"] == "unavailable" and
                    envelope["clock"]["budget"] == limit_ms * 1_000_000 and not envelope["clock"].get("progress") and
                    not envelope.get("time_estimate") and weighted.get("score") is None,
                    f"{mode}: unexpected weighted result at {limit_ms} ms: {weighted}")

        waiting, _ = post(url, "/execute", {**judged, "stdin": mode + "w",
                            "work_measurement_limits": {**hard, "wall_time_ms": 2000, "cpu_time_ms": 1500}})
        envelope = waiting["work_measurement_execution"]
        require(waiting["status"] == "Container Initialization Failed" and envelope["state"] == "resource_exhausted"
                and "wall" in envelope["termination_source"] and envelope["clock"]["state"] == "unavailable"
                and waiting.get("score") is None, f"{mode}: external watchdog failed: {waiting}")
        print(json.dumps({"case": mode, "url": url, "observed": measured[quality_field],
                          "raw_crossing": crossing["work_measurement_execution"]["termination_source"],
                          "weighted_limits_ms": [3, 500], "watchdog": envelope["termination_source"]}), flush=True)

    with server(args, 18764, False, operator_env=with_limits(worker_max)) as worker:
        with server(args, 18765, False, remote=worker, operator_env=with_limits(controller_max)) as controller:
            for url, maximum in ((worker, worker_max), (controller, controller_max)):
                run(url, "f", "Accepted", "complete", maximum, omitted=True)
                run(url, "b", "Time Limit Exceeded", "budget_exceeded", maximum, omitted=True)
                run(url, "f", "Accepted", "complete", hard)
                run(url, "w", "Container Initialization Failed", "resource_exhausted",
                    {**hard, "cpu_time_ms": 1000, "wall_time_ms": 1000})
                if report["engine_profile"] == "dynamorio-11.3-durante-v12-vm32m":
                    for mode, quality_field in (("s", "signals_observed"), ("c", "unsupported_rep_entries")):
                        check_eligibility(url, mode, quality_field)
                for omitted in (True, False):
                    nominal = (maximum if omitted else hard)["cpu_time_ms"]
                    request = {**template, "work_measurement_judge": True, "work_measurement_time": True,
                               "limits": {**template["limits"], "time_ms": nominal, "wall_time_ms": nominal}}
                    if omitted:
                        del request["work_measurement_limits"]
                    try:
                        post(url, "/execute", request)
                    except urllib.error.HTTPError as error:
                        require(error.code == 400 and "greater than nominal" in json.load(error)["message"],
                                "wrong HTTP admission error for tight raw watchdog")
                    else:
                        raise RuntimeError("admitted a raw watchdog equal to the nominal clock")
        model["coefficients"]["instruction_ns_numerator"] += 1
        other = {**operator, "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(registry)}
        with server(args, 18765, False, remote=worker, operator_env=other) as controller:
            result, _ = post(controller, "/execute", {**template, "work_measurement_judge": True, "work_measurement_time": True})
            require("work_measurement_judge_clock_binding" in result.get("reason", "") and
                    "work_measurement_execution" not in result, "mismatched worker model admitted")


if __name__ == "__main__":
    main()
