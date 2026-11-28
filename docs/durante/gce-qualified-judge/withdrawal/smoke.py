"""Check native fallback and explicit judge denial through the public mixed-version /execute endpoint."""
import argparse
import base64
import gzip
import importlib.util
import json
from pathlib import Path
import sys
import urllib.error

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent
ROOT = PILOT.parent
REGISTRY = "AONOHAKO_DURANTE_JUDGE_CASES"
AUTO = "AONOHAKO_DURANTE_JUDGE_AUTO"
CONTROLLERS = {"v12": "c6660e5962f5f9805ff5b52ee1b6bae2102b003cae674b8bc705d83714ebc584",
               "v13": "df468a55d46f4c18eb8bfc0dca54df90a3e7c1fe03cdba2e55df59a0bc720a66"}
DENIAL = {"error": "invalid_request",
          "message": "no judge case matches the problem, language, stdin, nominal limits and effective clock model"}


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


legacy = load("withdrawal_receipt_checks", ROOT / "gce-original-inversion-boundary/v12/replay.py")
exporter = load("withdrawal_exporter", PILOT / "controller_env.py")
require, read, sha = legacy.require, legacy.read, legacy.sha


def digest_env(env):
    return sha(json.dumps(env, sort_keys=True).encode())


def cases(studies):
    inversion = ROOT / "gce-original-inversion-boundary"
    rmq = studies / "gce-original-range-min-boundary/v13"
    paths = read(studies / "gce-shortest-paths-boundary/registration.json")
    inputs = [
        ("inversions", read(inversion / "plan.json"), read(inversion / "evidence/request_hashes.json"),
         ["original-" + r for r in ("fast","wrong","slow","hybrid512","hybrid1024","sqrt512","sqrt1024")]),
        ("range_min", read(rmq / "plan.json"), read(rmq / "registration.json")["requests"],
         ["fast","wrong","slow","cached4","edges256","edges512"]),
        ("shortest_paths", paths["plans"]["v13"], paths["requests"]["v13"],
         ["fast","wrong","slow","block8","block2048"]),
    ]
    selected = {}
    for problem, plan, requests, keys in inputs:
        for key in keys:
            case = plan["cases"][key]
            require(case["problem"] == problem, "different registered problem")
            name = Path(case["binary"]).name
            selected[name] = dict(case, ordinary_sha256=requests[key + "/contest/native"])
    require(len(selected) == 18, "incomplete withdrawal panel")
    return selected


def body(case, binary, explicit):
    require(sha(binary) == case["binary_sha256"], "unexpected ELF")
    artifact = {"name": "Main", "mode": "exec", "data_b64": base64.b64encode(binary).decode()}
    request = dict(problem_id=case["problem"], lang="CPP20", binaries=[artifact], stdin=case["stdin"],
                   expected_stdout=case["expected_stdout"], limits={**legacy.checks.NOMINAL,
                   "time_ms": case["limits"]["contest"]}, emit_logs=True)
    if explicit:
        # This is a public opt-in. The client supplies neither a private case-required bit nor a binding.
        request.update(work_measurement=True, work_measurement_limits=legacy.checks.HARD,
                       work_measurement_time=True, work_measurement_judge=True)
    return request


def check(document, version, env, selected, binaries=None):
    worker = read(ROOT / "gce-v13-judge-transfer/registration.json")["plan"]["arms"][version]
    require(json.loads(env[REGISTRY]) == {"schema": 2, "cases": []}, "expected an explicitly empty scope")
    require(document["schema"] == 1 and document["engine"] == version and document["image"] == worker["image"]
            and document["worker_sha256"] == worker["files"]["server"] and document["controller_sha256"] == CONTROLLERS[version]
            and document["public_env_sha256"] == digest_env({**env, AUTO: "on"})
            and document["worker_env_sha256"] == {"v12": "9c6c9c4b2fbdf9a99fee3efb8474d88cc3073cc2369c8f152a4c408785684801",
                "v13": "8da46e71af0b1385feab4d6df8535424cc273919a27805af2720d13aa18b51ea"}[version],
            "different controller, worker or settings")
    rows = document["trials"]
    require(len(rows) == len(selected) and {r["case"] for r in rows} == set(selected), "incomplete HTTP panel")
    for row in rows:
        name = row["case"]
        case = selected[name]
        receipt, stdout = row["receipt"], row["stdout"]
        require(row["ordinary_sha256"] == case["ordinary_sha256"] and row["binary_sha256"] == case["binary_sha256"]
                and row["denial"] == {"http": 400, "content_type": "application/json", "body": DENIAL},
                "unexpected ordinary request or public explicit judge response")
        if binaries:
            binary = (binaries / name).read_bytes()
            for field, explicit in (("ordinary_sha256", False), ("explicit_sha256", True)):
                require(row[field] == sha(json.dumps(body(case, binary, explicit)).encode()), "request differs")
        require(len(row["explicit_sha256"]) == 64 and row["ordinary_sha256"] != row["explicit_sha256"],
                "invalid explicit request digest")
        legacy.transfer.check_measured_cpu(receipt)
        legacy.checks.check_native(dict(receipt=receipt, limit_ms=case["limits"]["contest"], stdout_sha256=sha(stdout.encode())),
                                   sha(case["expected_stdout"].encode()))
        require(not any(k.startswith("work_measurement") for k in receipt), "unexpected metered evidence")
        if case["role"] in legacy.checks.EXPECTED:
            require(receipt["status"] == legacy.checks.EXPECTED[case["role"]], "unexpected original control verdict")
        if receipt["status"] == legacy.checks.TLE:
            require(stdout in ("", case["solver_stdout"]), "unexpected partial native output")
        else:
            require(stdout == case["solver_stdout"], "native answer differs from registered output")


