"""Rebuild the registered guest archive from published payload bytes and the three new ELFs."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import tarfile

from guest import schedule

HERE = Path(__file__).resolve().parent
TRANSFER = HERE.parent / "gce-v13-judge-transfer"


def sha(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write(path, data):
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")


def prepare(previous, additional, memory, out):
    out.mkdir(parents=True, exist_ok=True)
    payload = out / "payload"
    payload.mkdir()
    prior = json.loads((TRANSFER / "registration.json").read_bytes())
    plan = json.loads((HERE / "plan.json").read_bytes())
    manifest = {}

    def add(name, source, expected=None):
        target = payload / name
        target.parent.mkdir(exist_ok=True, parents=True)
        shutil.copyfile(source, target)
        manifest[name] = sha(target)
        if expected is not None and manifest[name] != expected:
            raise ValueError(f"unregistered bytes for {name}")

    def reuse(name, output=None):
        add(output or name, previous / name, prior["payload"][name])

    for name in ("plan.py", "plan.json", "guest.py", "guest.sh", "memory_probe.py", "table.cpp"):
        add(name, HERE / name)
    add("memory-table", memory)
    reuse("v13.squashfs", "runtime.squashfs")
    for name in ("guest.py", "guest.sh", "v12_guest.py", "scripts/verify_durante_observation.py",
                 "scripts/verify_durante_native_boundaries.py"):
        reuse("pilot/" + name)
    for case in plan["cases"].values():
        name = case["binary"]
        if name in manifest:
            continue
        src = previous if name in prior["payload"] else additional
        add(name, src / name, case["binary_sha256"])
    freeze = previous / "freeze.json"
    if sha(freeze) != prior["payload"]["freeze.json"]:
        raise ValueError("archived baseline policy changed")
    files = json.loads((TRANSFER / "evidence/builder.json").read_bytes())["installed_sha256"]
    shared = prior["plan"]["arms"]["v13"]
    if any(files[k] != v for k, v in shared["files"].items()):
        raise ValueError("worker hashes disagree with sealed image build")
    installation = payload / "installation.json"
    write(installation, dict(files=files, policy=json.loads(freeze.read_bytes())["candidate"]["policy"],
                             model_binding=shared["binding"]))
    manifest["installation.json"] = sha(installation)

    archive = out / "payload.tar.gz"
    with archive.open("xb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
            for name in sorted(manifest):
                path = payload / name
                info = tarfile.TarInfo(name)
                info.size = path.stat().st_size
                info.mode = 0o755 if name.endswith(".sh") or name.startswith("pilot/corpus/") or name == "memory-table" else 0o644
                with path.open("rb") as stream:
                    tar.addfile(info, stream)
    placeholders = {k: {"low_ms": 1, "high_ms": 2} for k, c in plan["cases"].items() if c["split"] == "new"}
    expected = len(list(schedule(plan, "primary", placeholders)))
    if len(list(schedule(plan, "repeat", placeholders))) != expected:
        raise ValueError("phase lengths differ")
    result = dict(schema=1, archive_sha256=sha(archive), payload=manifest, expected_rows=dict(clock=expected, memory=20))
    write(out / "registration.json", result)
    return result


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--previous", type=Path, required=True, help="unpacked gce-v13-judge-transfer payload")
    cli.add_argument("--additional", type=Path, required=True, help="compiled pilot/corpus tree for both new solvers")
    cli.add_argument("--memory", type=Path, required=True, help="compiled memory-table binary")
    cli.add_argument("--out", type=Path, required=True)
    args = cli.parse_args()
    prepare(args.previous, args.additional, args.memory, args.out)
