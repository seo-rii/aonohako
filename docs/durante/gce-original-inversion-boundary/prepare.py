"""Reassemble the exact-input judge payload using authenticated files from the inversion-clock study."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import tarfile

HERE = Path(__file__).resolve().parent
FIRST_REGISTRATION = "27cc1a221ceb00b8397fbb1056db8bd7d9f7a4ad04531bf8d0193bd8a3763289"


def sha(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def prepare(previous, registration, variants, out):
    if sha(registration) != FIRST_REGISTRATION:
        raise ValueError("unexpected previous payload registration")
    prior = json.loads(registration.read_bytes())["payload"]
    plan = json.loads((HERE / "plan.json").read_bytes())
    out.mkdir(parents=True, exist_ok=True)
    payload = out / "payload"
    payload.mkdir()
    manifest = {}

    def add(name, source, expected=None):
        target = payload / name
        target.parent.mkdir(exist_ok=True, parents=True)
        shutil.copyfile(source, target)
        manifest[name] = sha(target)
        if expected is not None and manifest[name] != expected:
            raise ValueError(f"unregistered bytes for {name}")

    def reuse(name, output=None):
        add(output or name, previous / name, prior[name])

    for name in ("plan.json", "guest.py", "guest.sh"):
        add(name, HERE / name)
    for name in ("runtime.squashfs", "installation.json", "plan.py", "pilot/guest.py", "pilot/guest.sh",
                 "pilot/v12_guest.py", "pilot/scripts/verify_durante_observation.py",
                 "pilot/scripts/verify_durante_native_boundaries.py"):
        reuse(name)
    reuse("guest.py", "inversion_guest.py")
    for case in plan["cases"].values():
        name = case["binary"]
        if name in manifest:
            continue
        if name in prior:
            reuse(name)
        else:
            add(name, variants / Path(name).name, case["binary_sha256"])
        if manifest[name] != case["binary_sha256"]:
            raise ValueError("registered ELF differs")
    # No threshold is fitted. The reused collector accepts an empty set on either fresh boot.
    thresholds = payload / "primary-thresholds.json"
    thresholds.write_bytes(b"{}\n")
    manifest[thresholds.name] = sha(thresholds)
    archive = out / "payload.tar.gz"
    with archive.open("xb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
            for name in sorted(manifest):
                path = payload / name
                info = tarfile.TarInfo(name)
                info.size = path.stat().st_size
                info.mode = 0o755 if name.endswith(".sh") or name.startswith("pilot/corpus/") else 0o644
                with path.open("rb") as stream:
                    tar.addfile(info, stream)
    rows = sum(5 * len(case["limits"]) for case in plan["cases"].values())
    if rows != 115:
        raise ValueError("registered 3-native/2-clock matrix differs")
    result = dict(schema=1, archive_sha256=sha(archive), payload=manifest, expected_rows=dict(clock=rows))
    (out / "registration.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--previous", type=Path, required=True, help="unpacked registered gce-inversion-clock payload")
    cli.add_argument("--previous-registration", type=Path, required=True)
    cli.add_argument("--variants", type=Path, required=True, help="directory containing the nine new compiled ELFs")
    cli.add_argument("--out", type=Path, required=True)
    args = cli.parse_args()
    prepare(args.previous, args.previous_registration, args.variants, args.out)
