"""Exercise ordinary submissions with a candidate controller scope that excludes the original RMQ."""
import argparse
import gzip
import json
from pathlib import Path
import sys

import prepare
import replay

HERE, read, sha = prepare.HERE, prepare.read, prepare.sha
require = replay.require
AUTO = "AONOHAKO_DURANTE_JUDGE_AUTO"
CASES = "AONOHAKO_DURANTE_JUDGE_CASES"
CONTROLLERS = {"v12":"5013d7460f68c03b74f12e53d2efcc0b91a32614813a137638f8cad6f5c7055c",
               "v13":"7cf41d979890153a0615d208d3640f7aefc8b4b587c4fffff9201565a395ba56"}
WORKER_ENVS = {"v12":"9c6c9c4b2fbdf9a99fee3efb8474d88cc3073cc2369c8f152a4c408785684801",
               "v13":"8da46e71af0b1385feab4d6df8535424cc273919a27805af2720d13aa18b51ea"}


def settings_digest(env):
    return sha(json.dumps(env, sort_keys=True).encode())


def candidate(worker, version):
    require(settings_digest(worker) == WORKER_ENVS[version], "different sealed worker settings")
    old = json.loads(worker[CASES])
    require(old["schema"] == 1, "expected the original worker registry")
    shortest, = (c for c in old["cases"] if c["problem_id"] == "shortest_paths")
    require(shortest["time_ms"] == 65, "different historical control")
    scope = dict(schema=2, cases=[dict(**shortest, memory_mb=768, wall_time_ms=60000)])
    return {**worker, CASES:json.dumps(scope), AUTO:"on"}


def collect(args, plan, reg, env):
    version = args.engine
    clock, installed, engine = replay.contract(version, plan)
    require(sha(args.controller.read_bytes()) == CONTROLLERS[version], "different controller")
    for name, digest in installed.items():
        require(sha((args.runtime / name).read_bytes()) == digest, "different installed file " + name)
    for name in ("pilot/guest.py", "pilot/v12_guest.py", "pilot/scripts/verify_durante_observation.py",
                 "pilot/scripts/verify_durante_native_boundaries.py"):
        require(sha((args.payload / name).read_bytes()) == reg["payload"][name], "different request helper " + name)
    pilot = prepare.load("rmq_pilot_http", args.payload / "pilot/guest.py")
    from verify_durante_observation import server
    worker = argparse.Namespace(server=str(args.runtime / "server"), latency_bound=False, runtime_image=engine["image"])
    controller = argparse.Namespace(**{**vars(worker), "server":str(args.controller)})
    trials = []
    with server(worker, 18381, False, operator_env=env, umask=0o022) as upstream:
        with server(controller, 18382, False, upstream, operator_env=candidate(env, version), umask=0o022) as url:
            for name, case in sorted(plan["cases"].items()):
                elf = (args.payload / case["binary"]).read_bytes()
                require(sha(elf) == case["binary_sha256"], "different submission ELF")
                request = pilot.request_for("range_min", "native", 183, case["stdin"].encode(),
                                            case["expected_stdout"].encode(), elf, None)
                digest = sha(json.dumps(request).encode())
                require(not any(k.startswith("work_measurement") for k in request) and
                        digest == reg["requests"][name + "/contest/native"], "different ordinary request")
                receipt, stdout = pilot.post(url, "/execute", request)
                trials.append(dict(case=name, request_sha256=digest, stdout=stdout, receipt=receipt))
    return dict(schema=1, controller_sha256=CONTROLLERS[version], engine=version, image=engine["image"],
                model_binding=clock["model_binding"], worker_sha256=installed["server"],
                worker_env_sha256=settings_digest(env), controller_env_sha256=settings_digest(candidate(env, version)),
                trials=trials)


def check(version, document, plan, reg, worker_env):
    clock, installed, engine = replay.contract(version, plan)
    require(document["schema"] == 1 and document["engine"] == version and document["image"] == engine["image"]
            and document["controller_sha256"] == CONTROLLERS[version] and document["worker_sha256"] == installed["server"]
            and document["model_binding"] == clock["model_binding"] and document["worker_env_sha256"] == WORKER_ENVS[version]
            and document["controller_env_sha256"] == settings_digest(candidate(worker_env, version)),
            "different workers or settings")
    trials = document["trials"]
    require(len(trials) == len(plan["cases"]) and {r["case"] for r in trials} == plan["cases"].keys(),
            "incomplete ordinary requests")
    for row in trials:
        key, receipt, stdout = row["case"], row["receipt"], row["stdout"]
        case = plan["cases"][key]
        require(row["request_sha256"] == reg["requests"][key + "/contest/native"], "different request")
        replay.older.transfer.check_measured_cpu(receipt)
        replay.older.checks.check_native(dict(receipt=receipt, limit_ms=183, stdout_sha256=sha(stdout.encode())),
                                        sha(case["expected_stdout"].encode()))
        require((stdout in ("", case["solver_stdout"]) if receipt["status"] == replay.older.checks.TLE
                 else stdout == case["solver_stdout"]), "unexpected output")


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("engine", choices=replay.VERSIONS)
    action = cli.add_mutually_exclusive_group(required=True)
    action.add_argument("--verify", type=Path)
    action.add_argument("--output", type=Path)
    cli.add_argument("--worker-env", type=Path, required=True)
    cli.add_argument("--payload", type=Path)
    cli.add_argument("--runtime", type=Path, default=Path("/opt/durante"))
    cli.add_argument("--controller", type=Path)
    args = cli.parse_args()
    env = read(args.worker_env)
    root = HERE / args.engine
    plan, registration = read(root / "plan.json"), read(root / "registration.json")
    if args.output:
        if not args.payload or not args.controller:
            cli.error("collection requires --controller and --payload")
        # The authenticated pilot collector imports the historical worker helper by basename.
        sys.path.insert(0, str(args.payload / "pilot"))
        document = collect(args, plan, registration, env)
    else:
        document = json.loads(gzip.decompress(args.verify.read_bytes()))
    check(args.engine, document, plan, registration, env)
    if args.output:
        with args.output.open("xb") as stream:
            stream.write(gzip.compress((json.dumps(document, sort_keys=True) + "\n").encode(), mtime=0))
    print(f"{len(document['trials'])} ordinary native routing receipts verified on {args.engine}")
