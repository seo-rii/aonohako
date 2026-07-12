#!/usr/bin/env python3
import hashlib
import json
import re
import subprocess
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


if len(sys.argv) < 3:
    print(f"usage: {Path(sys.argv[0]).name} <toolchain-artifacts-dir> <expected-profile=language,...>...", file=sys.stderr)
    raise SystemExit(1)

root = Path(sys.argv[1])
if not root.is_dir():
    fail(f"missing artifact directory {root}")

expected_profile_languages = {}
for specification in sys.argv[2:]:
    profile, separator, raw_languages = specification.partition("=")
    if not separator or not raw_languages:
        fail(f"invalid expected profile specification {specification!r}")
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", profile) is None:
        fail(f"invalid expected profile name {profile!r}")
    if profile in expected_profile_languages:
        fail(f"duplicate expected profile name {profile!r}")
    languages = [language.strip() for language in raw_languages.split(",")]
    if any(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", language) is None for language in languages):
        fail(f"invalid expected language inventory for {profile!r}")
    if len(languages) != len(set(languages)):
        fail(f"duplicate expected language for {profile!r}")
    expected_profile_languages[profile] = languages
expected_profiles = list(expected_profile_languages)

profile_dirs = sorted(path for path in root.glob("toolchain-profile-*") if path.is_dir())
if not profile_dirs:
    fail("no profile artifact directories found")
actual_profiles = [path.name.removeprefix("toolchain-profile-") for path in profile_dirs]
if set(actual_profiles) != set(expected_profiles):
    fail(f"profile inventory {actual_profiles!r} does not match expected {expected_profiles!r}")

expected_manifest = {"SUMMARY.md", "MANIFEST.txt", "SHA256SUMS"}
archive_paths = set()
expected_languages = set()

for profile_dir in profile_dirs:
    profile = profile_dir.name.removeprefix("toolchain-profile-")
    required = [
        profile_dir / "summary.md",
        profile_dir / f"{profile}.sbom.spdx.json",
        profile_dir / f"{profile}.grype.json",
        profile_dir / f"{profile}.provenance.json",
    ]
    for path in required:
        if not path.is_file():
            fail(f"missing {path}")
        if path.stat().st_size == 0:
            fail(f"empty {path}")

    summary_path = profile_dir / "summary.md"
    summary = summary_path.read_text(encoding="utf-8")
    for placeholder in ("<command failed>", "<no version output>", "<not installed>", "<package probe failed>"):
        if placeholder in summary:
            fail(f"{summary_path} contains failed version probe {placeholder!r}")
    if "## Runtime Toolchain Versions" not in summary:
        fail(f"{summary_path} is missing the runtime toolchain heading")
    languages_match = re.search(r"(?m)^- Languages: `([^`]+)`$", summary)
    if languages_match is None:
        fail(f"{summary_path} is missing a non-empty language inventory")
    profile_languages = [language.strip() for language in languages_match.group(1).split(",")]
    if any(not language for language in profile_languages) or len(profile_languages) != len(set(profile_languages)):
        fail(f"{summary_path} contains an invalid language inventory")
    if profile_languages != expected_profile_languages[profile]:
        fail(f"{summary_path} language inventory {profile_languages!r} does not match expected {expected_profile_languages[profile]!r}")
    expected_languages.update(profile_languages)
    image_match = re.search(r"(?m)^- Image: `([^`]+)`$", summary)
    expected_image = f"aonohako-ci-prod:{profile}"
    if image_match is None or image_match.group(1) != expected_image:
        fail(f"{summary_path} image must equal {expected_image!r}")
    image_id_match = re.search(r"(?m)^- Image ID: `(sha256:[0-9a-f]{64})`$", summary)
    if image_id_match is None:
        fail(f"{summary_path} is missing an immutable image ID")
    summary_image_id = image_id_match.group(1)
    if "| Tool | Version |" not in summary:
        fail(f"{summary_path} is missing the version table")
    version_section = summary.split("## Runtime Compile Options", maxsplit=1)[0]
    version_rows = [
        line
        for line in version_section.splitlines()
        if line.startswith("|") and "| `" in line and not line.startswith("| Tool |")
    ]
    if not version_rows:
        fail(f"{summary_path} contains no toolchain version rows")
    if "## Runtime Compile Options" not in summary or "| Language | Compile options |" not in summary:
        fail(f"{summary_path} is missing the compile-options table")
    compile_section = summary.split("## Runtime Compile Options", maxsplit=1)[1]
    compile_rows = [
        line
        for line in compile_section.splitlines()
        if line.startswith("| `") and "| `" in line
    ]
    if not compile_rows:
        fail(f"{summary_path} contains no compile-option rows")
    compile_languages = []
    for line in compile_rows:
        match = re.match(r"^\| `([^`]+)` \|", line)
        if match is not None:
            compile_languages.append(match.group(1))
    if set(compile_languages) != set(profile_languages) or len(compile_languages) != len(profile_languages):
        fail(f"{summary_path} compile-option inventory {compile_languages!r} does not match languages {profile_languages!r}")

    sbom_path = profile_dir / f"{profile}.sbom.spdx.json"
    grype_path = profile_dir / f"{profile}.grype.json"
    provenance_path = profile_dir / f"{profile}.provenance.json"
    reports = {}
    for path in [sbom_path, grype_path]:
        try:
            report = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            fail(f"{path} is not valid JSON: {exc}")
        if not isinstance(report, dict):
            fail(f"{path} must contain a JSON object")
        if "error" in report:
            fail(f"{path} contains scanner error diagnostic: {report['error']!r}")
        reports[path] = report

    sbom = reports[sbom_path]
    if sbom.get("spdxVersion") != "SPDX-2.3" or sbom.get("SPDXID") != "SPDXRef-DOCUMENT" or sbom.get("dataLicense") != "CC0-1.0":
        fail(f"{sbom_path} is missing required SPDX 2.3 document metadata")
    if sbom.get("name") != expected_image:
        fail(f"{sbom_path} document name must equal {expected_image!r}")
    if not isinstance(sbom.get("documentNamespace"), str) or not sbom["documentNamespace"].strip():
        fail(f"{sbom_path} is missing documentNamespace")
    creation_info = sbom.get("creationInfo")
    if not isinstance(creation_info, dict) or not isinstance(creation_info.get("creators"), list) or "Tool: syft-1.42.4" not in creation_info["creators"]:
        fail(f"{sbom_path} is missing pinned Syft 1.42.4 creation metadata")
    packages = sbom.get("packages")
    if not isinstance(packages, list) or not packages:
        fail(f"{sbom_path} is missing a non-empty SPDX packages array")
    if any(not isinstance(package, dict) or not isinstance(package.get("name"), str) or not package["name"].strip() or not isinstance(package.get("SPDXID"), str) or not package["SPDXID"].strip() for package in packages):
        fail(f"{sbom_path} contains an invalid SPDX package")
    relationships = sbom.get("relationships")
    if not isinstance(relationships, list) or not relationships:
        fail(f"{sbom_path} is missing SPDX relationships")
    if any(not isinstance(relationship, dict) or not all(isinstance(relationship.get(field), str) and relationship[field] for field in ("spdxElementId", "relatedSpdxElement", "relationshipType")) for relationship in relationships):
        fail(f"{sbom_path} contains an invalid SPDX relationship")

    grype = reports[grype_path]
    matches = grype.get("matches")
    if not isinstance(matches, list):
        fail(f"{grype_path} is missing the Grype matches array")
    if any(not isinstance(match, dict) or not isinstance(match.get("vulnerability"), dict) or not isinstance(match["vulnerability"].get("id"), str) or not match["vulnerability"]["id"] or not isinstance(match.get("artifact"), dict) or not isinstance(match["artifact"].get("name"), str) or not match["artifact"]["name"] for match in matches):
        fail(f"{grype_path} contains an invalid vulnerability match")
    source = grype.get("source")
    target = source.get("target") if isinstance(source, dict) else None
    if not isinstance(source, dict) or source.get("type") != "image" or not isinstance(target, dict) or target.get("userInput") != expected_image:
        fail(f"{grype_path} source image must equal {expected_image!r}")
    descriptor = grype.get("descriptor")
    if not isinstance(descriptor, dict) or descriptor.get("name") != "grype" or not isinstance(descriptor.get("version"), str) or descriptor["version"].lstrip("v") != "0.111.0":
        fail(f"{grype_path} is missing the pinned Grype descriptor")
    if not isinstance(grype.get("distro"), dict):
        fail(f"{grype_path} is missing Grype distro metadata")

    try:
        provenance = json.loads(provenance_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fail(f"{provenance_path} is not valid JSON: {exc}")
    if not isinstance(provenance, dict):
        fail(f"{provenance_path} must contain a JSON object")
    image_id = provenance.get("image_id")
    if provenance.get("profile") != profile or provenance.get("image") != expected_image or not isinstance(image_id, str) or re.fullmatch(r"sha256:[0-9a-f]{64}", image_id) is None:
        fail(f"{provenance_path} is missing exact profile, image, or immutable image ID provenance")
    if image_id != summary_image_id:
        fail(f"{summary_path} image ID does not match {provenance_path}")
    if target.get("imageID") != image_id:
        fail(f"{grype_path} image ID does not match {provenance_path}")
    provenance_artifacts = provenance.get("artifacts")
    expected_provenance_artifacts = {
        "summary.md": summary_path,
        "sbom.spdx.json": sbom_path,
        "grype.json": grype_path,
    }
    if not isinstance(provenance_artifacts, dict) or set(provenance_artifacts) != set(expected_provenance_artifacts):
        fail(f"{provenance_path} artifact inventory does not match the profile reports")
    for artifact_name, artifact_path in expected_provenance_artifacts.items():
        expected_digest = provenance_artifacts.get(artifact_name)
        if not isinstance(expected_digest, str) or re.fullmatch(r"[0-9a-f]{64}", expected_digest) is None or sha256_file(artifact_path) != expected_digest:
            fail(f"{provenance_path} digest for {artifact_name!r} does not match {artifact_path}")

    expected_manifest.add((profile_dir / "summary.md").relative_to(root).as_posix())
    expected_manifest.add(sbom_path.relative_to(root).as_posix())
    expected_manifest.add(grype_path.relative_to(root).as_posix())
    expected_manifest.add(provenance_path.relative_to(root).as_posix())

    archive = profile_dir / f"{profile}.docker.tar.gz"
    archive_digest = profile_dir / f"{profile}.docker.tar.gz.sha256"
    archive_error = profile_dir / f"{profile}.docker.tar.gz.error.json"
    if archive.is_file():
        if archive.stat().st_size == 0:
            fail(f"empty {archive}")
        if not archive_digest.is_file():
            fail(f"missing {archive_digest}")
        if archive_digest.stat().st_size == 0:
            fail(f"empty {archive_digest}")
        if archive_error.exists():
            fail(f"{profile_dir} contains both image archive and archive error diagnostic")
        digest_parts = archive_digest.read_text(encoding="utf-8").split()
        if len(digest_parts) != 2 or re.fullmatch(r"[0-9a-f]{64}", digest_parts[0]) is None:
            fail(f"{archive_digest} must contain one SHA256 entry")
        expected_archive_path = archive.relative_to(root).as_posix()
        if digest_parts[1].removeprefix("*") != expected_archive_path:
            fail(f"{archive_digest} must reference bundle path {expected_archive_path!r}")
        expected_digest = digest_parts[0]
        actual_digest = sha256_file(archive)
        if actual_digest != expected_digest:
            fail(f"{archive} digest {actual_digest} does not match sidecar {expected_digest}")
        archive_relative = archive.relative_to(root).as_posix()
        archive_paths.add(archive_relative)
        expected_manifest.add(archive_relative)
        expected_manifest.add(archive_digest.relative_to(root).as_posix())
    else:
        if archive_digest.exists():
            fail(f"{profile_dir} contains archive digest without image archive")
        if not archive_error.is_file():
            fail(f"missing {archive} or {archive_error}")
        if archive_error.stat().st_size == 0:
            fail(f"empty {archive_error}")
        try:
            diagnostic = json.loads(archive_error.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            fail(f"{archive_error} is not valid JSON: {exc}")
        if not isinstance(diagnostic, dict):
            fail(f"{archive_error} must contain a JSON object")
        if "error" in diagnostic:
            fail(f"{archive_error} contains an archive error diagnostic: {diagnostic['error']!r}")
        if diagnostic.get("profile") != profile:
            fail(f"{archive_error} profile must equal {profile!r}")
        if not isinstance(diagnostic.get("skipped"), str) or not diagnostic["skipped"].strip():
            fail(f"{archive_error} is missing a non-empty skipped reason")
        expected_manifest.add(archive_error.relative_to(root).as_posix())

bundle = root / "SHA256SUMS"
if not bundle.is_file():
    fail(f"missing {bundle}")
bundle_archives = set()
for raw_line in bundle.read_text(encoding="utf-8").splitlines():
    if not raw_line.strip():
        continue
    parts = raw_line.split(maxsplit=1)
    if len(parts) != 2 or re.fullmatch(r"[0-9a-f]{64}", parts[0]) is None:
        fail(f"malformed SHA256SUMS line: {raw_line!r}")
    expected_digest, raw_path = parts
    relative_path = raw_path.strip().removeprefix("*")
    archive = Path(relative_path)
    if archive.is_absolute() or ".." in archive.parts:
        fail(f"SHA256SUMS path must stay within the artifact root: {raw_path!r}")
    archive = root / archive
    if not archive.is_file():
        fail(f"SHA256SUMS references missing file {archive}")
    actual_digest = sha256_file(archive)
    if actual_digest != expected_digest:
        fail(f"{archive} digest {actual_digest} does not match bundle {expected_digest}")
    if relative_path in bundle_archives:
        fail(f"SHA256SUMS contains duplicate archive path {relative_path!r}")
    bundle_archives.add(relative_path)
if bundle_archives != archive_paths:
    fail(f"SHA256SUMS archive inventory {sorted(bundle_archives)!r} does not match {sorted(archive_paths)!r}")

aggregate = root / "SUMMARY.md"
if not aggregate.is_file() or aggregate.stat().st_size == 0:
    fail(f"missing or empty {aggregate}")
aggregate_text = aggregate.read_text(encoding="utf-8")
if "## Runtime Toolchain Versions" not in aggregate_text or "- Profiles:" not in aggregate_text:
    fail(f"{aggregate} is missing consolidated toolchain metadata")
for placeholder in ("<command failed>", "<no version output>", "<not installed>", "<package probe failed>"):
    if placeholder in aggregate_text:
        fail(f"{aggregate} contains a failed version probe {placeholder!r}")
profiles_line = next((line for line in aggregate_text.splitlines() if line.startswith("- Profiles:")), "")
aggregate_profiles = re.findall(r"`([^`]+)`", profiles_line)
if set(aggregate_profiles) != set(expected_profiles):
    fail(f"{aggregate} profile inventory {aggregate_profiles!r} does not match expected {expected_profiles!r}")
aggregate_version_section = aggregate_text.split("## Runtime Compile Options", maxsplit=1)[0]
aggregate_version_rows = [
    line
    for line in aggregate_version_section.splitlines()
    if line.startswith("|") and "| `" in line and not line.startswith("| Tool |")
]
if not aggregate_version_rows:
    fail(f"{aggregate} contains no consolidated toolchain version rows")
if "## Runtime Compile Options" not in aggregate_text or "| Language | Compile options | Profiles |" not in aggregate_text:
    fail(f"{aggregate} is missing the consolidated compile-options table")
aggregate_compile_section = aggregate_text.split("## Runtime Compile Options", maxsplit=1)[1]
aggregate_compile_rows = [line for line in aggregate_compile_section.splitlines() if line.startswith("| `")]
if not aggregate_compile_rows:
    fail(f"{aggregate} contains no consolidated compile-option rows")
aggregate_languages = []
for line in aggregate_compile_rows:
    match = re.match(r"^\| `([^`]+)` \|", line)
    if match is not None:
        aggregate_languages.append(match.group(1))
if set(aggregate_languages) != expected_languages or len(aggregate_languages) != len(expected_languages):
    fail(f"{aggregate} compile-option inventory {aggregate_languages!r} does not match profile languages {sorted(expected_languages)!r}")
aggregator = Path(__file__).with_name("aggregate_toolchain_summaries.py")
generated_aggregate = subprocess.run(
    [sys.executable, str(aggregator), str(root)],
    check=False,
    capture_output=True,
    text=True,
)
if generated_aggregate.returncode != 0:
    fail(f"aggregate regeneration failed: {generated_aggregate.stderr.strip()}")
if aggregate_text != generated_aggregate.stdout:
    fail(f"{aggregate} does not exactly match the profile summaries")

manifest = root / "MANIFEST.txt"
if not manifest.is_file() or manifest.stat().st_size == 0:
    fail(f"missing or empty {manifest}")
manifest_entries = manifest.read_text(encoding="utf-8").splitlines()
if manifest_entries != sorted(set(manifest_entries)):
    fail(f"{manifest} entries must be sorted and unique")
for entry in manifest_entries:
    path = Path(entry)
    if not entry or path.is_absolute() or ".." in path.parts:
        fail(f"{manifest} contains unsafe path {entry!r}")
    if not (root / path).is_file():
        fail(f"{manifest} references missing file {entry!r}")
if set(manifest_entries) != expected_manifest:
    fail(f"{manifest} inventory {manifest_entries!r} does not match {sorted(expected_manifest)!r}")

print(f"verified {len(profile_dirs)} toolchain profile artifact set(s)")
