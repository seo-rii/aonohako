#!/usr/bin/env python3
import re
import sys
from pathlib import Path


if len(sys.argv) != 2:
    print(f"usage: {Path(sys.argv[0]).name} <toolchain-artifacts-dir>", file=sys.stderr)
    raise SystemExit(1)

root = Path(sys.argv[1])
summary_paths = sorted(root.glob("toolchain-profile-*/summary.md"))

print("## Runtime Toolchain Versions")
print()

if not summary_paths:
    print("No toolchain profile summaries were found.")
    raise SystemExit(0)

profile_order = {}
profiles = []
rows = []
row_re = re.compile(r"^\|\s*(.*?)\s*\|\s*`(.*)`\s*\|$")

for index, summary_path in enumerate(summary_paths):
    profile = summary_path.parent.name
    if profile.startswith("toolchain-profile-"):
        profile = profile[len("toolchain-profile-") :]
    profile_order[profile] = index
    profiles.append(profile)
    for raw_line in summary_path.read_text(encoding="utf-8").splitlines():
        match = row_re.match(raw_line)
        if match is None or match.group(1) == "Tool":
            continue
        rows.append((profile, match.group(1), match.group(2)))

versions_by_tool = {}
for profile, tool, version in rows:
    versions_by_tool.setdefault(tool, {}).setdefault(version, []).append(profile)

print(f"- Profiles: {', '.join(f'`{profile}`' for profile in profiles)}")
print()

consistent = []
conflicts = []
for tool in sorted(versions_by_tool, key=str.lower):
    version_map = versions_by_tool[tool]
    if len(version_map) == 1:
        version = next(iter(version_map))
        consistent.append(
            (
                tool,
                version,
                sorted(set(version_map[version]), key=lambda item: profile_order[item]),
            )
        )
        continue
    for version in sorted(version_map):
        conflicts.append(
            (
                tool,
                version,
                sorted(set(version_map[version]), key=lambda item: profile_order[item]),
            )
        )

if consistent:
    print("| Tool | Version | Profiles |")
    print("| --- | --- | --- |")
    for tool, version, tool_profiles in consistent:
        print(f"| {tool} | `{version}` | {', '.join(f'`{profile}`' for profile in tool_profiles)} |")

if conflicts:
    if consistent:
        print()
    print("### Version Differences")
    print()
    print("| Tool | Version | Profiles |")
    print("| --- | --- | --- |")
    for tool, version, tool_profiles in conflicts:
        print(f"| {tool} | `{version}` | {', '.join(f'`{profile}`' for profile in tool_profiles)} |")
