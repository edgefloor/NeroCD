#!/usr/bin/env python3
"""Build and run a single-file Go skill analyzer using an atomic, versioned cache."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def run(source, args):
    source = Path(source).resolve(strict=True)
    environment = os.environ.copy()
    # These are host tools, even when the caller is cross-compiling the product.
    for name in ('GOOS', 'GOARCH', 'GOARM', 'GOAMD64', 'GOFLAGS'):
        environment.pop(name, None)
    environment['CGO_ENABLED'] = '0'
    info = json.loads(subprocess.check_output(
        ['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH'], env=environment))
    environment['GOOS'] = info['GOHOSTOS']
    environment['GOARCH'] = info['GOHOSTARCH']
    digest = hashlib.sha256(source.read_bytes() + b'\0' + json.dumps(info, sort_keys=True).encode()
                            + b'\0' + Path(__file__).read_bytes()).hexdigest()
    cache = Path(environment.get('XDG_CACHE_HOME', str(Path.home() / '.cache'))) / 'golang-skills'
    cache.mkdir(parents=True, exist_ok=True, mode=0o700)
    binary = cache / f'{source.stem}-{digest}'
    if not binary.is_file():
        # Each builder owns a private output. Concurrent builders may finish in
        # either order; readers only see complete executables after replacement.
        with tempfile.TemporaryDirectory(prefix='.build-', dir=cache) as staging:
            output = Path(staging) / 'analyzer'
            subprocess.run(['go', 'build', '-trimpath', '-o', str(output), str(source)],
                           env=environment, check=True)
            os.replace(output, binary)
    return subprocess.run([str(binary), *args]).returncode


if __name__ == '__main__':
    try:
        if len(sys.argv) < 2:
            raise ValueError('usage: run_analyzer.py SOURCE [ANALYZER_ARGS...]')
        sys.exit(run(sys.argv[1], sys.argv[2:]))
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f'analyzer launcher: {error}', file=sys.stderr)
        sys.exit(2)
