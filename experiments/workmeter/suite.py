#!/usr/bin/env python3
"""Materialize the public, fixed-work corpus used by the metering experiment.

This only creates local fixture files. It does not execute submissions or deploy
services. All targets and inputs come from the finite definitions below; caller
supplied Python source and arbitrary paths are not accepted.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path

SCALES = (1, 2, 4)
LANGUAGES = ("python", "pypy")
FAMILIES = (
    "pure_python", "sorted_builtin", "large_int", "string_native",
    "hashlib_native", "zlib_native", "regex_native",
)
BOUNDARIES = (
    "normal", "system_exit_zero", "system_exit_nonzero", "exception",
    "immediate_exit", "wall_timeout", "cpu_timeout", "thread",
)
RUNNER = '''import os, runpy, site, sys
path = sys.argv.pop(1)
site.setquit()
site.setcopyright()
site.sethelper()
sys.path.insert(0, os.path.dirname(path))
runpy.run_path(path, run_name="__main__")
'''

# The million-'a' digest is the published SHA-256 test vector. All three were
# also independently compared with the OpenSSL command-line implementation.
SHA256_A = {
    1: "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0",
    2: "bcf7f9d1b4311c3352e60502255ce09a6744df84e8f2c89f79c4b5d74933a95a",
    4: "437f326a498e437cbf8b95fed6c48661a622cca6a575bb57b4b04a582e711f24",
}


def lcg_after(iterations: int) -> int:
    """Binary composition of affine maps; independent of the target's loop."""
    modulus = 1 << 32
    multiplier, increment = 1664525, 1013904223
    combined_multiplier, combined_increment = 1, 0
    while iterations:
        if iterations & 1:
            combined_multiplier = combined_multiplier * multiplier % modulus
            combined_increment = (combined_increment * multiplier + increment) % modulus
        increment = increment * (multiplier + 1) % modulus
        multiplier = multiplier * multiplier % modulus
        iterations >>= 1
    return (combined_multiplier + combined_increment) % modulus


def expected_output(family: str, scale: int) -> bytes:
    if family not in FAMILIES or type(scale) is not int or scale not in SCALES:
        raise ValueError("unknown fixed scaling fixture")
    if family == "pure_python":
        value = str(lcg_after(750000 * scale))
    elif family == "sorted_builtin":
        size = 65536 * scale
        # Multiplication by an odd number permutes the power-of-two domain.
        value = f"{size} 0 {size - 1} {size * (size - 1) // 2}"
    elif family == "large_int":
        width = 4096 * scale
        result = pow((1 << (width - 1)) + 217, 4000, (1 << width) - 159)
        value = f"{result.bit_length()} {result % 1000000007}"
    elif family == "string_native":
        blocks = 65536 * scale
        value = f"{8 * blocks} {24 * 3 * blocks}"
    elif family == "hashlib_native":
        value = SHA256_A[scale]
    elif family == "zlib_native":
        value = str(24 * 1000000 * scale)
    else:
        blocks = 65536 * scale
        value = f"{5 * blocks} {blocks}"
    return (value + "\n").encode()


