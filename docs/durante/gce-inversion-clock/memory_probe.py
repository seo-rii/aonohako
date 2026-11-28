"""Paired native and unscored raw-memory controls on the sealed v13 CPU-1 worker."""
from contextlib import ExitStack
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys

IMAGE = "sha256:5ebf65c242036f85ea8873926aa9e8721fa22cd8d0d3394dd32309e09354a3d2"
SERVER = "214c0b900499017297649a8ec4a3d8d5d6ce5cd1c6b32b1de89882e506ae7d3c"
ELF = "3f947056dd48a8230d6fda7ad825591bc722337234cadd96acfc2906b2a752a6"
SIZES = (32, 720, 764, 768, 816)
ROUNDS = (1, 8)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def schedule(phase):
    for index, rounds in enumerate(ROUNDS):
        reverse = (index == 1) != (phase == "repeat")
        for size in reversed(SIZES) if reverse else SIZES:
            for kind in (("raw", "native") if reverse else ("native", "raw")):
                yield size, rounds, kind


def valid_raw(receipt, req, stdout, expected):
    report = receipt["work_measurement"]
    envelope = receipt["work_measurement_execution"]
    clock = envelope["clock"]
    rep = report["rep_breakdown"]
    return (receipt["status"] == "Container Initialization Failed" and receipt.get("score") is None
            and receipt.get("exit_code") == 0 and stdout.encode() == expected
            and receipt.get("memory_kb", 0) > 0 and receipt.get("cpu_time_ns", 0) > 0
            and envelope["state"] == "complete" and envelope["output_matches"] is True
            and envelope["nominal_limits"] == req["limits"] and envelope["raw_limits"] == req["work_measurement_limits"]
            and report["engine_profile"] == "dynamorio-11.3-durante-v13-vm32m"
            and report["complete"] and report["quality"] == "uninterrupted" and report["signals_observed"] == 0
            and report["installation"]["unchanged_before_after"]
            and report["installation"]["source_commit"] == "6b4d74f248235f8f616aa5b8e151ca0db5a029de"
            and clock["state"] == "observed" and clock["budget"] == req["work_measurement_clock"]["budget"]
            and clock["progress"] == {"instructions": report["instructions"], "movs_bytes": rep["movs_bytes"],
                                      "stos_bytes": rep["stos_bytes"]})


def collect(root, out, work, trace, phase):
    pilot_root = root / "pilot"
    sys.path.insert(0, str(pilot_root))
    spec = importlib.util.spec_from_file_location("memory_pilot", pilot_root / "guest.py")
    pilot = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(pilot)
    binary = (root / "memory-table").read_bytes()
    pilot.require(digest(binary) == ELF and digest(pilot.SERVER.read_bytes()) == SERVER,
                  "memory panel target or worker differs")
    pilot.require(os.sched_getaffinity(0) == {1}, "memory panel must run on CPU 1")
    boot = digest(Path("/proc/sys/kernel/random/boot_id").read_text().strip().encode())
    host = dict(phase=phase, boot_sha256=boot, affinity=[1], image=IMAGE, binary_sha256=ELF,
                server_sha256=SERVER, steal_ticks_before=pilot.source.cpu_steal())
    with ExitStack() as stack:
        services = {}
        for kind in ("native", "raw"):
            url, pid = stack.enter_context(pilot.worker(kind, work, out, IMAGE,
                                                       pilot.measurements() if kind == "raw" else {}))
            pilot.require(digest(Path(f"/proc/{pid}/exe").read_bytes()) == SERVER,
                          f"unexpected {kind} memory worker")
            services[kind] = url
        with (out / "trials.jsonl").open("xb") as rows, (out / "exec-trace.txt").open("x") as traces:
            for sequence, (size, rounds, kind) in enumerate(schedule(phase)):
                stdin = f"{size} {rounds}\n".encode()
                expected = f"{size * 256 * rounds * (rounds + 1) // 2}\n".encode()
                req = pilot.request_for("memory-table", kind, 30000, stdin, expected, binary, None)
                (trace / "trace").write_text("\n")
                receipt, stdout = pilot.post(services[kind], "/execute", req, timeout=200)
                execs = pilot.source.collect_execs(trace, traces, sequence, kind)
                row = dict(sequence=sequence, size_mib=size, rounds=rounds, kind=kind, stdout=stdout,
                           request_sha256=digest(json.dumps(req).encode()), execs=execs, receipt=receipt)
                rows.write(pilot.packed(row) + b"\n")
                rows.flush()
                if kind == "raw":
                    pilot.require(valid_raw(receipt, req, stdout, expected), "ineligible raw memory control")
                else:
                    pilot.require(receipt["status"] in ("Accepted", "Memory Limit Exceeded")
                                  and receipt.get("work_measurement_execution") is None
                                  and (stdout.encode() == expected if receipt["status"] == "Accepted"
                                       else "memory" in receipt["verdict_source"]),
                                  "unexpected native memory control")
    host.update(trial_count=sequence + 1, steal_ticks_after=pilot.source.cpu_steal())
    pilot.write_json(out / "platform.json", host)
