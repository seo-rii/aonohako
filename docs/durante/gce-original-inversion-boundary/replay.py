"""Replay the two independent 203 ms runs against the pre-registered disqualification rule."""
import argparse
from collections import Counter
import importlib.util
import json
from pathlib import Path
import re
from statistics import median
import sys
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
PREVIOUS = HERE.parent / "gce-inversion-clock"
FROZEN_COMMIT = "42ece96bc41072e5d61320e066f37707231df2a8"
FROZEN_REGISTRATION = "c582da36e7b37b668a474b1aa8f50b3b6aafbc688fba0a4b01f9123842b19ade"


def load(name, source):
    spec = importlib.util.spec_from_file_location(name, source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_checks():
    # The archived verifier uses flat imports. Keep its private guest, plan and
    # protocol modules separate from any already imported by the caller.
    names = ("guest", "plan", "protocol", "audit_transfer_receipts", "corpus")
    saved = {k: sys.modules.pop(k) for k in names if k in sys.modules}
    search_path = sys.path[:]
    try:
        module = load("original_boundary_receipt_checks", PREVIOUS / "replay.py")
    finally:
        sys.path[:] = search_path
        for k in names:
            sys.modules.pop(k, None)
        sys.modules.update(saved)
    fixed = load("original_boundary_guest", HERE / "guest.py")
    module.ARMS = ("shared",)
    module.guest = SimpleNamespace(schedule=fixed.schedule, cells=module.guest.cells, clock_policy=module.guest.clock_policy)
    return module


base = load_checks()
require = base.require


def summarize(plan, guests):
    panels, repeated = {}, []
    controls = True
    for phase in base.PHASES:
        panel = panels[phase] = {}
        for key, case in sorted(plan["cases"].items()):
            groups = guests[phase][key, "contest"]
            native = [r["receipt"]["status"] for r in groups["native"]]
            metered = [r["receipt"]["status"] for r in groups["shared"]]
            require((len(native), len(metered)) == (3, 2), "incomplete contest cell")
            outcome = base.transfer.classify(native, metered)
            sources = Counter(r["receipt"]["work_measurement_execution"]["termination_source"] for r in groups["shared"])
            cell = panel[key] = {"outcome": outcome, "native": dict(Counter(native)), "clock": dict(Counter(metered)),
                                 "clock_termination": dict(sources)}
            if case["role"] in base.checks.EXPECTED:
                controls &= set(native + metered) == {base.checks.EXPECTED[case["role"]]}
                if case["role"] == "slow":
                    controls &= all(r["receipt"]["work_measurement_execution"]["termination_source"] ==
                                    "durante_work_clock" for r in groups["shared"])
            if "ample" in case["limits"]:
                ample = guests[phase][key, "ample"]
                received = [r["receipt"] for r in ample["native"] + ample["shared"]]
                expected = base.checks.AC if case["solver_stdout"] == case["expected_stdout"] else base.checks.WA
                require(len(ample["native"]) == 3 and len(ample["shared"]) == 2 and
                        all(r["status"] == expected for r in received), "incomplete ample controls")
                cpu = median(r["receipt"]["cpu_time_ns"] for r in ample["native"])
                estimate = median(r["receipt"]["work_measurement_execution"]["time_estimate"]["nanoseconds"]
                                  for r in ample["shared"])
                cell.update(ample_native_median_ns=cpu, ample_estimated_median_ns=estimate,
                            signed_error_pct=round(100 * (estimate / cpu - 1), 3))
    for key, case in sorted(plan["cases"].items()):
        if case["split"] == "variant":
            outcome = panels["primary"][key]["outcome"]
            if outcome in ("false_pass", "false_timeout") and outcome == panels["repeat"][key]["outcome"]:
                repeated.append({"case": key, "direction": outcome})
    return {"schema": 1, "controls_passed": controls, "disqualified": bool(controls and repeated),
            "repeated_opposition": repeated, "panels": panels}


def verify(root, payload=None):
    plan = base.read(HERE / "plan.json")
    reg = base.read(HERE / "registration.json")
    require(base.sha((HERE / "registration.json").read_bytes()) == FROZEN_REGISTRATION, "different registration")
    require(all(base.sha((HERE / name).read_bytes()) == reg["payload"][name]
                for name in ("plan.json", "guest.py", "guest.sh")), "different frozen experiment")
    require(base.sha((HERE / "variants.cpp").read_bytes()) == plan["build"]["source_sha256"] and
            base.sha((base.transfer.PILOT / "corpus/generator.hpp").read_bytes()) == plan["build"]["generator_sha256"],
            "different variant or generator source")
    previous = base.read(PREVIOUS / "registration.json")["payload"]
    for member, digest in reg["payload"].items():
        if member in ("plan.py", "runtime.squashfs", "installation.json") or member.startswith("pilot/") and member in previous:
            require(digest == previous[member], "different inherited runtime or executable")
    require(reg["payload"]["inversion_guest.py"] == previous["guest.py"] == base.sha((PREVIOUS / "guest.py").read_bytes())
            and reg["payload"]["plan.py"] == base.sha((PREVIOUS / "plan.py").read_bytes()), "different inherited collector")
    require(all(reg["payload"][case["binary"]] == case["binary_sha256"] for case in plan["cases"].values()),
            "different contestant executable in registration")
    hashes = base.read(root / "request_hashes.json")
    keys = {"/".join((k, boundary, arm)) for k, case in plan["cases"].items() for boundary in case["limits"]
            for arm in ("native", "shared")}
    require(set(hashes) == keys and all(re.fullmatch("[0-9a-f]{64}", v) for v in hashes.values()),
            "different request digest index")
    if payload is not None:
        require(hashes == base.request_hashes(plan, {}, payload), "different requests for authenticated binaries")
    infra = base.read(root / "infrastructure.json")
    base.transfer.check_topology(infra, reg)
    for prior in (base.TRANSFER, PREVIOUS):
        old = base.read(prior / "evidence/infrastructure.json")
        for field in ("instance_id_sha256", "disk_id_sha256", "boot_sha256"):
            require({r[field] for r in infra.values()}.isdisjoint(r[field] for r in old.values()),
                    "reused earlier guest identity")
    for cloud in infra.values():
        require(cloud["registration_commit"] == FROZEN_COMMIT and cloud["guest_exit"] == 0 and
                cloud["trial_count"] == reg["expected_rows"]["clock"] == 115 and cloud["groups"] == {"clock": 115}
                and cloud["temporary_key_removed"] is True and cloud["raw_archive_bytes"] > 0 and
                re.fullmatch("[0-9a-f]{64}", cloud["raw_archive_sha256"]) and
                cloud["files"]["clock/thresholds.json"] == reg["payload"]["primary-thresholds.json"],
                "incomplete lifecycle or altered fixed deadlines")
    guests = {phase: base.verify_guest(root, phase, plan, reg, {}, infra, hashes) for phase in base.PHASES}
    result = summarize(plan, guests)
    result.update(original_input_sha256=plan["original_input_sha256"], clock_binding=plan["baseline_binding"],
                  requests_per_boot=115)
    return result


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--evidence", type=Path, default=HERE / "evidence")
    cli.add_argument("--payload", type=Path, help="reconstruct request digests from the authenticated ELF files")
    args = cli.parse_args()
    print(json.dumps(verify(args.evidence, args.payload), indent=2, sort_keys=True, allow_nan=False))
