"""Build the live deadline comparison archive from the verified pilot runtime and ELFs."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from prepare import make_archive, sha, write_json

DURANTE_RESULTS = "357994c1f4cd4fc650848624bc3e355f7bfa06d8"
FEATURE_DIR = "experiments/qualified_batch_features"


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--pilot-payload", required=True, type=Path)
    cli.add_argument("--durante", required=True, type=Path)
    cli.add_argument("--output", required=True, type=Path)
    cli.add_argument("--registration", required=True, type=Path)
    args = cli.parse_args()
    root = args.output.resolve()
    root.mkdir()
    pilot_registration = HERE.parent / "registration.json"
    registration = json.loads(pilot_registration.read_bytes())
    for name, digest in registration["payload"].items():
        original = args.pilot_payload / name
        if sha(original) != digest:
            raise ValueError(f"original pilot file differs at {name}")
        destination = root / "pilot" / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(original, destination)

    manifest = subprocess.check_output(
        ["git", "show", f"{DURANTE_RESULTS}:{FEATURE_DIR}/case_manifest.json"], cwd=args.durante)
    if (HERE / "case_manifest.json").read_bytes() != manifest:
        raise ValueError("case manifest differs from the registered feature study")
    for name in ("guest.py", "guest.sh", "case_manifest.json", "freeze.json"):
        shutil.copyfile(HERE / name, root / name)

    payload = {str(p.relative_to(root)): sha(p) for p in sorted(root.rglob("*")) if p.is_file()}
    archive = root.with_suffix(".tar.gz")
    make_archive(root, archive)
    write_json(args.registration, dict(schema=1, archive_sha256=sha(archive), payload=payload,
               pilot_registration_sha256=sha(pilot_registration), feature_study_commit=DURANTE_RESULTS))


if __name__ == "__main__":
    main()
