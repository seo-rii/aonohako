#!/usr/bin/env python3
"""Verify Pipeline V1 Durante observations in the offline HTTP qualification image.

Mount this script beside verify_durante_observation.py and use the same image,
judge bundle, server and resource limits documented in that script.
"""
import argparse
import base64
from contextlib import contextmanager
import ctypes
import hashlib
import json
import os
from pathlib import Path
import struct
import sysconfig
import urllib.error
import urllib.request

from verify_durante_observation import artifact, compile_cpp, require, server


# The consumer receives the producer's stdout followed by the contents of its
# output file. It checks the tags and both values. The timestamp can vary in
# exactly one channel while the native final answer stays deterministic.
PROGRAM = r"""
#include <cstdio>
#include <cstring>
#include <ctime>
int main() {
    char tag[64], mode[64];
    if (scanf("%63s", tag) != 1) return 2;
    if (!strcmp(tag, "produce")) {
        if (scanf("%63s", mode) != 1) return 3;
        timespec ts{};
        if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) return 4;
        long long now = ts.tv_sec * 1000000000LL + ts.tv_nsec;
        FILE *f = fopen("handoff.txt", "w");
        if (!f) return 5;
        fprintf(f, "PRIVATE_PIPE_FILE %d %lld\n", 22, !strcmp(mode, "vary-file") ? now : 0);
        if (fclose(f) != 0) return 6;
        printf("PRIVATE_PIPE_STDOUT %d %lld\n", 20, !strcmp(mode, "vary-stdout") ? now : 0);
    } else {
        int a, b;
        long long first, second;
        if (strcmp(tag, "PRIVATE_PIPE_STDOUT") || scanf("%d%lld%63s%d%lld", &a, &first, tag, &b, &second) != 5
            || strcmp(tag, "PRIVATE_PIPE_FILE")) return 7;
        printf("PRIVATE_PIPE_FINAL %d\n", a + b);
    }
}
"""
ANSWER = "PRIVATE_PIPE_FINAL 42\n"
LIMITS = {"time_ms": 3000, "wall_time_ms": 8000, "memory_mb": 256}


def resource(text):
    return {"data_b64": base64.b64encode(text.encode()).decode()}


def make_pipeline(binary, mode="stable", answer=ANSWER, producer_lang="CPP20"):
    return {"emit_logs": True, "pipeline": {
        "version": 1,
        "resources": {"input": resource(f"produce {mode}\n"), "answer": resource(answer)},
        "programs": [{"id": "producer", "lang": producer_lang, "binaries": binary},
                     {"id": "consumer", "lang": "CPP20", "binaries": binary}],
        "steps": [
            {"id": "write", "executor": {"kind": "batch", "program_id": "producer"},
             "stdin": [{"type": "resource", "id": "input"}], "limits": LIMITS,
             "outputs": [{"id": "text", "source": {"kind": "participant_stdout"}, "max_bytes": 256},
                         {"id": "file", "source": {"kind": "participant_file", "path": "handoff.txt"}, "max_bytes": 256}]},
            {"id": "read", "executor": {"kind": "batch", "program_id": "consumer"}, "limits": LIMITS,
             "stdin": [{"type": "artifact", "id": "text"}, {"type": "artifact", "id": "file"}]}],
        "final_judge": {"kind": "diff", "input": {"type": "resource", "id": "input"},
                        "expected": {"type": "resource", "id": "answer"},
                        "actual": {"type": "step_stdout", "step_id": "read"}}}}