def collect(args, env, selected):
    sys.path.insert(0, str(ROOT.parents[1] / "scripts"))
    from verify_durante_observation import post, server
    worker = read(ROOT / "gce-v13-judge-transfer/registration.json")["plan"]["arms"][args.engine]
    old_env = read(args.worker_env)
    require(json.loads(old_env[REGISTRY])["schema"] == 1 and
            {k:v for k,v in old_env.items() if k != REGISTRY} == {k:v for k,v in env.items() if k != REGISTRY},
            "different sealed worker environment")
    for name, digest in worker["files"].items():
        require(sha((args.runtime / name).read_bytes()) == digest, "different sealed installation")
    document = dict(schema=1, engine=args.engine, image=worker["image"], worker_sha256=worker["files"]["server"],
                    controller_sha256=sha(args.controller.read_bytes()), worker_env_sha256=digest_env(old_env),
                    public_env_sha256=digest_env({**env, AUTO: "on"}), trials=[])
    private = argparse.Namespace(server=str(args.runtime / "server"), latency_bound=False, runtime_image=worker["image"])
    public = argparse.Namespace(**{**vars(private), "server": str(args.controller)})
    with server(private, 18381, False, operator_env=old_env, umask=0o022) as upstream:
        with server(public, 18382, False, remote=upstream, operator_env={**env, AUTO: "on"}, umask=0o022) as url:
            for name, case in selected.items():
                binary = (args.binaries / name).read_bytes()
                ordinary, explicit = (body(case, binary, mode) for mode in (False, True))
                receipt, stdout = post(url, "/execute", ordinary)
                try:
                    post(url, "/execute", explicit)
                except urllib.error.HTTPError as error:
                    denial = {"http": error.code, "content_type": error.headers.get_content_type(), "body": json.load(error)}
                else:
                    raise ValueError("public explicit judging request produced a competitive result")
                document["trials"].append(dict(case=name, binary_sha256=sha(binary), receipt=receipt, stdout=stdout,
                    ordinary_sha256=sha(json.dumps(ordinary).encode()), explicit_sha256=sha(json.dumps(explicit).encode()),
                    denial=denial))
    return document


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("engine", choices=CONTROLLERS)
    group = cli.add_mutually_exclusive_group(required=True)
    group.add_argument("--verify", type=Path)
    group.add_argument("--output", type=Path)
    cli.add_argument("--studies", type=Path, default=ROOT, help="directory containing the two new boundary studies")
    cli.add_argument("--binaries", type=Path, help="original and counterexample ELFs, named as in the study manifests")
    cli.add_argument("--runtime", type=Path, default=Path("/opt/durante"))
    cli.add_argument("--controller", type=Path)
    cli.add_argument("--worker-env", type=Path)
    args = cli.parse_args()
    if args.output and not all((args.binaries, args.controller, args.worker_env)):
        cli.error("live collection requires --binaries, --controller and --worker-env")
    env = exporter.controller_environment(args.engine)
    selected = cases(args.studies)
    doc = json.loads(gzip.decompress(args.verify.read_bytes())) if args.verify else collect(args, env, selected)
    check(doc, args.engine, env, selected, args.binaries)
    if args.output:
        with args.output.open("xb") as stream:
            stream.write(gzip.compress((json.dumps(doc, sort_keys=True) + "\n").encode(), mtime=0))
    print(f"{len(selected)} native results and {len(selected)} explicit denials verified for {args.engine}")
