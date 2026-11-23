#!/usr/bin/env python3
"""Run the automatic-observation HTTP checks in an offline Linux container.

Mount the server and this script read-only, the judge bundle at /opt/durante,
and writable executable tmpfs mounts at /tmp and /work. Invoke with Python 3,
--server <binary> and --runtime-image sha256:<digest>. The image needs g++,
static libc and CPython. Run as root with docker --network none.
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
import subprocess
import sysconfig
import time
import urllib.error
import urllib.request


STDIN = "17 25\n"
EXPECTED = "42\n"
RESOURCE_POLICY = {"max_limits": {"cpu_time_ms": 12000, "wall_time_ms": 30000, "memory_mb": 512},
                   "memory_budget_mb": 2048, "reserved_memory_mb": 512}
CPP = '#include <cstdio>\nint main(){int a,b;if(scanf("%d%d",&a,&b)!=2)return 2;printf("%d\\n",a+b);}\n'


CPU_LOOP = """#include <cstdio>
int main() {
    unsigned n, value;
    if (scanf("%u%u", &n, &value) != 2) return 1;
    for (unsigned i = 0; i < n; ++i) value = value * 1664525u + 1013904223u;
    printf("%u\\n", value);
}
"""


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def check_clock_receipt(report, latency_bound, name):
    require(report and report.get("complete") is True, f"{name}: missing complete Durante report")
    if latency_bound:
        require((report.get("latency_bound") or {}).get("version") == 1, f"{name}: missing v1 latency bound")
    else:
        require("latency_bound" not in report, f"{name}: ordinary clock unexpectedly reported a latency bound")


def artifact(name, text, mode=None):
    fields = {"name": name, "data_b64": base64.b64encode(text.encode()).decode()}
    if mode:
        fields["mode"] = mode
    return fields


@contextmanager
def server(args, port, enabled, remote=None, *, runner_token=None, operator_env=None, umask=-1):
    env = {"PATH": os.defpath, "PORT": str(port), "AONOHAKO_DEPLOYMENT_TARGET": "dev",
           "AONOHAKO_INBOUND_AUTH": "none", "AONOHAKO_WORK_ROOT": "/work",
           "AONOHAKO_WORK_ROOT_MAX_BYTES": str(256 << 20), "AONOHAKO_MAX_ACTIVE_RUNS": "1",
           "AONOHAKO_CPU_NORMALIZATION": "false", "AONOHAKO_CPU_SHORT_CASE_SAMPLING": "false",
           "AONOHAKO_EXECUTION_TRANSPORT": "remote" if remote else "embedded",
           "AONOHAKO_SANDBOX_BACKEND": "none" if remote else "helper",
           "AONOHAKO_DURANTE_OBSERVE": "on" if enabled else "off",
           "AONOHAKO_DURANTE_FAMILIES": "cpp,cpython", "AONOHAKO_DURANTE_CPUID_PROFILE": "x86-64-v2",
           "AONOHAKO_DURANTE_LATENCY_BOUND": "v1" if getattr(args, "latency_bound", True) else "off",
           "AONOHAKO_DURANTE_RUNTIME_DETERMINISM": "v1",
           "AONOHAKO_DURANTE_RESOURCE_POLICY": json.dumps(RESOURCE_POLICY),
           "AONOHAKO_DURANTE_RUNTIME_IMAGE": args.runtime_image,
           "AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT": args.runtime_image}
    if operator_env:
        env.update(operator_env)
    if remote:
        env.update(AONOHAKO_REMOTE_RUNNER_URL=remote, AONOHAKO_REMOTE_RUNNER_AUTH="none")
        if runner_token:
            env.update(AONOHAKO_REMOTE_RUNNER_AUTH="bearer", AONOHAKO_REMOTE_RUNNER_TOKEN=runner_token)
    elif runner_token:
        env.update(AONOHAKO_INBOUND_AUTH="bearer", AONOHAKO_API_BEARER_TOKEN=runner_token)
    url = f"http://127.0.0.1:{port}"
    process = subprocess.Popen([args.server], env=env, umask=umask)
    try:
        for _ in range(100):
            require(process.poll() is None, f"server {port} exited before readiness")
            try:
                with urllib.request.urlopen(url + "/readyz", timeout=1) as ready:
                    if ready.read() == b"ok":
                        break
            except urllib.error.URLError:
                pass
            time.sleep(0.1)
        else:
            raise RuntimeError(f"server {port} did not become ready")
        yield url
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=12)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def post(url, path, payload, *, token=None, timeout=75, max_bytes=8 << 20):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url + path, data=json.dumps(payload).encode(), headers=headers)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        require(response.headers.get_content_type() == "text/event-stream", f"{path}: expected SSE")
        data = response.read(max_bytes + 1)
        require(len(data) <= max_bytes, f"{path}: SSE response exceeds {max_bytes} bytes")
        body = data.decode()
    events = []
    for frame in body.split("\n\n"):
        pair = frame.splitlines()
        if len(pair) == 2 and pair[0].startswith("event: ") and pair[1].startswith("data: "):
            events.append((pair[0][7:], json.loads(pair[1][6:])))
    require(events and [name for name, _ in events].count("result") == 1 and events[-1][0] == "result",
            f"{path}: missing final result")
    stdout = "".join(data["chunk"] for name, data in events if name == "log" and data["stream"] == "stdout")
    return events[-1][1], stdout


def compile_cpp(url, source, *, timeout=75, max_bytes=8 << 20):
    payload = {"lang": "CPP20", "sources": [artifact("Main.cpp", source)], "target": "Main"}
    result, _ = post(url, "/compile", payload, timeout=timeout, max_bytes=max_bytes)
    require(result["status"] == "OK", f"C++ compile failed: {result.get('reason')}, {result.get('stderr')}")
    binary, = result["artifacts"]
    sha = hashlib.sha256(base64.b64decode(binary["data_b64"], validate=True)).hexdigest()
    print(json.dumps({"compiled_cpp_sha256": sha}), flush=True)
    return [binary]


def execute(url, name, lang, binaries, *, state, status="Accepted", expected=EXPECTED, spj=None,
            stdin=STDIN, time_ms=3000, native_stdout=EXPECTED):
    payload = {"lang": lang, "binaries": binaries, "stdin": stdin, "expected_stdout": expected,
               "limits": {"time_ms": time_ms, "wall_time_ms": 8000, "memory_mb": 256}}
    if spj:
        payload["spj"] = spj
    result, stdout = post(url, "/execute", payload)
    print(json.dumps({"case": name, "stdout": stdout, "result": result}), flush=True)
    require(result["status"] == status, f"{name}: primary verdict mismatch")
    tle = status == "Time Limit Exceeded"
    if tle:
        require(result.get("verdict_source") in {"cpu_time", "cpu_time_final", "cpu_time_cgroup", "cpu_time_cgroup_final"}
                and result.get("score") is None and result.get("cpu_time_ns", 0) > time_ms * 1_000_000,
                f"{name}: expected the native CPU timeout")
        if result.get("exit_code") != 0:
            require(stdout == "", f"{name}: replay output reached the native stream")
    else:
        require(stdout == native_stdout and result.get("exit_code") == 0, f"{name}: primary output mismatch")
    require(result.get("cpu_time_ns", 0) > 0 and result["memory_kb"] >= 0 and
            not ({"work_measurement", "work_measurement_execution", "work_measurement_judgment",
                  "work_measurement_shadow"} & result.keys()), f"{name}: expected primary native accounting")
    observation = result.get("work_measurement_observation")
    if state is None:
        require(observation is None, f"{name}: observation should be omitted")
        return result
    require(observation and observation["version"] == 1 and observation["authority"] == "diagnostic_only" and
            observation["state"] == state, f"{name}: unexpected observation state")
    if state == "unavailable":
        require(observation.get("reason") and not observation.get("report") and not observation.get("output_check"),
                f"{name}: unavailable shape")
        if lang == "binary":
            require("source-language profile" in observation["reason"], f"{name}: unexpected rejection")
    else:
        report = observation["report"]
        family = "cpython" if lang == "PYTHON3" else "cpp"
        require(observation.get("output_check") == ("expected_output" if tle else "native_output"),
                f"{name}: unexpected source of the output comparison")
        require(report["family"] == family and report["complete"] is True and report["instructions"] > 0 and
                report["cpuid_profile"] == "x86-64-v2" and report["latency_bound"]["version"] == 1 and
                report["installation"]["unchanged_before_after"] is True and
                report["installation"]["client_sha256"] ==
                hashlib.sha256(Path("/opt/durante/lib/libdurante.so").read_bytes()).hexdigest() and
                "stdin_sha256" not in observation,
                f"{name}: incomplete or mismatched Durante report")
    return result


def special_judge(url, binary):
    # The checker opens a benign stdlib file once. Only this request runs in
    # the isolated container. Watching open and close prevents kernel event
    # coalescing if a regression invokes the checker twice.
    observed_file = Path(sysconfig.get_path("stdlib")) / "colorsys.py"
    library = ctypes.CDLL(None, use_errno=True)
    fd = library.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
    if fd < 0:
        raise OSError(ctypes.get_errno(), "inotify_init1")
    try:
        opened, closed = 0x20, 0x10  # IN_OPEN, IN_CLOSE_NOWRITE
        if library.inotify_add_watch(fd, os.fsencode(observed_file), opened | closed) < 0:
            raise OSError(ctypes.get_errno(), str(observed_file))
        checker = (f"with open({str(observed_file)!r}, 'rb') as f: assert f.read(1)\n"
                   "import sys\nfrom pathlib import Path\n"
                   "assert Path(sys.argv[2]).read_text() == '42\\n'\nprint('0.625')\n")
        spj = {"lang": "PYTHON3", "binary": artifact("checker.py", checker), "emit_score": True}
        result = execute(url, "cpp-special-judge", "CPP20", binary, state="complete",
                         expected="different\n", spj=spj)
        require(result.get("score") == 0.625 and result.get("verdict_source") == "spj",
                "special judge score or verdict was replaced")
        records = list(struct.iter_unpack("iIII", os.read(fd, 65536)))
        require([mask for _, mask, _, _ in records] == [opened, closed], "expected one checker execution")
        print(json.dumps({"special_judge_invocations": 1}), flush=True)
    finally:
        os.close(fd)


def operand_observations(args):
    code = args.rep_breakdown_binary.read_bytes()
    binaries = [{"name": "Main", "mode": "exec", "data_b64": base64.b64encode(code).decode()}]
    expected = "rep-breakdown-ok\n"

    def check(url, name, enabled):
        result = execute(url, name, "CPP20", binaries, state="complete", expected=expected,
                         native_stdout=expected, stdin="")
        report = result["work_measurement_observation"]["report"]
        require(report.get("rep_bytes") == 131, f"{name}: REP demand changed")
        if enabled:
            # MOVS reads and writes 1, 4 and 8 bytes. STOS writes 2, 8 and 1.
            # Seven byte comparisons account for the remaining seven bytes.
            require(report.get("memory_operand_bytes") == {"version": 1, "bytes": 44},
                    f"{name}: wrong nominal operand byte count")
        else:
            require("memory_operand_bytes" not in report, f"{name}: unexpected operand count")

    for enabled in (False, True):
        setting = "v1" if enabled else "off"
        matching = {"AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES": setting}
        mismatch = {"AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES": "off" if enabled else "v1"}
        with server(args, 18770, True, operator_env=matching) as runner:
            check(runner, f"embedded-operands-{setting}", enabled)
            with server(args, 18771, True, remote=runner, operator_env=matching) as control:
                check(control, f"remote-operands-{setting}", enabled)
            with server(args, 18771, True, remote=runner, operator_env=mismatch) as control:
                result = execute(control, f"remote-mismatched-operands-{setting}", "CPP20", binaries,
                                 state="unavailable", stdin="", expected=expected, native_stdout=expected)
                reason = result["work_measurement_observation"]["reason"]
                require(reason == "remote observation is inconsistent with the request or primary verdict",
                        f"unexpected counter mismatch diagnostic: {reason}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--runtime-image", required=True)
    parser.add_argument("--rep-breakdown-binary", type=Path,
                        help="run the v11 operand-byte checks using the statically linked rep_breakdown.S fixture")
    args = parser.parse_args()
    require(os.geteuid() == 0 and os.listdir("/sys/class/net") == ["lo"],
            "run inside a root network-none validation container")
    if args.rep_breakdown_binary:
        operand_observations(args)
        print(json.dumps({"complete": True}), flush=True)
        return
    with server(args, 18770, False) as off:
        binary = compile_cpp(off, CPP)
        cpu_loop = compile_cpp(off, CPU_LOOP)
        execute(off, "cpp-disabled", "CPP20", binary, state=None)
        with server(args, 18771, True, remote=off) as control:
            result = execute(control, "forwarded-disabled-runner", "CPP20", binary, state="unavailable")
            require(result["work_measurement_observation"]["reason"] == "remote runner returned no observation",
                    "disabled runner did not preserve the native result")
    with server(args, 18770, True) as runner:
        execute(runner, "cpp-accepted", "CPP20", binary, state="complete")
        execute(runner, "cpp-wrong-answer", "CPP20", binary, state="complete",
                status="Wrong Answer", expected="43\n")
        python = [artifact("Main.py", "a,b=map(int,input().split());print(a+b)\n")]
        execute(runner, "cpython-accepted", "PYTHON3", python, state="complete")
        execute(runner, "ambiguous-binary", "binary", binary, state="unavailable")
        # Applying the affine recurrence 25 million times modulo 2**32 gives
        # 3468253817. The bounded loop takes longer than the nominal 10 ms.
        for label, reference, state in (("match", "3468253817\n", "complete"),
                                        ("mismatch", "0\n", "unavailable")):
            execute(runner, "cpu-tle-" + label, "CPP20", cpu_loop, state=state, status="Time Limit Exceeded",
                    stdin="25000000 12345\n", expected=reference, time_ms=10)
        special_judge(runner, binary)
        with server(args, 18771, True, remote=runner) as control:
            execute(control, "forwarded-cpp", "CPP20", binary, state="complete")
            execute(control, "forwarded-cpu-tle", "CPP20", cpu_loop, state="complete",
                    status="Time Limit Exceeded", stdin="25000000 12345\n", expected="3468253817\n", time_ms=10)
            execute(control, "forwarded-ambiguous-binary", "binary", binary, state="unavailable")
    print(json.dumps({"complete": True}), flush=True)


if __name__ == "__main__":
    main()
