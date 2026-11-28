"""Freeze two sealed runtimes and the exact 65 ms shortest-paths judge experiment."""
import argparse
from collections import deque
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import tarfile

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent / "gce-qualified-judge"
PREVIOUS = HERE.parent / "gce-original-inversion-boundary"
ENGINES = ("v12", "v13")
METHODS = {"ordered_set": "-DORDERED_SET", "fifo": "-DQUEUE_RELAX=0", "small_label_first": "-DQUEUE_RELAX=1",
           **{f"block{width}": f"-DBLOCK_SIZE={width}" for width in (4, 8, 16, 32, 64, 128, 512, 1024, 2048)}}
CONTROLS = {"fast": 2, "wrong": 4, "slow": 3}


def load(name, source):
    spec = importlib.util.spec_from_file_location(name, source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


corpus = load("paths_pilot_corpus", PILOT / "corpus.py")
prior = load("paths_prior_receipts", PREVIOUS / "v12/prepare.py")
read, sha, write = prior.read, prior.sha, prior.write


def wrong_output(parameters):
    n, extra, max_cost, seed = parameters
    rng = corpus.numbers(seed)
    graph = []
    for u in range(n):
        next(rng)
        graph.append([(u + 1) % n])
    for _ in range(extra):
        u = next(rng) % n
        graph[u].append((u + 1 + next(rng) % (n - 1)) % n)
        next(rng)
    distances = [-1] * n
    distances[0] = 0
    pending = deque([0])
    while pending:
        u = pending.popleft()
        for v in graph[u]:
            if distances[v] < 0:
                distances[v] = distances[u] + 1
                pending.append(v)
    return f"{sum(distances)}\n"


def build(bins):
    bins.mkdir(parents=True, exist_ok=True)
    problem = "shortest_paths"
    original = read(PILOT / "registration.json")["payload"]
    stdin, expected = corpus.case(problem)
    wrong = wrong_output(corpus.INPUTS[problem])
    result = {}
    for role, define in {**{r: f"-DSOLUTION={idx}" for r, idx in CONTROLS.items()}, **METHODS}.items():
        control = role in CONTROLS
        source = PILOT / "corpus/shortest_paths.cpp" if control else HERE / "variants.cpp"
        name = f"shortest_paths-{role}"
        binary = bins / name
        subprocess.run(["g++", *corpus.FLAGS, define, str(source), "-o", str(binary)], check=True)
        digest = sha(binary.read_bytes())
        if control and digest != original["corpus/" + name]:
            raise ValueError("control ELF differs from original pilot: " + role)
        solver = wrong if role == "wrong" else expected.decode()
        if subprocess.check_output([binary], input=stdin).decode() != solver:
            raise ValueError("solver differs from independent oracle: " + role)
        limits = {"contest": 65} if control else {"contest": 65, "ample": 30000}
        result[role] = dict(binary="pilot/corpus/" + name, binary_sha256=digest, stdin=stdin.decode(),
                            expected_stdout=expected.decode(), solver_stdout=solver, problem=problem,
                            split="control" if control else "variant", role=role, limits=limits)
    return result


def package(out, bins, parents):
    cases = build(bins)
    payload = out / "payload"
    payload.mkdir(parents=True)
    shutil.copyfile(HERE / "guest.sh", payload / "guest.sh")
    plans, requests = {}, {}
    common = ("guest.py", "guest.sh", "inversion_guest.py", "plan.py", "primary-thresholds.json", "pilot/guest.py",
              "pilot/guest.sh", "pilot/v12_guest.py", "pilot/scripts/verify_durante_observation.py",
              "pilot/scripts/verify_durante_native_boundaries.py", "runtime.squashfs", "installation.json")
    for version, source in zip(ENGINES, parents, strict=True):
        old = PREVIOUS / "v12" if version == "v12" else PREVIOUS
        frozen = read(old / "registration.json")["payload"]
        previous_plan = read(old / "plan.json")
        root = payload / version
        for name in (*common, *(("fixed_guest.py",) if version == "v12" else ())):
            if sha((source / name).read_bytes()) != frozen[name]:
                raise ValueError("different registered parent payload: " + name)
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / name, target)
        for case in cases.values():
            target = root / case["binary"]
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(bins / target.name, target)
        plan = dict(schema=1, cases=cases, image=previous_plan["image"],
                    baseline_binding=previous_plan["baseline_binding"],
                    original_input_sha256=sha(next(iter(cases.values()))["stdin"].encode()))
        write(root / "plan.json", plan)
        plans[version] = plan
        requests[version] = prior.request_hashes(plan, root)
    manifest = {}
    archive = out / "payload.tar.gz"
    with archive.open("xb") as raw, gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
            for path in sorted(p for p in payload.rglob("*") if p.is_file()):
                name = path.relative_to(payload).as_posix()
                manifest[name] = sha(path.read_bytes())
                info = tarfile.TarInfo(name)
                info.mode = 0o755 if name.endswith(".sh") or "/corpus/" in name else 0o644
                info.size = path.stat().st_size
                with path.open("rb") as stream:
                    tar.addfile(info, stream)
    count = sum(5 * len(case["limits"]) for case in cases.values())
    sources = {str(f.relative_to(HERE.parent)): sha(f.read_bytes()) for f in
               (HERE / "variants.cpp", HERE / "prepare.py", HERE / "guest.sh", HERE / "protocol.md",
                PILOT / "corpus/shortest_paths.cpp", PILOT / "corpus/generator.hpp", PILOT / "corpus.py")}
    write(out / "registration.json", dict(schema=1, plans=plans, requests=requests, sources=sources,
          build_flags=corpus.FLAGS, variants=METHODS, expected_rows={v:count for v in ENGINES},
          payload=manifest, archive_sha256=sha(archive.read_bytes())))


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--v12-parent", required=True, type=Path)
    cli.add_argument("--v13-parent", required=True, type=Path)
    cli.add_argument("--out", required=True, type=Path)
    args = cli.parse_args()
    package(args.out, args.out / "binaries", (args.v12_parent, args.v13_parent))
