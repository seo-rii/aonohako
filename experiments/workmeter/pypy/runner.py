"""Fixed-fixture diagnostic wrapper; invoke with pypy3 -I -S and PYPYLOG set.

--mode counts|mapped --report /absolute/report.json -- /absolute/Main.py [args]
Same stdlib policy as Aonohako pythonStdlibRunner; no JIT/hash/GC changes.
The target and the report share a process. This is not tamper-resistant metering.
"""
import os
import runpy
import site
import sys

# -I omits the script directory. Import our fixed helper without leaving this
# directory in the submitted program's import search path.
_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, _HERE)
import meter
sys.path.pop(0)
import json
import pypyjit

MAX_TRACES = 1024
MAX_OPS = 100000


def code_source(key):
    if isinstance(key, tuple) and len(key) == 3 and hasattr(key[0], "co_filename"):
        code = key[0]
        return [code.co_filename, code.co_firstlineno, code.co_name, key[1]]
    return None


def main():
    args = sys.argv[1:]
    if len(args) < 6 or args[0] != "--mode" or args[1] not in ("counts", "mapped") or args[2] != "--report" or args[4] != "--":
        raise SystemExit("usage: --mode counts|mapped --report PATH -- MAIN [args]")
    mode, report_path, target = args[1], args[3], os.path.abspath(args[5])
    if not os.path.isabs(report_path):
        raise SystemExit("report path must be absolute")
    sys.argv = ["-c", *args[5:]]
    path = sys.argv.pop(1)
    site.setquit()
    site.setcopyright()
    site.sethelper()
    sys.path.insert(0, os.path.dirname(path))
    traces = []
    hook_errors = []
    state = {"ops": 0, "capped": False}

    def hook(info):
        try:
            ops = info.operations
            if len(traces) >= MAX_TRACES or state["ops"] + len(ops) > MAX_OPS:
                state["capped"] = True
                return
            trace = {"index": len(traces), "type": info.type, "driver": info.jitdriver_name,
                     "loop_no": info.loop_no, "bridge_no": info.bridge_no if info.type == "bridge" else None,
                     "source": code_source(info.greenkey), "asm_bytes": info.asmlen, "ops": []}
            for op in ops:
                row = {"name": op.name}
                if op.name == "label":
                    row["token"] = meter.token_number(repr(op))
                elif op.name == "debug_merge_point":
                    row["source"] = code_source(op.greenkey)
                elif op.name.startswith("guard_"):
                    match = meter.GUARD.search(repr(op))
                    row["guard_id"] = int(match.group(1), 16) if match else None
                trace["ops"].append(row)
            traces.append(trace)
            state["ops"] += len(ops)
        except BaseException as exc:
            if len(hook_errors) < 16:
                hook_errors.append({"type": type(exc).__name__, "message": str(exc)})

    if mode == "mapped":
        pypyjit.set_compile_hook(hook, operations=True)
    before = pypyjit.get_stats_snapshot()
    target_ok = False
    error_type = None
    try:
        runpy.run_path(path, run_name="__main__")
        target_ok = True
    except SystemExit as exc:
        target_ok = exc.code in (None, 0)
        if not target_ok:
            error_type = "SystemExit"
        raise
    except BaseException as exc:
        error_type = type(exc).__name__
        raise
    finally:
        if mode == "mapped":
            pypyjit.set_compile_hook(None)
        after = pypyjit.get_stats_snapshot()
        # The pinned PyPy consumes/removes PYPYLOG during startup. Its absence
        # from os.environ is not evidence that counters were disabled. Require
        # observed counter storage instead; target-window zero remains a gap.
        logging_enabled = bool(before.loop_run_times or after.loop_run_times)
        logging_env_visible = "jit-backend-counts:" in os.environ.get("PYPYLOG", "")
        delta_error = None
        try:
            counts = meter.delta_counts(before.loop_run_times, after.loop_run_times)
        except (TypeError, ValueError) as exc:
            counts = {}
            delta_error = str(exc)
        metric = meter.summarize(traces, counts, {os.path.abspath(__file__), os.path.join(_HERE, "meter.py")}) if mode == "mapped" else None
        if metric is not None and (hook_errors or state["capped"]):
            metric["available"] = False
        report = {"schema": 1, "version": 1, "pid": os.getpid(), "mode": mode,
                  "runtime": sys.version, "target": target,
                  "valid": target_ok and logging_enabled and delta_error is None and not hook_errors and not state["capped"],
                  "target_ok": target_ok, "target_error_type": error_type,
                  "logging_enabled": logging_enabled, "logging_env_visible": logging_env_visible,
                  "logging_enabled_evidence": "nonempty eager JIT counter snapshot",
                  "delta_error": delta_error,
                  "counter_snapshot_nonempty": bool(after.loop_run_times),
                  "baseline_counts": meter.encode_counts(before.loop_run_times),
                  "end_counts": meter.encode_counts(after.loop_run_times),
                  "delta_counts": meter.encode_counts(counts),
                  "jit_counters_delta": {key: value-before.counters.get(key, 0) for key, value in after.counters.items()},
                  "jit_counter_times_delta": {key: value-before.counter_times.get(key, 0.0) for key, value in after.counter_times.items()},
                  "hook_errors": hook_errors, "trace_capture_capped": state["capped"],
                  "traces": traces, "metric": metric,
                  "limitations": ["trusted fixtures only; in-process report is not tamper resistant",
                                  "all zero or absent JIT work is a coverage gap, never proof of zero program work",
                                  "report serialization occurs after snapshots but remains included in process wait4 cost"]}
        fd = os.open(report_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as out:
            json.dump(report, out, separators=(",", ":"), allow_nan=False)
            out.write("\n")


if __name__ == "__main__":
    main()
