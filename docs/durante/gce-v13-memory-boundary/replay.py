"""Verify the independent CPU-pinned memory controls and summarize their peak RSS."""
from collections import defaultdict
import importlib.util
import json
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
TRANSFER = HERE.parent / "gce-v13-judge-transfer"
sys.path.insert(0, str(TRANSFER))
spec = importlib.util.spec_from_file_location("memory_transfer_checks", TRANSFER / "verify.py")
transfer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(transfer)
checks = transfer.V13
require = checks.require
PHASES = ("primary", "repeat")
SIZES = (32, 720, 764, 768, 816)
ARCHIVE = "1a640d0de453bb45de2dfc02006e8e3b8ef254c24ed3b90fa190d244d70b3aea"
REGISTRATION = "a5a98052e36d7b0dd5e88d3e5fb2c230e51c8695"
ELF = "3f947056dd48a8230d6fda7ad825591bc722337234cadd96acfc2906b2a752a6"


def check_raw(receipt, worker, patches, stdout, oracle):
    report = receipt["work_measurement"]
    checks.check_report(report, {"installed_files": worker["files"]}, complete=True)
    transfer.check_install(receipt, {**worker, "patches": patches})
    run = receipt["work_measurement_execution"]
    clock = run["clock"]
    counts = transfer.count_tuple(report)
    require(receipt["status"] == "Container Initialization Failed" and receipt.get("score") is None
            and receipt.get("work_measurement_judgment") is None and receipt["exit_code"] == 0
            and run["authority"] == "diagnostic_only" and run["state"] == "complete"
            and run["output_matches"] is True and stdout == oracle and run["termination_source"] == "stdout"
            and run["nominal_limits"] == checks.NOMINAL and run["raw_limits"] == checks.HARD
            and 0 < receipt["memory_kb"] <= 1024 * checks.HARD["memory_mb"]
            and 0 < receipt["wall_time_ms"] <= checks.HARD["wall_time_ms"]
            and receipt["cpu_time_ns"] <= checks.HARD["cpu_time_ms"] * 1_000_000,
            "ineligible unscored diagnostic")
    require(clock == {"version": 1, "metric": "instructions_plus_movs_stos_bytes_v1", "budget": 2**63 - 1,
                      "state": "observed", "progress": dict(zip(("instructions", "movs_bytes", "stos_bytes"), counts))},
            "raw clock disagrees with terminal counters")


def replay(root):
    infra = json.loads((root / "infrastructure.json").read_bytes())
    transfer.check_topology(infra, {"archive_sha256": ARCHIVE})
    plan = json.loads((TRANSFER / "registration.json").read_bytes())["plan"]
    worker = plan["arms"]["v13"]
    upstream = json.loads((HERE.parents[2] / "third_party/durante-v13/UPSTREAM.json").read_bytes())
    require(upstream["commit"] == worker["upstream"], "changed vendor pin")
    patches = {name: upstream["files"]["toolkit/patches/" + filename] for name, filename in (
        ("lifetime", "dynamorio-vfork-lifetime.patch"), ("replay", "dynamorio-code-replay.patch"))}
    hashes = json.loads((root / "requests.json").read_bytes())
    grouped = defaultdict(lambda: defaultdict(list))
    for phase in PHASES:
        machine = infra[phase]
        require(machine["registration_commit"] == REGISTRATION and machine["guest_exit"] == 0
                and machine["temporary_key_removed"] is True and machine["groups"]["memory"] == 20,
                "different registration, incomplete guest or missing cleanup")
        files = {name: checks.evidence(root / phase, name)
                 for name in ("trials.jsonl", "platform.json", "exec-trace.txt")}
        for name, data in files.items():
            require(checks.sha(data) == machine["files"]["memory/" + name], "memory file digest mismatch")
        host = json.loads(files["platform.json"])
        require(host["phase"] == phase and host["boot_sha256"] == machine["boot_sha256"] and host["affinity"] == [1]
                and host["image"] == worker["image"] and host["server_sha256"] == worker["files"]["server"]
                and host["binary_sha256"] == ELF and host["trial_count"] == 20, "unbound memory worker")
        rows = [json.loads(line) for line in files["trials.jsonl"].splitlines()]
        windows = checks.read_windows(files["exec-trace.txt"])
        scheduled = []
        for passes in (1, 8):
            reverse = (passes == 8) != (phase == "repeat")
            for mib in reversed(SIZES) if reverse else SIZES:
                for arm in (("raw", "native") if reverse else ("native", "raw")):
                    scheduled.append((mib, passes, arm))
        for i, (row, window, key) in enumerate(zip(rows, windows, scheduled, strict=True)):
            mib, passes, kind = key
            require((row["size_mib"], row["rounds"], row["kind"]) == key and row["sequence"] == i
                    and row["request_sha256"] == hashes[f"{mib}/{passes}/{kind}"], "wrong memory trial or request")
            checks.check_placement(row, window)
            receipt = row["receipt"]
            transfer.check_measured_cpu(receipt)
            oracle = f"{mib * 256 * passes * (passes + 1) // 2}\n"
            if kind == "raw":
                check_raw(receipt, worker, patches, row["stdout"], oracle)
            else:
                require(not any(k.startswith("work_measurement") for k in receipt), "native run was metered")
                if receipt["status"] == "Accepted":
                    require(receipt["verdict_source"] == "stdout" and row["stdout"] == oracle
                            and receipt["exit_code"] == 0 and 0 <= receipt["memory_kb"] <= checks.NOMINAL["memory_mb"] * 1024,
                            "inconsistent native answer or resources")
                else:
                    require(receipt["status"] == "Memory Limit Exceeded" and receipt["verdict_source"] in
                            ("memory_rss", "memory_reported") and receipt["memory_kb"] > checks.NOMINAL["memory_mb"] * 1024,
                            "unverified native memory stop")
            grouped[mib][kind].append((receipt["status"], receipt["memory_kb"]))
    return {size: {kind: {"statuses": sorted({s for s, _ in values}), "samples": len(values),
                          "min_kib": min(v for _, v in values), "max_kib": max(v for _, v in values)}
                   for kind, values in kinds.items()} for size, kinds in sorted(grouped.items())}


if __name__ == "__main__":
    print(json.dumps(replay(HERE / "evidence"), indent=2))
