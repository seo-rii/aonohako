#!/usr/bin/env python3
import json
import os
import pathlib
import shutil
import subprocess
import sys


def receipt_candidates(source: pathlib.Path) -> list[pathlib.Path]:
    stem = source.with_suffix("").name
    return [
        pathlib.Path.cwd() / stem / "receipt.json",
        source.with_suffix("") / "receipt.json",
    ]


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: aonohako-alloy-check <Main.als>", file=sys.stderr)
        return 2

    source = pathlib.Path(sys.argv[1]).resolve()
    jar = pathlib.Path("/usr/local/lib/aonohako/alloy.jar")
    if not jar.exists():
        print(f"Alloy jar not found: {jar}", file=sys.stderr)
        return 1

    for candidate in receipt_candidates(source):
        if candidate.parent.exists() and candidate.parent.name == source.with_suffix("").name:
            shutil.rmtree(candidate.parent, ignore_errors=True)

    command = [
        "java",
        "-Xmx512m",
        "-Xss1m",
        "-XX:+UseSerialGC",
        "-jar",
        str(jar),
        "exec",
        str(source),
    ]
    result = subprocess.run(command, check=False)
    if result.returncode != 0:
        return result.returncode

    receipt = next((path for path in receipt_candidates(source) if path.exists()), None)
    if receipt is None:
        return 0

    with receipt.open("r", encoding="utf-8") as handle:
        data = json.load(handle)

    failed = False
    for name, command_data in sorted(data.get("commands", {}).items()):
        command_type = command_data.get("type")
        has_solution = bool(command_data.get("solution"))
        if command_type == "check" and has_solution:
            print(f"Alloy check {name} has a counterexample", file=sys.stderr)
            failed = True
        elif command_type == "run" and not has_solution:
            print(f"Alloy run {name} is unsatisfiable", file=sys.stderr)
            failed = True

    if receipt.parent.exists() and receipt.parent.is_dir():
        shutil.rmtree(receipt.parent, ignore_errors=True)
    return 1 if failed else 0


if __name__ == "__main__":
    os.environ.setdefault("JAVA_TOOL_OPTIONS", "-Djava.awt.headless=true")
    raise SystemExit(main())