def execute(url, name, payload, states, client_sha, *, status="Accepted", source="final:diff", count=2):
    request = urllib.request.Request(url + "/execute", data=json.dumps(payload).encode(),
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=90) as response:
        require(response.headers.get_content_type() == "text/event-stream", f"{name}: expected SSE")
        body = response.read(8 << 20).decode()
    require("PRIVATE_PIPE_" not in body, f"{name}: private artifact reached SSE")
    events = []
    for frame in body.split("\n\n"):
        lines = frame.splitlines()
        if len(lines) == 2 and lines[0].startswith("event: ") and lines[1].startswith("data: "):
            events.append((lines[0][7:], json.loads(lines[1][6:])))
    require(events and events[-1][0] == "result" and [e for e, _ in events].count("result") == 1 and
            all(e not in {"image", "log"} for e, _ in events), f"{name}: unexpected pipeline stream")
    result = events[-1][1]
    steps = result["steps"]
    order = [("write", "producer"), ("read", "consumer")][:count]
    require(result["status"] == status and result["verdict_source"] == source, f"{name}: {result}")
    require([(s["id"], s["program_id"]) for s in steps] == order, f"{name}: wrong native step order")
    cpu = [s["cpu_time_ns"] for s in steps]
    require(all(n > 0 for n in cpu) and sum(cpu) == result["cpu_time_ns"] and
            sum(s["cpu_time_ms"] for s in steps) == result["cpu_time_ms"], f"{name}: native CPU mismatch")
    require(all(not r.get("stdout") and not r.get("stderr") for r in [*steps, result]) and
            not result.get("sidecar_outputs"), f"{name}: private step output returned")
    receipt = result.get("work_measurement_observation")
    measured = []
    if states is None:
        require(receipt is None, f"{name}: unexpected observation")
    else:
        require(receipt and receipt["version"] == 2 and receipt["authority"] == "diagnostic_only" and
                receipt["state"] == "pipeline" and not receipt.get("report"), f"{name}: incorrect pipeline receipt")
        rows = receipt["steps"]
        require([(r["step_id"], r["program_id"]) for r in rows] == order and
                [r["state"] for r in rows] == states, f"{name}: {rows}")
        for row in rows:
            if row["state"] == "complete":
                report = row["report"]
                measured.append(report["instructions"])
                require(row["output_check"] == "native_output" and not row.get("reason") and
                        report["family"] == "cpp" and report["instructions"] > 0 and report["complete"] and
                        report["cpuid_profile"] == "x86-64-v2" and report["installation"]["client_sha256"] == client_sha,
                        f"{name}: incomplete output-checked report")
            else:
                measured.append(None)
                require(row.get("reason") and not row.get("output_check") and not row.get("report"),
                        f"{name}: unavailable step contains an unchecked report")
    print(json.dumps({"case": name, "verdict": result["status"], "source": source, "native_step_cpu_ns": cpu,
                      "native_cpu_ns": result["cpu_time_ns"], "states": states, "instructions": measured,
                      "score": result.get("score")}), flush=True)
    return result


@contextmanager
def single_marker_read():
    # Only the native checker/interactor opens this stdlib file. Watching open
    # and close prevents the kernel coalescing consecutive identical events.
    marker = Path(sysconfig.get_path("stdlib")) / "colorsys.py"
    libc = ctypes.CDLL(None, use_errno=True)
    fd = libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
    if fd < 0:
        raise OSError(ctypes.get_errno(), "inotify_init1")
    try:
        if libc.inotify_add_watch(fd, os.fsencode(marker), 0x20 | 0x10) < 0:
            raise OSError(ctypes.get_errno(), str(marker))
        yield marker
        masks = [mask for _, mask, _, _ in struct.iter_unpack("iIII", os.read(fd, 65536))]
        require(masks == [0x20, 0x10], "expected one checker or interactor invocation")
    finally:
        os.close(fd)


def check_spj(url, binary, client_sha):
    request = make_pipeline(binary)
    with single_marker_read() as marker:
        checker = (f"with open({str(marker)!r}, 'rb') as f: assert f.read(1)\n"
                   "import sys\nfrom pathlib import Path\n"
                   "assert Path(sys.argv[1]).read_text() == 'produce stable\\n'\n"
                   f"assert Path(sys.argv[2]).read_text() == {ANSWER!r}\n"
                   f"assert Path(sys.argv[3]).read_text() == {ANSWER!r}\n"
                   "print('0.625')\n")
        request["pipeline"]["final_judge"].update(kind="spj", spj={
            "lang": "PYTHON3", "binary": artifact("check.py", checker), "emit_score": True})
        result = execute(url, "one-final-spj", request, ["complete", "complete"], client_sha, source="final:spj")
        require(result.get("score") == 0.625, "native final checker score changed")
    print(json.dumps({"final_checker_invocations": 1}), flush=True)


