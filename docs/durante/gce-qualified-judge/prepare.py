"""Build the frozen guest payload from a clean server revision and the v12 runtime installation."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

import corpus

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
HELPERS = ("verify_durante_observation.py", "verify_durante_native_boundaries.py")


def sha(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def make_archive(root, destination):
    # Bind source bytes plus the modes that the disposable guest will actually see.
    with destination.open("xb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.GNU_FORMAT) as out:
            for path in sorted(root.rglob("*")):
                if not (path.is_file() or path.is_dir()) or path.is_symlink():
                    raise ValueError(f"unexpected payload file: {path}")
                info = out.gettarinfo(str(path), arcname=str(path.relative_to(root)))
                info.uid = info.gid = info.mtime = 0
                info.uname = info.gname = ""
                info.mode = 0o755 if path.is_dir() or path.name == "guest.sh" else 0o644
                if path.is_file():
                    with path.open("rb") as contents:
                        out.addfile(info, contents)
                else:
                    out.addfile(info)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--server-source", type=Path, required=True)
    p.add_argument("--durante-install", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True, help="new payload directory")
    p.add_argument("--registration", type=Path, required=True)
    args = p.parse_args()
    server = args.server_source.resolve()
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=server, text=True).strip()
    subprocess.run(["git", "diff", "--quiet", "HEAD", "--", "cmd", "internal", "go.mod", "go.sum"], cwd=server, check=True)
    runtime = args.durante_install.resolve()
    if (runtime / "UPSTREAM.json").read_bytes() != (server / "third_party/durante-v12/UPSTREAM.json").read_bytes():
        raise ValueError("Durante installation provenance differs from server source")
    root = args.output.resolve()
    root.mkdir()
    binary_dir = root / "corpus"
    binary_dir.mkdir()
    corpus.prepare(binary_dir, "g++")
    for name in ("guest.py", "guest.sh"):
        shutil.copyfile(HERE / name, root / name)
    (root / "scripts").mkdir()
    for name in HELPERS:
        shutil.copyfile(REPO / "scripts" / name, root / "scripts" / name)
    shutil.copyfile(HERE.parent / "gce-v12-clock-transfer/guest.py", root / "v12_guest.py")
    flags = ["-tags", "durante_v12", "-trimpath", "-buildvcs=false"]
    with tempfile.TemporaryDirectory(dir=root.parent) as staging:
        tmp = Path(staging)
        squash = tmp / "install"
        shutil.copytree(runtime, squash)
        for name, package in (("server", "server"), ("reference", "durante-reference")):
            subprocess.run(["go", "build", *flags, "-o", squash / name, "./cmd/" + package],
                           cwd=server, check=True, env={**os.environ, "CGO_ENABLED": "0", "GOTMPDIR": str(tmp)})
        installed = {str(f.relative_to(squash)): sha(f) for f in sorted(squash.rglob("*")) if f.is_file()}
        for f in [squash, *squash.rglob("*")]:
            f.chmod(0o755 if f.is_dir() or os.access(f, os.X_OK) else 0o644)
        subprocess.run(["mksquashfs", squash, root / "runtime.squashfs", "-noappend", "-all-root",
                        "-all-time", "0", "-mkfs-time", "0", "-comp", "xz", "-processors", "2", "-no-progress"],
                       check=True)
    digest = sha(root / "runtime.squashfs")
    plan = dict(schema=1, server_commit=revision,
                go_version=subprocess.check_output(["go", "version"], text=True, cwd=server).strip(),
                go_build_flags=flags, cgo_enabled=False, installed_files=installed,
                compiler=subprocess.check_output(["g++", "--version"], text=True).splitlines()[0],
                server_sha256=installed["server"], reference_sha256=installed["reference"],
                runtime_sha256=digest, runtime_image="sha256:" + digest,
                manifest_sha256=sha(binary_dir / "manifest.json"))
    write_json(root / "plan.json", plan)
    archive = root.with_suffix(".tar.gz")
    make_archive(root, archive)
    registration = dict(schema=1, archive_sha256=sha(archive), plan=plan,
                        corpus=json.loads((binary_dir / "manifest.json").read_text()),
                        payload={str(f.relative_to(root)): sha(f) for f in sorted(root.rglob("*")) if f.is_file()})
    write_json(args.registration, registration)


if __name__ == "__main__":
    main()
