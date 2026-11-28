"""Exercise the v12 public controller with ordinary requests or replay its recorded SSE results."""
import argparse
import gzip
import importlib.util
import json
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("v12_routing_replay", HERE / "replay.py")
replay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(replay)
checks, design, transfer = replay.checks, replay.design, replay.transfer
load, read, require, runtime_contract, sha = replay.load, replay.read, replay.require, replay.runtime_contract, replay.sha

CONTROLLER = "5013d7460f68c03b74f12e53d2efcc0b91a32614813a137638f8cad6f5c7055c"
WORKER_ENV = "9c6c9c4b2fbdf9a99fee3efb8474d88cc3073cc2369c8f152a4c408785684801"
AUTO = "AONOHAKO_DURANTE_JUDGE_AUTO"
REGISTRY = "AONOHAKO_DURANTE_JUDGE_CASES"


def settings_digest(env):
    return sha(json.dumps(env, sort_keys=True).encode())


def panel():
    plan = read(HERE / "plan.json")
    reg = read(HERE / "registration.json")
    trials = {key: {**c, "limit": 203, "request": reg["requests"][key + "/contest/native"]}
              for key, c in plan["cases"].items()}
    requests = read(design.TRANSFER / "registration.json")["plan"]["request_hashes"]
    corpus = read(design.PILOT / "registration.json")["corpus"]
    originals = {c["problem"]: c for c in read(design.PILOT / "refit/case_manifest.json")["cases"]
                 if c["role"] == "reference" and c["variant"] == "original"}
    archived = json.loads(checks.evidence(design.PILOT.parent / "scoped-auto-http", "receipts.json"))
    for row in archived["trials"]:
        problem, role = row["problem"], row["role"]
        if row["route"] != "automatic" or problem not in ("range_min", "shortest_paths"):
            continue
        key = problem + "-" + role
        trials[key] = {**originals[problem], "role": role, "binary": "pilot/corpus/" + key,
                       "binary_sha256": corpus[problem]["elf_sha256"][role], "limit": transfer.TIMES[problem],
                       "solver_stdout": row["stdout"], "request": requests["v12/native/" + key + "-contest"]}
    require(len(trials) == 11, "incomplete v12 routing panel")
    return trials


def check(document, env):
    selected = panel()
    clock, installed, engine = runtime_contract(read(HERE / "plan.json"))
    scope = json.loads(env[REGISTRY])
    expected = [{"problem_id": name, "time_ms": transfer.TIMES[name], "source_language": "CPP20",
                 "stdin_sha256": sha(selected[name + "-fast"]["stdin"].encode()), "model_binding": clock["model_binding"],
                 "memory_mb": checks.NOMINAL["memory_mb"], "wall_time_ms": checks.NOMINAL["wall_time_ms"]}
                for name in ("range_min", "shortest_paths")]
    require(scope["schema"] == 2 and sorted(scope["cases"], key=lambda r: r["problem_id"]) == expected
            and document["schema"] == 1 and document["controller_sha256"] == CONTROLLER
            and document["image"] == engine["image"] and document["worker_sha256"] == installed["server"]
            and document["controller_env_sha256"] == settings_digest({**env, AUTO: "on"})
            and document["worker_env_sha256"] == WORKER_ENV and document["model_binding"] == clock["model_binding"],
            "different controller, sealed worker, model or public scope")
    require(len(document["trials"]) == 11 and {r["case"] for r in document["trials"]} == selected.keys(),
            "incomplete ordinary routing receipts")
    for row in document["trials"]:
        c = selected[row["case"]]
        receipt, stdout = row["receipt"], row["stdout"]
        oracle = sha(c["expected_stdout"].encode())
        sample = dict(receipt=receipt, limit_ms=c["limit"], stdout_sha256=sha(stdout.encode()))
        require(row["binary_sha256"] == c["binary_sha256"] and row["request_sha256"] == c["request"], "different input")
        transfer.check_measured_cpu(receipt)
        if c["problem"] == "inversions":
            checks.check_native(sample, oracle)
            require((stdout in ("", c["solver_stdout"]) if receipt["status"] == checks.TLE else stdout == c["solver_stdout"]),
                    "different native solver output")
        else:
            require(receipt["status"] == checks.EXPECTED[c["role"]] and stdout == c["solver_stdout"]
                    and 0 < receipt["memory_kb"] <= checks.NOMINAL["memory_mb"] * 1024, "unexpected metered result")
            checks.check_judge(sample, clock, {"installed_files": installed}, oracle)
            transfer.check_install(receipt, engine)
            if c["role"] == "slow":
                require(receipt["work_measurement_execution"]["termination_source"] == "durante_work_clock",
                        "missing in-flight clock cutoff")


