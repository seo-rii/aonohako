"""Rebuild the registered 183 ms range-minimum panel for either sealed judge."""
import argparse
import gzip
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import tarfile

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent / "gce-qualified-judge"
ORIGINAL = HERE.parent / "gce-original-inversion-boundary"
TRANSFER = HERE.parent / "gce-v13-judge-transfer"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


corpus = load("rmq_pilot_corpus", PILOT / "corpus.py")
prior = load("rmq_fixed_payload", ORIGINAL / "v12/prepare.py")
sha, read, write = prior.sha, prior.read, prior.write
BLOCKS = {"edges": (16, 64, 128, 256, 512, 1024), "cached": (4, 16, 64)}


def variants():
    return [(f"{method}{width}", [f"-DBLOCK_SIZE={width}", f"-DCACHED_EDGES={int(method == 'cached')}"])
            for method, widths in BLOCKS.items() for width in widths]


def prepare(engine, old, pilot, output):
    parent = read(ORIGINAL / ("v12/registration.json" if engine == "v12" else "registration.json"))["payload"]
    transfer = read(TRANSFER / "registration.json")
    design = transfer["plan"]["arms"][engine]
    payload = output / "payload"
    payload.mkdir(parents=True)
    for name, expected in parent.items():
        if name == "plan.json" or name.startswith("pilot/corpus/"):
            continue
        source = old / name
        target = payload / name
        target.parent.mkdir(exist_ok=True, parents=True)
        if sha(source.read_bytes()) != expected:
            raise ValueError("incorrect parent file " + name)
        shutil.copyfile(source, target)

    directory = payload / "pilot/corpus"
    directory.mkdir()
    inp, ans = corpus.case("range_min")
    small_in, small_out = corpus.case("range_min", small=True)
    cases = {}
    for role, flags in [("fast", None), ("wrong", None), ("slow", None), *variants()]:
        path = directory / ("range_min-" + role)
        if flags is None:
            shutil.copyfile(pilot / path.name, path)
            if sha(path.read_bytes()) != transfer["payload"]["pilot/corpus/" + path.name]:
                raise ValueError("different pilot control")
            path.chmod(0o755)
        else:
            subprocess.run(["g++", *corpus.FLAGS, *flags, HERE / "variants.cpp", "-o", path], check=True)
            if subprocess.check_output([path], input=small_in) != small_out:
                raise ValueError("small oracle failed: " + role)
        answer = subprocess.check_output([path], input=inp)
        if (ans != answer) != (role == "wrong"):
            raise ValueError("contest oracle failed: " + role)
        cases[role] = dict(problem="range_min", role=role, split="original" if flags is None else "variant",
                           stdin=inp.decode(), expected_stdout=ans.decode(), solver_stdout=answer.decode(),
                           binary="pilot/corpus/" + path.name, binary_sha256=sha(path.read_bytes()),
                           limits=dict(contest=183) if flags is None else dict(contest=183, ample=30000))
    plan = dict(schema=1, cases=cases, image=design["image"], baseline_binding=design["binding"],
                original_input_sha256=sha(inp), build=dict(flags=corpus.FLAGS, variants=dict(variants()),
                    source_sha256=sha((HERE / "variants.cpp").read_bytes()),
                    generator_sha256=sha((PILOT / "corpus/generator.hpp").read_bytes())),
                decision="On both fresh boots require the original fast AC, wrong WA and slow in-flight TLE, "
                         "and exact AC on every variant's ample runs. Opposite unanimous native and metered "
                         "decisions on the same variant on both boots disqualify this exact engine/input scope. "
                         "Mixed cells are inconclusive. Agreement does not expand the scope.")
    write(payload / "plan.json", plan)
    archive = output / "payload.tar.gz"
    manifest = {}
    with archive.open("xb") as raw, gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
            for file in sorted(p for p in payload.rglob("*") if p.is_file()):
                name = file.relative_to(payload).as_posix()
                manifest[name] = sha(file.read_bytes())
                info = tarfile.TarInfo(name)
                info.mode = 0o755 if name.endswith(".sh") or name.startswith("pilot/corpus/") else 0o644
                info.size = file.stat().st_size
                with file.open("rb") as stream:
                    tar.addfile(info, stream)
    rows = 5 * sum(len(c["limits"]) for c in cases.values())
    write(output / "registration.json", dict(schema=1, payload=manifest, archive_sha256=sha(archive.read_bytes()),
                                           expected_rows={"clock":rows}, requests=prior.request_hashes(plan, payload)))


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("engine", choices=("v12", "v13"))
    cli.add_argument("parent_payload", type=Path)
    cli.add_argument("pilot_corpus", type=Path)
    cli.add_argument("output", type=Path)
    args = cli.parse_args()
    prepare(args.engine, args.parent_payload, args.pilot_corpus, args.output)
