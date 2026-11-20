#!/usr/bin/env python3
"""Verify the exact vendored Durante source before fetching its pinned SDK."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tempfile


# Ordered from the full v7 source to each successive patch overlay.
COMMITS = {
    'v7': '41743068ef9070b6e04195bf7822c5ee513660ff',
    'v8': '614761c18d4735f00e93667c74c099cfdb2800c7',
    'v9': '27954247e094e682a7cf47504ff13682f970469a',
    'v10': '191858a02666c1406b871e4974b9072b2c91bb34',
}


def verify(root, version):
    manifest = json.loads((root / 'UPSTREAM.json').read_text())
    if manifest.get('repository') != 'https://github.com/seo-rii/durante':
        raise ValueError('unexpected upstream repository')
    if manifest.get('commit') != COMMITS[version]:
        raise ValueError('unexpected upstream commit for ' + version)
    files = manifest.get('files')
    if not isinstance(files, dict) or not files:
        raise ValueError('empty source manifest')
    actual = set()
    for path in root.rglob('*'):
        if path.is_symlink():
            raise ValueError('symlink in vendor source')
        if path.is_file():
            actual.add(path.relative_to(root).as_posix())
    if actual != set(files) | {'UPSTREAM.json'}:
        raise ValueError('vendor file set differs from source manifest')
    for name, expected in files.items():
        relative = PurePosixPath(name)
        if relative.is_absolute() or '..' in relative.parts or not re.fullmatch(r'[0-9a-f]{64}', expected):
            raise ValueError('invalid source manifest entry')
        if hashlib.sha256((root / name).read_bytes()).hexdigest() != expected:
            raise ValueError('vendor source hash mismatch: ' + name)
    return manifest['commit'], len(files)


def materialize(root, version, output):
    # Verify and assemble in a fresh directory. A failed patch or verification
    # must leave the caller's source and the requested destination untouched.
    root = root.resolve()
    if output.resolve().is_relative_to(root):
        raise ValueError('output must be outside the base source directory')
    if output.exists() or output.is_symlink():
        raise ValueError('output directory already exists')
    verify(root, 'v7')
    with tempfile.TemporaryDirectory(dir=output.parent) as temporary:
        tree = Path(temporary) / 'durante'
        shutil.copytree(root, tree)
        for profile in COMMITS:
            if profile != 'v7':
                overlay = root.with_name('durante-' + profile)
                with (overlay / 'changes.patch').open('rb') as diff:
                    subprocess.run(['patch', '--batch', '--forward', '--fuzz=0', '-p1'],
                                   cwd=tree, stdin=diff, check=True)
                shutil.copyfile(overlay / 'UPSTREAM.json', tree / 'UPSTREAM.json')
            result = verify(tree, profile)
            if profile == version:
                break
        tree.rename(output)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, help='verify an existing source tree (defaults to v7)')
    parser.add_argument('--version', choices=COMMITS)
    parser.add_argument('--output', type=Path, help='assemble the selected verified source in a new directory')
    args = parser.parse_args()
    root = args.root or Path(__file__).resolve().parent.parent / 'third_party/durante'
    try:
        if args.output:
            results = [materialize(root, args.version or 'v7', args.output)]
        elif args.root:
            results = [verify(root, args.version or 'v7')]
        else:
            # The repository CI entry point checks every profile by default.
            versions = (args.version,) if args.version else COMMITS
            with tempfile.TemporaryDirectory() as tmp:
                results = [materialize(root, version, Path(tmp) / version) for version in versions]
    except (ValueError, OSError, TypeError, subprocess.CalledProcessError) as exc:
        print('Durante vendor verification failed: ' + str(exc), file=sys.stderr)
        return 1
    for commit, count in results:
        print('Verified %d Durante files at %s' % (count, commit))
    return 0


if __name__ == '__main__':
    sys.exit(main())