def collect(args, env):
    clock, installed, engine = runtime_contract(read(HERE / "plan.json"))
    reg = read(HERE / "registration.json")
    worker_env = read(args.worker_env)
    require(sha(args.controller.read_bytes()) == CONTROLLER and settings_digest(worker_env) == WORKER_ENV,
            "different control plane or sealed settings")
    for name, digest in engine["files"].items():
        require(sha((args.runtime / name).read_bytes()) == digest, "different installed file " + name)
    for path in ("pilot/guest.py", "pilot/v12_guest.py", "pilot/scripts/verify_durante_observation.py",
                 "pilot/scripts/verify_durante_native_boundaries.py"):
        require(sha((args.payload / path).read_bytes()) == reg["payload"][path], "different request helper")
    sys.path.insert(0, str(args.payload / "pilot"))
    pilot = load("v12_routing_pilot", args.payload / "pilot/guest.py")
    from verify_durante_observation import server
    worker = argparse.Namespace(server=str(args.runtime / "server"), latency_bound=False, runtime_image=engine["image"])
    public = argparse.Namespace(**{**vars(worker), "server": str(args.controller)})
    trials = []
    with server(worker, 18381, False, operator_env=worker_env, umask=0o022) as upstream:
        with server(public, 18382, False, upstream, operator_env={**env, AUTO: "on"}, umask=0o022) as url:
            for key, c in panel().items():
                root = args.payload if c["problem"] == "inversions" else args.pilot_payload
                elf = (root / c["binary"]).read_bytes()
                require(sha(elf) == c["binary_sha256"], "different ELF " + key)
                req = pilot.request_for(c["problem"], "native", c["limit"], c["stdin"].encode(),
                                        c["expected_stdout"].encode(), elf, None)
                digest = sha(json.dumps(req).encode())
                require(digest == c["request"] and not any(k.startswith("work_measurement") for k in req),
                        "different ordinary request " + key)
                receipt, stdout = pilot.post(url, "/execute", req)
                trials.append(dict(case=key, request_sha256=digest, binary_sha256=c["binary_sha256"], receipt=receipt,
                                   stdout=stdout))
    return dict(schema=1, controller_sha256=CONTROLLER, worker_sha256=installed["server"], image=engine["image"],
                worker_env_sha256=WORKER_ENV, controller_env_sha256=settings_digest({**env, AUTO: "on"}),
                model_binding=clock["model_binding"], trials=trials)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    mode = cli.add_mutually_exclusive_group(required=True)
    mode.add_argument("--verify", type=Path, help="replay a .json.gz receipt")
    mode.add_argument("--output", type=Path, help="write a new .json.gz receipt from an offline container")
    cli.add_argument("--controller-env", required=True, type=Path)
    cli.add_argument("--worker-env", type=Path)
    cli.add_argument("--controller", type=Path)
    cli.add_argument("--runtime", default=Path("/opt/durante"), type=Path)
    cli.add_argument("--payload", type=Path, help="registered v12 boundary payload")
    cli.add_argument("--pilot-payload", type=Path, help="pilot payload with the two retained problems")
    args = cli.parse_args()
    env = read(args.controller_env)
    if args.output and not all((args.controller, args.worker_env, args.payload, args.pilot_payload)):
        cli.error("live collection requires both payloads, --controller and --worker-env")
    document = collect(args, env) if args.output else json.loads(gzip.decompress(args.verify.read_bytes()))
    check(document, env)
    if args.output:
        with args.output.open("xb") as dest:
            dest.write(gzip.compress((json.dumps(document, sort_keys=True) + "\n").encode(), mtime=0))
    print("11 ordinary v12 routing receipts verified")
