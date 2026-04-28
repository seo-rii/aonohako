#!/usr/bin/env python3
import hashlib
import json
import sys
from pathlib import Path


def fail(message: str) -> None:
    print(f"toolchain artifact verification failed: {message}", file=sys.stderr)
    raise SystemExit(1)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


if len(sys.argv) != 2:
    print(f"usage: {Path(sys.argv[0]).name} <toolchain-artifacts-dir>", file=sys.stderr)
    raise SystemExit(1)

root = Path(sys.argv[1])
profile_dirs = sorted(path for path in root.glob("toolchain-profile-*") if path.is_dir())
if not profile_dirs:
    fail("no profile artifact directories found")

for profile_dir in profile_dirs:
    profile = profile_dir.name.removeprefix("toolchain-profile-")
    required = [
        profile_dir / "summary.md",
        profile_dir / f"{profile}.sbom.spdx.json",
        profile_dir / f"{profile}.grype.json",
        profile_dir / f"{profile}.docker.tar.gz",
        profile_dir / f"{profile}.docker.tar.gz.sha256",
    ]
    for path in required:
        if not path.is_file():
            fail(f"missing {path}")
        if path.stat().st_size == 0:
            fail(f"empty {path}")

    for path in [profile_dir / f"{profile}.sbom.spdx.json", profile_dir / f"{profile}.grype.json"]:
        try:
            json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            fail(f"{path} is not valid JSON: {exc}")

    archive = profile_dir / f"{profile}.docker.tar.gz"
    expected_digest = (profile_dir / f"{profile}.docker.tar.gz.sha256").read_text(encoding="utf-8").split()[0]
    actual_digest = sha256_file(archive)
    if actual_digest != expected_digest:
        fail(f"{archive} digest {actual_digest} does not match sidecar {expected_digest}")

bundle = root / "SHA256SUMS"
if bundle.exists():
    for raw_line in bundle.read_text(encoding="utf-8").splitlines():
        if not raw_line.strip():
            continue
        parts = raw_line.split(maxsplit=1)
        if len(parts) != 2:
            fail(f"malformed SHA256SUMS line: {raw_line!r}")
        expected_digest, raw_path = parts
        archive = Path(raw_path.strip())
        if not archive.is_absolute():
            archive = Path.cwd() / archive
        if not archive.is_file():
            fail(f"SHA256SUMS references missing file {archive}")
        actual_digest = sha256_file(archive)
        if actual_digest != expected_digest:
            fail(f"{archive} digest {actual_digest} does not match bundle {expected_digest}")

print(f"verified {len(profile_dirs)} toolchain profile artifact set(s)")