def check_mixed_pipeline(url, binary, client_sha):
    request = make_pipeline(binary)
    pipeline = request["pipeline"]
    write = pipeline["steps"][0]
    write["executor"] = {"kind": "interactive", "participant_program_id": "producer",
                         "interactor_program_id": "judge", "interactor_limits": LIMITS}
    write["outputs"][0]["source"] = {"kind": "interactor_output"}
    with single_marker_read() as marker:
        checker = (f"with open({str(marker)!r}, 'rb') as f: assert f.read(1)\n"
                   "import sys\nfrom pathlib import Path\n"
                   "print(Path(sys.argv[1]).read_text().strip(), flush=True)\n"
                   "response = sys.stdin.readline()\n"
                   "assert response == 'PRIVATE_PIPE_STDOUT 20 0\\n'\n"
                   "Path(sys.argv[2]).write_text(response)\n")
        pipeline["programs"].append({"id": "judge", "lang": "PYTHON3", "binaries": [artifact("Main.py", checker)]})
        result = execute(url, "interactive-then-batch", request, ["unavailable", "complete"], client_sha)
        require("interactive" in result["work_measurement_observation"]["steps"][0]["reason"],
                "missing interactive eligibility reason")
    print(json.dumps({"interactor_invocations": 1}), flush=True)


def remote_capability(url, payload):
    with urllib.request.urlopen(url + "/capabilities", timeout=5) as response:
        require("pipeline-v1" not in json.load(response)["capabilities"], "remote transport advertised Pipeline V1")
    request = urllib.request.Request(url + "/execute", data=json.dumps(payload).encode(),
                                     headers={"Content-Type": "application/json"})
    try:
        urllib.request.urlopen(request, timeout=10).close()
    except urllib.error.HTTPError as error:
        with error:
            require(error.code == 400 and json.load(error)["error"] == "unsupported_capability",
                    "remote controller returned an unexpected pipeline response")
    else:
        raise RuntimeError("remote controller accepted an unnegotiated pipeline")
    print(json.dumps({"case": "remote-capability", "http_status": 400, "reason": "unsupported_capability"}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"], "use a network-none root container")
    client_sha = hashlib.sha256(Path("/opt/durante/lib/libdurante.so").read_bytes()).hexdigest()
    with server(args, 18770, False) as off:
        binary = compile_cpp(off, PROGRAM)
        execute(off, "observation-disabled", make_pipeline(binary), None, client_sha)
    with server(args, 18770, True) as worker:
        complete = ["complete", "complete"]
        execute(worker, "batch-handoffs", make_pipeline(binary), complete, client_sha)
        execute(worker, "wrong-answer", make_pipeline(binary, answer="wrong\n"), complete, client_sha,
                status="Wrong Answer")
        for mode in ("vary-file", "vary-stdout"):
            result = execute(worker, mode, make_pipeline(binary, mode), ["unavailable", "complete"], client_sha)
            require("output differs" in result["work_measurement_observation"]["steps"][0]["reason"],
                    f"{mode}: divergence was not detected")
        execute(worker, "unknown-source-family", make_pipeline(binary, producer_lang="binary"),
                ["unavailable", "complete"], client_sha)
        small = make_pipeline(binary)
        small["pipeline"]["steps"][0]["outputs"][1]["max_bytes"] = 1
        execute(worker, "failed-handoff-stops-consumer", small, ["unavailable"], client_sha, count=1,
                status="Runtime Error", source="step:write:artifact")
        check_spj(worker, binary, client_sha)
        check_mixed_pipeline(worker, binary, client_sha)
        with server(args, 18771, True, remote=worker) as control:
            remote_capability(control, make_pipeline(binary))
    print(json.dumps({"complete": True}), flush=True)


if __name__ == "__main__":
    main()
