#!/usr/bin/env python3
"""Smoke-test automatic routing with two actual Aonohako HTTP servers.

Run in an offline Linux container with executable tmpfs at /work and /tmp.
Mount the prepared gce-qualified-judge payload and its extracted runtime
(/opt/durante) read-only. Supply the pilot registration, calibration frozen.json,
verified results.json and the new control-plane binary. The worker is the
original qualified binary. The controller receives only ordinary requests.
"""
import argparse
from contextlib import ExitStack
import hashlib
import json
from pathlib import Path
import sys
from tempfile import TemporaryDirectory

from verify_durante_observation import post, require, server


def digest(path):
    with path.open("rb") as stream:
        h = hashlib.sha256()
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            h.update(chunk)
        return h.hexdigest()


def load(path):
    return json.loads(path.read_bytes())


def run(args):
    root = args.payload.resolve()
    registered = load(args.registration)
    plan = load(root / "plan.json")
    frozen = load(args.frozen)
    qualified = load(args.qualified)
    scope = qualified["judge_cases"]
    require(qualified["frozen_sha256"] == digest(args.frozen) and
            qualified["qualified_contest_cases"] == len(scope["cases"]),
            "qualification result differs from the frozen calibration or its released scope")
    require(registered["plan"] == plan and digest(root / "plan.json") == frozen["plan_sha256"]
            and digest(root / "corpus/manifest.json") == plan["manifest_sha256"] == frozen["manifest_sha256"]
            and digest(root / "runtime.squashfs") == plan["runtime_sha256"] == frozen["runtime_sha256"],
            "frozen calibration does not identify this payload")
    # Use exactly the pilot's request builder, raw ceiling and clock verifier.
    for name in ("guest.py", "v12_guest.py", "scripts/verify_durante_native_boundaries.py",
                 "scripts/verify_durante_observation.py"):
        require(digest(root / name) == registered["payload"][name], f"unregistered pilot helper {name}")
    sys.path.insert(0, str(root))
    import guest as pilot

    worker = Path("/opt/durante/server")
    for name, sha in plan["installed_files"].items():
        require(digest(worker.parent / name) == sha, f"mounted runtime differs at {name}")
    require(plan["runtime_image"] == "sha256:" + frozen["runtime_sha256"] and scope["schema"] == 1,
            "expected registered runtime and case schema")
    manifest = load(root / "corpus/manifest.json")
    released = {}
    for entry in scope["cases"]:
        problem = entry["problem_id"]
        require(problem in manifest and problem not in released and entry in frozen["judge_cases"]["cases"]
                and entry["time_ms"] == frozen["cases"][problem]["contest_ms"], "outside registered contest scope")
        released[problem] = entry
    require(released and (not args.problem or args.problem in released), "no qualified contest case to exercise")
    problems = [args.problem] if args.problem else sorted(released)

    trials = []

    def submit(url, problem, role, route):
        files = root / "corpus"
        stdin = (files / (problem + ".stdin")).read_bytes()
        expected = (files / (problem + ".stdout")).read_bytes()
        binary = (files / (problem + "-" + role)).read_bytes()
        for data, sha in ((stdin, manifest[problem]["stdin_sha256"]), (expected, manifest[problem]["stdout_sha256"]),
                          (binary, manifest[problem]["elf_sha256"][role])):
            require(hashlib.sha256(data).hexdigest() == sha, "unregistered corpus artifact")
        request = pilot.request_for(problem, "native", released[problem]["time_ms"], stdin, expected, binary, None)
        if route == "unregistered":
            request["problem_id"] += "-outside-pilot"
            require(request["problem_id"] not in released, "unregistered control collides with scope")
        elif route in ("wall_time_ms", "memory_mb"):
            request["limits"][route] -= 1
        require(not any(k.startswith("work_measurement") for k in request), "client supplied a measurement option")
        result, stdout = post(url, "/execute", request, timeout=pilot.HARD["wall_time_ms"] / 1000 + 30)
        status = result["status"]
        if route == "unscoped-worker":
            require(status == "Container Initialization Failed" and "requires a scoped deciding judge" in result.get("reason", "")
                    and not result.get("cpu_time_ns") and not stdout and not any(k.startswith("work_measurement") for k in result),
                    "scope mismatch reached execution or silently fell back to native")
        elif route == "automatic":
            wanted = {"fast": "Accepted", "wrong": "Wrong Answer", "slow": "Time Limit Exceeded"}[role]
            require(status == wanted, f"{problem}/{role}: expected {wanted}, got {status}: {result.get('reason')}")
            pilot.validate(result, "judge", request, stdout.encode(), expected, frozen)
            if role == "slow":
                require(result["work_measurement_execution"]["termination_source"] == "durante_work_clock",
                        "slow program was not stopped by the running work clock")
        else:
            require(status == "Accepted", f"{route}: unexpected native verdict {status}")
            pilot.validate(result, "native", request, stdout.encode(), expected, frozen)
        trials.append({"problem": request["problem_id"], "role": role, "route": route, "limits": request["limits"],
                       "receipt": result, "stdout": stdout})
        print(f"{request['problem_id']}/{role} {route}: {status} ({result.get('verdict_source')})", flush=True)

    image = plan["runtime_image"]
    with TemporaryDirectory() as tmp:
        env, options, registry = pilot.configure(frozen["policy"], image, Path(tmp))
    require(registry == frozen["time_models"] and options == frozen["runtime_options_id"]
            and pilot.model_binding(frozen["policy"], options) == frozen["model_binding"], "model descriptor drift")
    scoped = {**env, "AONOHAKO_DURANTE_JUDGE_CASES": json.dumps(scope)}
    # The sealed worker predates schema 2. The new controller independently
    # checks every nominal limit replaced by that worker's raw envelope.
    controller_scope = {"schema": 2, "cases": [
        {**entry, "wall_time_ms": pilot.NOMINAL["wall_time_ms"], "memory_mb": pilot.NOMINAL["memory_mb"]}
        for entry in scope["cases"]]}
    auto = {**scoped, "AONOHAKO_DURANTE_JUDGE_CASES": json.dumps(controller_scope),
            "AONOHAKO_DURANTE_JUDGE_AUTO": "on"}
    worker_args = argparse.Namespace(server=str(worker), runtime_image=image)
    controller_args = argparse.Namespace(server=str(args.controller.resolve()), runtime_image=image)
    with ExitStack() as services:
        target = services.enter_context(server(worker_args, 18211, False, operator_env=scoped, umask=0o022))
        default = services.enter_context(server(controller_args, 18212, False, target, operator_env=scoped))
        enabled = services.enter_context(server(controller_args, 18213, False, target, operator_env=auto))
        for problem in problems:
            submit(default, problem, "fast", "default")
            for role in ("fast", "wrong", "slow"):
                submit(enabled, problem, role, "automatic")
            for route in ("unregistered", "wall_time_ms", "memory_mb"):
                submit(enabled, problem, "fast", route)
    with ExitStack() as services:
        target = services.enter_context(server(worker_args, 18211, False, operator_env=env, umask=0o022))
        enabled = services.enter_context(server(controller_args, 18213, False, target, operator_env=auto))
        submit(enabled, problems[0], "fast", "unscoped-worker")

    if args.output:
        with args.output.open("x") as out:
            json.dump({"schema": 1, "runtime_image": image, "controller_sha256": digest(args.controller),
                       "frozen_sha256": digest(args.frozen), "qualified_sha256": digest(args.qualified),
                       "worker_sha256": digest(worker), "trials": trials}, out, indent=2)
            out.write("\n")


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    for name in ("controller", "payload", "registration", "frozen", "qualified"):
        cli.add_argument("--" + name, required=True, type=Path)
    cli.add_argument("--problem", help="run one released case (default: all released cases)")
    cli.add_argument("--output", type=Path, help="new JSON file for complete receipts and stdout")
    run(cli.parse_args())
