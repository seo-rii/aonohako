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
option_rows = []
row_re = re.compile(r"^\|\s*(.*?)\s*\|\s*`(.*)`\s*\|$")
option_re = re.compile(r"^\|\s*`?(.*?)`?\s*\|\s*`(.*)`\s*\|$")

for index, summary_path in enumerate(summary_paths):
    profile = summary_path.parent.name
    if profile.startswith("toolchain-profile-"):
        profile = profile[len("toolchain-profile-") :]
    profile_order[profile] = index
    profiles.append(profile)
    section = None
    for raw_line in summary_path.read_text(encoding="utf-8").splitlines():
        if raw_line == "## Runtime Toolchain Versions":
            section = "versions"
            continue
        if raw_line == "## Runtime Compile Options":
            section = "compile_options"
            continue
        if raw_line.startswith("## "):
            section = None
            continue
        if section == "versions":
            match = row_re.match(raw_line)
            if match is None or match.group(1) == "Tool":
                continue
            rows.append((profile, match.group(1), match.group(2)))
        elif section == "compile_options":
            match = option_re.match(raw_line)
            if match is None or match.group(1) == "Language":
                continue
            option_rows.append((profile, match.group(1).strip("`"), match.group(2)))

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

options_by_language = {}
for profile, language, options in option_rows:
    options_by_language.setdefault(language, {}).setdefault(options, []).append(profile)

if options_by_language:
    print()
    print("## Runtime Compile Options")
    print()

    option_consistent = []
    option_conflicts = []
    for language in sorted(options_by_language, key=str.lower):
        options_map = options_by_language[language]
        if len(options_map) == 1:
            options = next(iter(options_map))
            option_consistent.append(
                (
                    language,
                    options,
                    sorted(set(options_map[options]), key=lambda item: profile_order[item]),
                )
            )
            continue
        for options in sorted(options_map):
            option_conflicts.append(
                (
                    language,
                    options,
                    sorted(set(options_map[options]), key=lambda item: profile_order[item]),
                )
            )

    if option_consistent:
        print("| Language | Compile options | Profiles |")
        print("| --- | --- | --- |")
        for language, options, option_profiles in option_consistent:
            print(
                f"| `{language}` | `{options}` | {', '.join(f'`{profile}`' for profile in option_profiles)} |"
            )

    if option_conflicts:
        if option_consistent:
            print()
        print("### Compile Option Differences")
        print()
        print("| Language | Compile options | Profiles |")
        print("| --- | --- | --- |")
        for language, options, option_profiles in option_conflicts:
            print(
                f"| `{language}` | `{options}` | {', '.join(f'`{profile}`' for profile in option_profiles)} |"
            )
