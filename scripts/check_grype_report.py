#!/usr/bin/env python3
import json
import sys
from pathlib import Path


if len(sys.argv) != 2:
    print(f"usage: {Path(sys.argv[0]).name} <grype-report.json>", file=sys.stderr)
    raise SystemExit(1)

report_path = Path(sys.argv[1])
try:
    report_body = report_path.read_text(encoding="utf-8")
except OSError as exc:
    print(f"grype report policy failed: cannot read {report_path}: {exc}", file=sys.stderr)
    raise SystemExit(1)

try:
    report = json.loads(report_body)
except json.JSONDecodeError as exc:
    print(f"grype report policy failed: {report_path} is not valid JSON: {exc}", file=sys.stderr)
    raise SystemExit(1)

if not isinstance(report, dict):
    print(f"grype report policy failed: {report_path} must contain a JSON object", file=sys.stderr)
    raise SystemExit(1)

if "error" in report:
    error = json.dumps(report["error"], ensure_ascii=False)
    print(
        f"grype report policy failed: {report_path} contains a scanner operational error: {error}",
        file=sys.stderr,
    )
    raise SystemExit(1)

matches = report.get("matches")
if not isinstance(matches, list):
    print(f"grype report policy failed: {report_path} has no valid matches array", file=sys.stderr)
    raise SystemExit(1)

findings = set()
for index, match in enumerate(matches):
    if not isinstance(match, dict):
        print(
            f"grype report policy failed: {report_path} match {index} must be an object",
            file=sys.stderr,
        )
        raise SystemExit(1)

    vulnerability = match.get("vulnerability")
    artifact = match.get("artifact")
    if not isinstance(vulnerability, dict) or not isinstance(artifact, dict):
        print(
            f"grype report policy failed: {report_path} match {index} has invalid vulnerability or artifact data",
            file=sys.stderr,
        )
        raise SystemExit(1)

    vulnerability_id = vulnerability.get("id")
    severity = vulnerability.get("severity")
    package_name = artifact.get("name")
    package_version = artifact.get("version")
    if not all(
        isinstance(value, str) and value
        for value in (vulnerability_id, severity, package_name, package_version)
    ):
        print(
            f"grype report policy failed: {report_path} match {index} has missing vulnerability or package fields",
            file=sys.stderr,
        )
        raise SystemExit(1)

    fix = vulnerability.get("fix")
    if not isinstance(fix, dict):
        print(
            f"grype report policy failed: {report_path} match {index} has invalid fix data",
            file=sys.stderr,
        )
        raise SystemExit(1)

    fix_versions = fix.get("versions", [])
    if not isinstance(fix_versions, list) or not all(
        isinstance(version, str) and version for version in fix_versions
    ):
        print(
            f"grype report policy failed: {report_path} match {index} has invalid fix versions",
            file=sys.stderr,
        )
        raise SystemExit(1)

    available = fix.get("available")
    if available is None:
        available = []
    if not isinstance(available, list):
        print(
            f"grype report policy failed: {report_path} match {index} has invalid available-fix data",
            file=sys.stderr,
        )
        raise SystemExit(1)
    for available_fix in available:
        if not isinstance(available_fix, dict):
            print(
                f"grype report policy failed: {report_path} match {index} has invalid available-fix data",
                file=sys.stderr,
            )
            raise SystemExit(1)
        version = available_fix.get("version")
        if not isinstance(version, str) or not version:
            print(
                f"grype report policy failed: {report_path} match {index} has an invalid available fix version",
                file=sys.stderr,
            )
            raise SystemExit(1)
        fix_versions.append(version)

    if severity.lower() in {"high", "critical"} and fix_versions:
        findings.add(
            (
                severity,
                vulnerability_id,
                package_name,
                package_version,
                tuple(sorted(set(fix_versions))),
            )
        )

if findings:
    ordered_findings = sorted(
        findings,
        key=lambda finding: (
            0 if finding[0].lower() == "critical" else 1,
            finding[1],
            finding[2],
            finding[3],
        ),
    )
    print(
        f"grype report policy failed: {len(ordered_findings)} fixable high/critical finding(s) in {report_path}",
        file=sys.stderr,
    )
    for severity, vulnerability_id, package_name, package_version, fix_versions in ordered_findings[:20]:
        print(
            f"- {severity} {vulnerability_id}: {package_name} {package_version} -> {', '.join(fix_versions)}",
            file=sys.stderr,
        )
    if len(ordered_findings) > 20:
        print(f"- ... and {len(ordered_findings) - 20} more", file=sys.stderr)
    raise SystemExit(1)

print(
    f"grype report policy passed: {len(matches)} match(es), no fixable high/critical findings"
)
