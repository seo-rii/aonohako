"""Run ordinary HTTP requests through the two-case controller or replay their receipts."""
import argparse
import gzip
import json
from pathlib import Path
import sys

from replay import HERE, base, load, require

CONTROLLER_SHA = "7cf41d979890153a0615d208d3640f7aefc8b4b587c4fffff9201565a395ba56"
SCOPE = "AONOHAKO_DURANTE_JUDGE_CASES"
AUTO = "AONOHAKO_DURANTE_JUDGE_AUTO"
RETAINED = ("range_min", "shortest_paths")


def digest_settings(settings):
    return base.sha(json.dumps(settings, sort_keys=True).encode())


def cases():
    plan = base.read(HERE / "plan.json")
    hashes = base.read(HERE / "evidence/request_hashes.json")
    selected = {}
    for key, case in plan["cases"].items():
        selected[key] = {**case, "limit": 203, "request": hashes[key + "/contest/native"]}
    require(len(selected) == 12, "expected all twelve original inversion submissions")
    archive = base.transfer.HERE
    prior = json.loads(base.checks.evidence(archive / "evidence", "automatic-routing.json"))
    answers = {(r["problem"], r["role"]): r["stdout"] for r in prior["trials"]}
    canonical = {c["problem"]: c for c in base.read(base.transfer.PILOT / "refit/case_manifest.json")["cases"]
                 if c["variant"] == "original" and c["role"] == "reference"}
    binaries = base.read(base.transfer.PILOT / "registration.json")["corpus"]
    requests = base.read(archive / "registration.json")["plan"]["request_hashes"]
    for problem in RETAINED:
        for role in ("fast", "wrong", "slow"):
            key = problem + "-" + role
            selected[key] = {**canonical[problem], "role": role, "limit": base.transfer.TIMES[problem],
                             "binary": "pilot/corpus/" + key, "binary_sha256": binaries[problem]["elf_sha256"][role],
                             "request": requests["v12/native/" + key + "-contest"],
                             "solver_stdout": answers[problem, role]}
    return selected, prior["env_sha256"]


def check(document, controller_env):
    selected, worker_env_hash = cases()
    worker = base.read_clock_plan()
    clock = base.bound_clocks(base.read(HERE / "plan.json"), {})["shared"]
    expected_scope = [{"problem_id": p, "source_language": "CPP20",
                       "stdin_sha256": base.sha(selected[p + "-fast"]["stdin"].encode()),
                       "time_ms": base.transfer.TIMES[p], "memory_mb": base.checks.NOMINAL["memory_mb"],
                       "wall_time_ms": base.checks.NOMINAL["wall_time_ms"], "model_binding": worker["binding"]}
                      for p in RETAINED]
    registry = json.loads(controller_env[SCOPE])
    require(registry["schema"] == 2 and sorted(registry["cases"], key=lambda r: r["problem_id"]) == expected_scope,
            "different public case registry")
    require(document["schema"] == 1 and document["controller_sha256"] == CONTROLLER_SHA
            and document["worker_sha256"] == worker["files"]["server"] and document["image"] == worker["image"]
            and document["worker_env_sha256"] == worker_env_hash and document["model_binding"] == worker["binding"]
            and document["controller_env_sha256"] == digest_settings({**controller_env, AUTO: "on"}),
            "different controller, sealed worker, clock or environment")
    trials = document["trials"]
    require(len(trials) == len(selected) and {r["case"] for r in trials} == selected.keys(),
            "missing or duplicate ordinary HTTP requests")
    for trial in trials:
        case = selected[trial["case"]]
        receipt, stdout = trial["receipt"], trial["stdout"]
        require(trial["request_sha256"] == case["request"] and trial["binary_sha256"] == case["binary_sha256"],
                "unregistered request or executable")
        row = dict(receipt=receipt, limit_ms=case["limit"], stdout_sha256=base.sha(stdout.encode()))
        oracle = base.sha(case["expected_stdout"].encode())
        base.transfer.check_measured_cpu(receipt)
        if case["problem"] == "inversions":
            base.checks.check_native(row, oracle)
            require((stdout in ("", case["solver_stdout"]) if receipt["status"] == base.checks.TLE
                     else stdout == case["solver_stdout"]), "incorrect native solver output")
        else:
            require(receipt["status"] == base.checks.EXPECTED[case["role"]] and stdout == case["solver_stdout"]
                    and 0 < receipt["memory_kb"] <= base.checks.NOMINAL["memory_mb"] * 1024,
                    "incorrect retained verdict, output or memory")
            base.checks.check_judge(row, clock, {"installed_files": worker["files"]}, oracle)
            base.transfer.check_install(receipt, worker)
            if case["role"] == "slow":
                require(receipt["work_measurement_execution"]["termination_source"] == "durante_work_clock",
                        "the deciding clock did not interrupt the slow submission")


