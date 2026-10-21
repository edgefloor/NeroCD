#!/usr/bin/env python3
"""Print a deterministic UI asset manifest, or compare a build with a saved manifest."""
import argparse
import hashlib
import json
from pathlib import Path
import sys


def manifest(root):
    if root.is_symlink() or not root.is_dir():
        raise ValueError('asset root must be a real directory')
    files = {}
    for path in sorted(root.rglob('*')):
        if path.is_symlink():
            raise ValueError(f'asset symlink rejected: {path.relative_to(root)}')
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError(f'non-regular asset rejected: {path.relative_to(root)}')
        files[path.relative_to(root).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
    if 'index.html' not in files:
        raise ValueError('built index.html is missing; build the frontend first')
    return {'schema': 'nerocd.ui-assets/v1', 'files': files}


def compare(current, previous):
    if not isinstance(previous, dict) or previous.get('schema') != 'nerocd.ui-assets/v1' or not isinstance(previous.get('files'), dict):
        raise ValueError('unsupported saved manifest')
    before, after = previous['files'], current['files']
    return {'added': sorted(after.keys() - before.keys()),
            'removed': sorted(before.keys() - after.keys()),
            'changed': sorted(key for key in before.keys() & after.keys() if before[key] != after[key])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[2] / 'web/dist')
    parser.add_argument('--compare', type=Path)
    args = parser.parse_args()
    current = manifest(args.root)
    if args.compare:
        delta = compare(current, json.loads(args.compare.read_text()))
        passed = not any(delta.values())
        print(json.dumps({'status': 'passed' if passed else 'failed', **delta}, sort_keys=True))
        return 0 if passed else 1
    print(json.dumps(current, sort_keys=True, separators=(',', ':')))
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError) as error:
        print(f'UI manifest: {error}', file=sys.stderr)
        sys.exit(2)
