"""Replay the pinned Durante footprint study through Aonohako's input-deadline CLI."""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile

COLLECTION_SHA256 = "a06944828ec6f11f52baeb71835dd1355ef54ee76c319a854155ac4ac8dd4dad"


def checked_bytes(path, digest):
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != digest:
        raise ValueError(f"SHA-256 mismatch for {path}")
    return data


def replay(args, tmp):
    experiments = args.durante / "experiments"
    data = experiments / "results/gce-footprint-admission"
    manifest = json.loads(checked_bytes(data / "collection.json", COLLECTION_SHA256))
    for name, digest in manifest["input_sources_sha256"].items():
        checked_bytes(args.durante / name, digest)
    archive = tmp / "receipts.tar.xz"
    archive.write_bytes(checked_bytes(data / "output.tar.xz", manifest["output_sha256"]))

    sys.path.insert(0, str(experiments))
    from footprint_admission import verify
    from shape_calibration import aonohako_clock, verify_combined

    root = verify_combined.unpack(archive, tmp)
    metrics = verify.verify(root)
    plan = verify.load(experiments / "footprint_admission/plan.json")
    counts = {method: Counter() for method in verify.METHODS}
    for case in plan["cases"]:
        if case["split"] != "holdout":
            continue
        name = case["case_id"]
        capture = root / "cases" / name
        elf = args.binaries / case["program"]
        stdin = capture / "full/stdin"
        for method in verify.METHODS:
            arm = metrics["cases"][name][method]
            policy = root / "cases" / arm["anchor_case"] / "policy.json"
            selection = capture / f"{method}-selection.json"
            projection = aonohako_clock.export(selection, capture, policy, elf, stdin, case["limits_ms"])
            projected = tmp / "projection.json"
            projected.write_text(json.dumps(projection))
            for index, (ms, event) in enumerate(zip(case["limits_ms"], arm["live"], strict=True)):
                live = capture / "limits" / method / f"{index:02d}" / "result.json"
                command = [args.reference, "replay-input-deadline", "--projection", projected,
                           "--live", live, "--elf", elf, "--stdin", stdin, "--limit-ms", str(ms)]
                result = subprocess.run(command, capture_output=True, text=True, check=False)
                if result.returncode:
                    raise ValueError(f"{name} {method} {ms} ms: {result.stderr.strip()}")
                status = "budget_exceeded" if event["tle"] else "within_budget"
                expected = dict(schema=1, authority="diagnostic_only", selection_sha256=arm["selection_sha256"],
                                policy_sha256=verify.sha(policy.read_bytes()), limit_ms=ms, pid=event["pid"],
                                status=status, exceeded=event["tle"])
                if event["limit_ms"] != ms or json.loads(result.stdout) != expected:
                    raise ValueError(f"Go result differs from the verified capture: {name} {method} {ms} ms")
                counts[method][status] += 1
    totals = metrics["totals"]["all"]
    for method, count in counts.items():
        if count.total() != totals[method]["limits"] or count["budget_exceeded"] != totals[method]["live_tle"]:
            raise ValueError(f"incomplete {method} replay")
    return dict(archive_sha256=manifest["output_sha256"], inputs=totals["inputs"], decisions=counts)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--durante", type=Path, required=True, help="trusted Durante source checkout")
    cli.add_argument("--binaries", type=Path, required=True, help="directory of the three registered static ELF files")
    cli.add_argument("--reference", type=Path, required=True, help="built Aonohako durante-reference executable")
    args = cli.parse_args()
    args.reference = args.reference.resolve()
    try:
        with tempfile.TemporaryDirectory() as directory:
            print(json.dumps(replay(args, Path(directory)), indent=2))
    except (OSError, ValueError, KeyError, TypeError) as error:
        cli.exit(1, f"replay failed: {error}\n")
