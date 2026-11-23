#!/usr/bin/env python3
"""Qualify fixed stock-Python native-library fixtures with the Durante CLI.

This is a trusted-fixture diagnostic, not a sandbox or a time calibration.
Every observation (including failures) is retained in a fresh private directory.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "internal/durante/testdata/python-corpus"
SCALES = (1, 2, 4)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runner-bin", type=Path, required=True)
    parser.add_argument("--drrun", type=Path, required=True)
    parser.add_argument("--client", type=Path, required=True)
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--timeout-ms", type=int, default=30000)
    args = parser.parse_args()
    if not 1000 <= args.timeout_ms <= 120000:
        parser.error("timeout must be between 1000 and 120000 ms")
    prlimit = shutil.which("prlimit")
    python = shutil.which(args.python)
    if not prlimit or not python:
        parser.error("Linux prlimit and the selected Python interpreter are required")
    python = str(Path(python).resolve())
    args.output_dir.mkdir(mode=0o700, parents=False, exist_ok=False)
    os.chmod(args.output_dir, 0o700)
    output = args.output_dir.resolve()
    # Do not disable either interpreter's JIT or change its native-library policy.
    version = subprocess.run([python, "--version"], capture_output=True, text=True,
                             timeout=10, check=True)
    receipt = {"schema": 1, "authority": "diagnostic_only", "python": python,
               "version": (version.stdout + version.stderr).strip(),
               "runner_bin": str(args.runner_bin.resolve()),
               "installation_sha256": {name: hashlib.sha256(path.resolve().read_bytes()).hexdigest()
                                       for name, path in (("runner", args.runner_bin),
                                                          ("drrun", args.drrun), ("client", args.client))},
               "observations": [],
               "limitations": ["trusted fixed fixtures only", "not sandbox/API qualification",
                               "not CPU milliseconds or calibrated time", "kernel work excluded"]}
    failures = []
    try:
        for name in ("hashlib_native", "zlib_native"):
            fixture = FIXTURES / (name + ".py")
            counts = []
            for scale in SCALES:
                case = output / f"{name}-{scale}"
                case.mkdir(mode=0o700)
                expected = (hashlib.sha256(b"a" * (1000000 * scale)).hexdigest()
                            if name == "hashlib_native" else str(24000000 * scale)) + "\n"
                command = [prlimit, "--as=1073741824", "--cpu=150", "--fsize=16777216", "--",
                           str(args.runner_bin.resolve()), "run", "--drrun", str(args.drrun.resolve()),
                           "--client", str(args.client.resolve()), "--report", str(case / "report.json"),
                           "--records-dir", str(case / "records"), "--timeout-ms", str(args.timeout_ms),
                           "--", python, "-I", "-S", str(fixture)]
                row = {"fixture": name, "scale": scale,
                       "fixture_sha256": hashlib.sha256(fixture.read_bytes()).hexdigest(),
                       "command": command, "passed": False}
                receipt["observations"].append(row)
                with (case / "stdout").open("wb") as stdout, (case / "stderr").open("wb") as stderr:
                    with subprocess.Popen(command, stdin=subprocess.PIPE, stdout=stdout,
                                          stderr=stderr, start_new_session=True) as process:
                        try:
                            process.communicate(f"{scale}\n".encode(), timeout=args.timeout_ms / 1000 + 15)
                        except subprocess.TimeoutExpired:
                            # First give the Durante supervisor its cleanup window.
                            process.send_signal(signal.SIGTERM)
                            try:
                                process.communicate(timeout=8)
                            except subprocess.TimeoutExpired:
                                os.killpg(process.pid, signal.SIGKILL)
                                process.communicate()
                            raise
                        row["runner_exit"] = process.returncode
                report = json.loads((case / "report.json").read_text())
                execution, measurement = report["execution"], report["measurement"]
                valid = (row["runner_exit"] == 0 and execution.get("state") == "complete"
                         and execution.get("exit_code") == 0
                         and execution.get("cleanup_complete") is True
                         and measurement.get("valid") is True and measurement.get("complete") is True
                         and report.get("schema") == 1 and measurement.get("schema") == 1
                         and measurement.get("metric") == "bb_entry_app_instructions"
                         and measurement.get("scope") == "process_tree_user_code"
                         and measurement.get("process_count") == 1 and measurement.get("image_count") == 1
                         and measurement.get("authority") == "diagnostic_only"
                         and measurement.get("quality") == "uninterrupted"
                         and measurement.get("rep_bytes_enabled") is True
                         and measurement.get("unsupported_rep_entries") == 0
                         and measurement.get("signals_observed") == 0
                         and type(measurement.get("instructions")) is int
                         and measurement["instructions"] > 0
                         and (case / "stdout").read_text() == expected)
                row["passed"] = valid
                for key in ("instructions", "rep_bytes", "rep_entries", "quality"):
                    row[key] = measurement.get(key)
                if valid:
                    counts.append(measurement["instructions"])
                else:
                    failures.append(f"{name} scale {scale}: output/exit/accounting mismatch")
            # This deliberately detects the old LLVM/JIT counter's flat external-
            # library result. It is a coverage gate, not an instruction/time model.
            scaling = len(counts) == 3 and counts[0] < counts[1] < counts[2]
            receipt.setdefault("scaling", {})[name] = scaling
            if not scaling:
                failures.append(f"{name}: native-library instruction work did not grow at every scale")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        failures.append(f"{type(error).__name__}: {error}")
    finally:
        receipt["failures"] = failures
        receipt["passed"] = not failures
        (output / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({"passed": not failures, "receipt": str(output / "receipt.json"), "failures": failures}))
    return int(bool(failures))


if __name__ == "__main__":
    raise SystemExit(main())
