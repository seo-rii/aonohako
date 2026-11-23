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


PROGRAM = """#include <cstdio>
#include <unistd.h>
int main() {
    int ch = getchar();
    if (ch == 'w') sleep(30);
    if (ch == 'b') for (volatile unsigned long i = 0; ; i = i + 1) {}
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

    with server(args, 18764, False, operator_env=with_limits(worker_max)) as worker:
        with server(args, 18765, False, remote=worker, operator_env=with_limits(controller_max)) as controller:
            for url, maximum in ((worker, worker_max), (controller, controller_max)):
                run(url, "f", "Accepted", "complete", maximum, omitted=True)
                run(url, "b", "Time Limit Exceeded", "budget_exceeded", maximum, omitted=True)
                run(url, "f", "Accepted", "complete", hard)
                run(url, "w", "Container Initialization Failed", "resource_exhausted",
                    {**hard, "cpu_time_ms": 1000, "wall_time_ms": 1000})
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