def collect(payload, pilot_payload, controller, controller_env, worker_env, runtime):
    selected, worker_env_hash = cases()
    worker = base.read_clock_plan()
    require(base.sha(controller.read_bytes()) == CONTROLLER_SHA and digest_settings(worker_env) == worker_env_hash,
            "unexpected controller or sealed worker settings")
    for name, digest in worker["files"].items():
        require(base.sha((runtime / name).read_bytes()) == digest, "unexpected sealed worker file " + name)
    reg = base.read(HERE / "registration.json")
    for name in ("pilot/guest.py", "pilot/v12_guest.py", "pilot/scripts/verify_durante_observation.py",
                 "pilot/scripts/verify_durante_native_boundaries.py"):
        require(base.sha((payload / name).read_bytes()) == reg["payload"][name], "different request helper")
    sys.path.insert(0, str(payload / "pilot"))
    pilot = load("routing_pilot", payload / "pilot/guest.py")
    from verify_durante_observation import server
    document = dict(schema=1, controller_sha256=CONTROLLER_SHA, worker_sha256=worker["files"]["server"],
                    worker_env_sha256=worker_env_hash, controller_env_sha256=digest_settings({**controller_env, AUTO: "on"}),
                    image=worker["image"], model_binding=worker["binding"], trials=[])
    args = argparse.Namespace(runtime_image=worker["image"], latency_bound=False, server=str(runtime / "server"))
    with server(args, 18381, False, operator_env=worker_env, umask=0o022) as worker_url:
        args = argparse.Namespace(**{**vars(args), "server": str(controller)})
        with server(args, 18382, False, worker_url, operator_env={**controller_env, AUTO: "on"}, umask=0o022) as public:
            for key, case in selected.items():
                binary_root = payload if case["problem"] == "inversions" else pilot_payload
                binary = (binary_root / case["binary"]).read_bytes()
                require(base.sha(binary) == case["binary_sha256"], "different ELF " + key)
                req = pilot.request_for(case["problem"], "native", case["limit"], case["stdin"].encode(),
                                        case["expected_stdout"].encode(), binary, None)
                request_hash = base.sha(json.dumps(req).encode())
                require(request_hash == case["request"] and not any(k.startswith("work_measurement") for k in req),
                        "different ordinary request " + key)
                receipt, stdout = pilot.post(public, "/execute", req)
                document["trials"].append(dict(case=key, request_sha256=request_hash, binary_sha256=case["binary_sha256"],
                                               receipt=receipt, stdout=stdout))
    check(document, controller_env)
    return document


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    mode = cli.add_mutually_exclusive_group(required=True)
    mode.add_argument("--verify", type=Path, help="check a recorded .json.gz document")
    mode.add_argument("--output", type=Path, help="new .json.gz document for a live check")
    cli.add_argument("--controller-env", required=True, type=Path)
    cli.add_argument("--worker-env", type=Path)
    cli.add_argument("--controller", type=Path)
    cli.add_argument("--runtime", type=Path, default=Path("/opt/durante"))
    cli.add_argument("--payload", type=Path, help="registered original-boundary payload")
    cli.add_argument("--pilot-payload", type=Path, help="sealed v13 transfer payload with retained controls")
    args = cli.parse_args()
    settings = base.read(args.controller_env)
    if args.verify:
        check(json.loads(gzip.decompress(args.verify.read_bytes())), settings)
    else:
        if not (args.controller and args.worker_env and args.payload and args.pilot_payload):
            cli.error("live collection requires --controller, --worker-env, --payload and --pilot-payload")
        data = collect(args.payload, args.pilot_payload, args.controller, settings, base.read(args.worker_env), args.runtime)
        with args.output.open("xb") as out:
            out.write(gzip.compress((json.dumps(data, sort_keys=True) + "\n").encode(), mtime=0))
    print("18 ordinary routing receipts verified")