def boundary_contract(name: str, language: str = "python") -> dict:
    if name not in BOUNDARIES:
        raise ValueError("unknown fixed boundary fixture")
    if language not in LANGUAGES:
        raise ValueError("unknown runtime")
    exit_code = {"system_exit_nonzero": 7, "exception": 1}.get(name, 0)
    execution = {"kind": "exit", "code": exit_code}
    native_report = "valid"
    cpu_limit, wall_timeout = 20, 60
    if name == "wall_timeout":
        execution = {"kind": "wall_limit"}
        native_report = "missing"
        cpu_limit, wall_timeout = 5, 1
    elif name == "cpu_timeout":
        execution = {"kind": "cpu_limit"}
        native_report = "missing"
        cpu_limit, wall_timeout = 1, 10
    elif name == "immediate_exit":
        native_report = "missing"
    elif name == "thread":
        native_report = "unsupported_threads"
        if language == "python":
            # Both experiment CPython builds link the meter runtime, which
            # rejects pthread_create before it can race non-atomic counters.
            execution = {"kind": "exit", "code": 1}
    return {
        "expected_execution": execution,
        "expected_metric": {
            "ir_count": native_report,
            "jit_mapped": "partial_or_missing",
        },
        "cpu_limit_seconds": cpu_limit,
        "wall_timeout_seconds": wall_timeout,
        "expected_output": b"done\n" if name in (
            "normal", "system_exit_zero", "system_exit_nonzero"
        ) or (name == "thread" and language == "pypy") else b"",
    }


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def generate(output: Path, languages: tuple[str, ...] = LANGUAGES) -> dict:
    if not languages or len(set(languages)) != len(languages) or any(x not in LANGUAGES for x in languages):
        raise ValueError("languages must be a nonempty unique subset of python,pypy")
    output = Path(output)
    if output.is_symlink() or (output.exists() and (not output.is_dir() or any(output.iterdir()))):
        raise ValueError("output must be absent or an empty real directory")
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    output.chmod(0o700)
    sources = Path(__file__).resolve().parent / "fixtures"
    written: dict[str, str] = {}

    def write(relative: str, data: bytes) -> None:
        path = output / relative
        digest = sha(data)
        if relative in written:
            if written[relative] != digest:
                raise ValueError("conflicting generated fixture")
            return
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with path.open("xb") as handle:
            handle.write(data)
        path.chmod(0o600)
        written[relative] = digest

    workloads = []
    definitions = [(family, scale, None) for family in FAMILIES for scale in SCALES]
    definitions += [("boundary", None, name) for name in BOUNDARIES]
    for family, scale, boundary in definitions:
        fixture_id = f"{family}-{scale}" if boundary is None else f"boundary-{boundary}"
        source_relative = f"sources/{family}.py"
        source_bytes = (sources / (family + ".py")).read_bytes()
        write(source_relative, source_bytes)
        input_bytes = (str(scale) + "\n").encode() if boundary is None else (boundary + "\n").encode()
        input_relative = f"cases/{fixture_id}.in"
        contract = ({
            "expected_execution": {"kind": "exit", "code": 0},
            "expected_metric": {"ir_count": "valid", "jit_mapped": "partial_or_missing"},
            "cpu_limit_seconds": 20, "wall_timeout_seconds": 60,
            "expected_output": expected_output(family, scale),
        } if boundary is None else None)
        write(input_relative, input_bytes)
        for language in languages:
            selected_contract = boundary_contract(boundary, language) if boundary is not None else dict(contract)
            expected = selected_contract.pop("expected_output")
            expected_relative = f"cases/{fixture_id}-{language}.out"
            write(expected_relative, expected)
            interpreter = "python3" if language == "python" else "pypy3"
            workloads.append({
                "id": f"public-{language}-{fixture_id}", "submission_id": None,
                "problem_id": None, "kind": "diagnostic", "language": language,
                "family": family, "scale": scale, "boundary": boundary,
                "cwd": "sources", "primary": source_relative, "primary_argv_index": 5,
                "argv": [interpreter, "-I", "-S", "-c", RUNNER, family + ".py"],
                "input": input_relative, "expected": expected_relative,
                "input_sha256": sha(input_bytes), "expected_sha256": sha(expected),
                "input_bytes": len(input_bytes), "expected_bytes": len(expected),
                "files": [{"path": source_relative, "sha256": sha(source_bytes),
                           "bytes": len(source_bytes), "mode": "0600"}],
                "coverage_expectation": (
                    "external_library_work_gap" if family in ("hashlib_native", "zlib_native")
                    else "internal_native_or_interpreter_work" if boundary is None
                    else "exit_and_validity_boundary"
                ),
                **selected_contract,
            })
    manifest = {
        "schema": 1, "purpose": "Fixed-work public metering diagnostics, not a production judging policy",
        "workloads": workloads, "files": dict(written),
        "scope": {"workloads": len(workloads), "languages": list(languages),
                  "scales": list(SCALES), "full_case_rejudge": False},
        "recommended_cohort": {
            "instance_count": 4, "cpu_per_instance": 1,
            "serial_repeats": 3, "parallel_stress_repeats": 3,
            "parallel_stress_copies": 2, "arms_per_language": 2,
            "parallel_stress_selection": "scale=4; private original cases may be added separately",
            "public_executions": 4 * 3 * 2 * (len(workloads) + 2 * len(FAMILIES) * len(languages)),
            "fresh_instance_effects": "Preserve instance identities and model; distinct instances need not be distinct physical hosts",
            "parallel_scope": "Same-instance interference stress; measure horizontal TC fanout separately",
            "schedule": "Predeclare and randomize order; retain every result, including expected boundary failures",
        },
        "expected_output_methods": {
            "pure_python": "Binary affine composition modulo 2^32",
            "sorted_builtin": "Odd multiplier permutes a power-of-two domain; closed-form sum",
            "large_int": "Modular exponentiation versus target repeated multiplication",
            "string_native": "Each fixed source pattern contributes one match of each token",
            "hashlib_native": "Precomputed SHA-256 vectors independently checked with OpenSSL CLI",
            "zlib_native": "Roundtrip equality to original bytes and known length",
            "regex_native": "Digit deletion leaves exactly aabb| per source pattern",
        },
        "measurement_notes": [
            "Work is fixed by inputs; no loop terminates using a measured CPU clock.",
            "String/hashlib/zlib/regex use constant native call counts and growing input sizes.",
            "The native meter excludes external shared-library internals; hashlib/zlib are deliberate coverage probes.",
            "PyPy JIT counts are partial diagnostics; zero or missing counts are not zero execution time.",
            "Expected nonzero exits, missing reports and thread invalidity are boundary outcomes, not accepted submissions.",
            "Timeout fixtures must be terminated and reaped by the parent with the stated limit provenance.",
        ],
    }
    write("manifest.json", (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode())
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--languages", nargs="+", choices=LANGUAGES, default=list(LANGUAGES))
    args = parser.parse_args()
    os.umask(0o077)
    result = generate(args.output, tuple(args.languages))
    print(json.dumps({"manifest": str(args.output / "manifest.json"),
                      "workloads": len(result["workloads"])}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
