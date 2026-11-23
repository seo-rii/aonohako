#!/usr/bin/env python3
"""Qualify legacy two-step Durante observations in the offline HTTP validation image.

Run beside verify_durante_pipeline.py and verify_durante_observation.py with
the same read-only server, Durante bundle, image and tmpfs mounts.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets

from verify_durante_observation import artifact, compile_cpp, post, require, server
from verify_durante_pipeline import LIMITS, MARKER, expect_marker_reads


# Both stages consume real binary data. In vary-file and vary-stdout modes
# only one producer channel changes across runs while the final answer stays 42.
PROGRAM = r"""
#include <cstdio>
#include <cstring>
#include <ctime>
int main() {
#ifdef INVOCATION_MARKER
    FILE *marker = fopen(INVOCATION_MARKER, "rb");
    if (!marker || fgetc(marker) == EOF || fclose(marker) != 0) return 8;
#endif
    int tag = getchar();
    if (tag == 'P') {
        char mode[32];
        if (scanf("%31s", mode) != 1 || !strcmp(mode, "fail")) return 2;
        timespec ts{};
        if (clock_gettime(CLOCK_MONOTONIC, &ts)) return 3;
        long long now = ts.tv_sec * 1000000000LL + ts.tv_nsec;
        long long file_time = !strcmp(mode, "vary-file") ? now : 0;
        FILE *handoff = !strcmp(mode, "stdout") ? stdout : fopen("handoff.bin", "wb");
        unsigned char header[] = {0, 255, 42};
        if (!handoff || fwrite(header, 1, 3, handoff) != 3 ||
            fwrite(&file_time, sizeof(file_time), 1, handoff) != 1) return 4;
        if (handoff != stdout) {
            if (fclose(handoff)) return 5;
            printf("LEGACY_PRODUCER %lld\n", !strcmp(mode, "vary-stdout") ? now : 0);
        }
    } else {
        bool wrapped = (tag == '<');
        if ((wrapped ? getchar() : tag) != 0 || getchar() != 255 || getchar() != 42) return 6;
        long long stamp;
        if (fread(&stamp, sizeof(stamp), 1, stdin) != 1 ||
            (wrapped && getchar() != '>') || getchar() != EOF) return 7;
        puts("42");
    }
}
"""
ORDER = [("write", "producer"), ("read", "consumer")]
COMPLETE = ["complete", "complete"]


def request(binary, mode="stable"):
    write = {"id": "write", "program_id": "producer", "stdin": f"P {mode}\n", "limits": LIMITS,
             "handoff": {"id": "artifact", "from": "file", "path": "handoff.bin", "max_bytes": 128}}
    read = {"id": "read", "program_id": "consumer", "limits": LIMITS,
            "stdin_parts": [{"type": "text", "data": "<"}, {"type": "handoff", "from": "artifact"},
                            {"type": "text", "data": ">"}]}
    if mode == "stdout":
        write["handoff"] = {"id": "artifact", "from": "stdout", "max_bytes": 128}
        del read["stdin_parts"]
        read["stdin_from"] = "artifact"
    return {"emit_logs": True, "expected_stdout": "42\n", "steps": [write, read],
            "programs": [{"id": program, "lang": "CPP20", "binaries": binary} for _, program in ORDER]}


def execute(url, name, payload, states, client_sha, *, status="Accepted", source="step:read:stdout", count=2):
    result, stdout = post(url, "/execute", payload)
    require(result["status"] == status and result.get("verdict_source") == source,
            f"{name}: unexpected native outcome {result['status']} {result.get('verdict_source')}")
    steps = result["steps"]
    order = ORDER[:count]
    require([(s["id"], s["program_id"]) for s in steps] == order, f"{name}: wrong executed prefix")
    cpu = [s["cpu_time_ns"] for s in steps]
    require(all(n > 0 for n in cpu) and sum(cpu) == result["cpu_time_ns"] and
            sum(s["cpu_time_ms"] for s in steps) == result["cpu_time_ms"], f"{name}: native CPU mismatch")
    if count == 2:
        require(steps[0]["handoff_bytes"] == 11 and stdout.count("42\n") == 1,
                f"{name}: native handoff or native log count changed")
    receipt = result.get("work_measurement_observation")
    counters = []
    if states is None:
        require(receipt is None, f"{name}: unexpected observation")
    else:
        require(receipt and receipt["version"] == 2 and receipt["authority"] == "diagnostic_only" and
                receipt["state"] == "steps" and not receipt.get("report"), f"{name}: invalid steps receipt")
        rows = receipt["steps"]
        require([(r["step_id"], r["program_id"]) for r in rows] == order and
                [r["state"] for r in rows] == states, f"{name}: unexpected diagnostic prefix or states")
        for row in rows:
            if row["state"] == "complete":
                report = row["report"]
                require(row["output_check"] == "native_output" and not row.get("reason") and
                        report["family"] == "cpp" and report["complete"] and report["instructions"] > 0 and
                        report["cpuid_profile"] == "x86-64-v2" and
                        report["installation"]["client_sha256"] == client_sha, f"{name}: unverified counter")
                counters.append(report["instructions"])
            else:
                require(row.get("reason") and not row.get("report") and not row.get("output_check"),
                        f"{name}: unavailable row contains a report")
                counters.append(None)
        require("LEGACY_PRODUCER" not in json.dumps(receipt), f"{name}: diagnostic leaked output")
    print(json.dumps({"case": name, "verdict": status, "source": source, "native_step_cpu_ns": cpu,
                      "native_cpu_ns": result["cpu_time_ns"], "states": states, "instructions": counters,
                      "score": result.get("score")}), flush=True)
    return result


def exercise(url, transport, binary, client_sha):
    def run(label, payload, states=COMPLETE, **kwargs):
        return execute(url, f"{transport}-{label}", payload, states, client_sha, **kwargs)

    run("composed-file-handoff", request(binary))
    run("binary-stdout-handoff", request(binary, "stdout"))
    wrong = request(binary)
    wrong["expected_stdout"] = "43\n"
    run("wrong-answer", wrong, status="Wrong Answer")
    for mode in ("vary-file", "vary-stdout"):
        run(mode, request(binary, mode), ["unavailable", "complete"])
    unknown = request(binary)
    unknown["programs"][0]["lang"] = "binary"
    run("unknown-producer-family", unknown, ["unavailable", "complete"])
    run("failed-producer", request(binary, "fail"), ["unavailable"], status="Runtime Error",
        source="step:write:exit_code", count=1)
    small = request(binary)
    small["steps"][0]["limits"] = {**LIMITS, "output_bytes": 128}
    small["steps"][0]["handoff"]["max_bytes"] = 1
    run("failed-handoff", small, ["unavailable"], status="Runtime Error", source="step:write:handoff", count=1)
    with expect_marker_reads() as marker:
        checker = (f"with open({str(marker)!r}, 'rb') as f: assert f.read(1)\n"
                   "from pathlib import Path\nimport sys\n"
                   "assert Path(sys.argv[1]).read_bytes() == b'<\\x00\\xff*' + bytes(8) + b'>'\n"
                   "assert Path(sys.argv[2]).read_bytes() == b'42\\n'\n"
                   "assert Path(sys.argv[3]).read_bytes() == b'43\\n'\nprint('0.625')\n")
        special = request(binary)
        special.update(expected_stdout="43\n", spj={"lang": "PYTHON3", "binary": artifact("judge.py", checker),
                                                 "emit_score": True})
        result = run("one-spj", special, source="step:read:spj")
        require(result.get("score") == 0.625, "native special judge score changed")
    print(json.dumps({"transport": transport, "final_checker_invocations": 1}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a network-none root container")
    client_sha = hashlib.sha256(Path("/opt/durante/lib/libdurante.so").read_bytes()).hexdigest()
    with server(args, 18770, True) as worker:
        binary = compile_cpp(worker, PROGRAM)
        counted = compile_cpp(worker, f"#define INVOCATION_MARKER {json.dumps(str(MARKER))}\n" + PROGRAM)
        exercise(worker, "embedded", binary, client_sha)
    token = secrets.token_urlsafe(32)
    with server(args, 18770, True, runner_token=token) as worker:
        with server(args, 18771, False, remote=worker, runner_token=token) as control:
            with expect_marker_reads(2):
                execute(control, "remote-observation-disabled", request(counted), None, client_sha)
            print(json.dumps({"remote_observation_disabled_invocations": 2}), flush=True)
        with server(args, 18771, True, remote=worker, runner_token=token) as control:
            exercise(control, "remote", binary, client_sha)
    print(json.dumps({"complete": True}), flush=True)


if __name__ == "__main__":
    main()
