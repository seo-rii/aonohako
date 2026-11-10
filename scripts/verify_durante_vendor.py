#!/usr/bin/env python3
"""Verify the exact vendored Durante source before fetching its pinned SDK."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import sys


def verify(root):
    manifest = json.loads((root / 'UPSTREAM.json').read_text())
    if manifest.get('repository') != 'https://github.com/seo-rii/durante':
        raise ValueError('unexpected upstream repository')
    if not re.fullmatch(r'[0-9a-f]{40}', manifest.get('commit', '')):
        raise ValueError('invalid upstream commit')
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parent.parent / 'third_party/durante')
    args = parser.parse_args()
    try:
        commit, count = verify(args.root)
    except (ValueError, OSError, TypeError) as exc:
        print('Durante vendor verification failed: ' + str(exc), file=sys.stderr)
        return 1
    print('Verified %d Durante files at %s' % (count, commit))
    return 0


if __name__ == '__main__':
    sys.exit(main())
