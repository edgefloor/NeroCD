#!/usr/bin/env python3
"""Check private evidence and the stable handoff without running Docker."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

GATE = Path(__file__).with_name('backup-restore-gate.sh')


class EvidenceTest(unittest.TestCase):
    def test_publication_replaces_symlink_without_touching_target(self):
        self.check_publication(GATE, 'NEROCD_BACKUP_RESTORE_EVIDENCE', 'backup/restore')

    def test_dogfood_publication_replaces_symlink_without_touching_target(self):
        self.check_publication(GATE.with_name('dogfood-gate.sh'), 'NEROCD_DOGFOOD_EVIDENCE', 'dogfood')

    def test_compose_publication_replaces_symlink_without_touching_target(self):
        self.check_publication(GATE.parent.parent / 'acceptance/runtime-compose/gate.sh', 'NEROCD_RUNTIME_COMPOSE_EVIDENCE', 'runtime compose')

    def check_publication(self, gate, variable, label):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            bindir = root / 'bin'
            bindir.mkdir()
            victim = root / 'victim'
            victim.write_text('valuable existing content')
            evidence = root / 'evidence.txt'
            evidence.symlink_to(victim)
            for name in ('docker', 'bun', 'jq', 'rg'):
                path = bindir / name
                if name == 'docker':
                    path.write_text('#!/bin/sh\n[ "$1" != info ]\n')
                else:
                    path.write_text('#!/bin/sh\nexit 0\n')
                path.chmod(0o755)
            # Inspect the evidence before publication, while the real gate is
            # waiting for its mocked Docker availability check to fail.
            (bindir / 'docker').write_text('''#!/usr/bin/env python3
import os, pathlib, stat, sys
if sys.argv[1] == 'info':
    root = pathlib.Path(os.environ['TEST_ROOT'])
    stage = pathlib.Path((root / 'stage').read_text().strip())
    assert stat.S_IMODE(stage.stat().st_mode) == 0o700
    assert stat.S_IMODE(next(stage.glob('evidence.*')).stat().st_mode) == 0o600
    sys.exit(1)
''')
            mktemp = shutil.which('mktemp')
            self.assertIsNotNone(mktemp)
            (bindir / 'mktemp').write_text(f'#!/bin/sh\nif [ "$1" = -d ]; then {mktemp} "$@" | tee "$TEST_ROOT/stage"; else {mktemp} "$@"; fi\n')
            (bindir / 'mktemp').chmod(0o755)
            env = dict(os.environ, PATH=f'{bindir}:{os.environ["PATH"]}', TEST_ROOT=str(root))
            env[variable] = str(evidence)
            result = subprocess.run(['bash', str(gate)], env=env, text=True, capture_output=True, timeout=20)
            stage = Path((root / 'stage').read_text().strip())
            self.addCleanup(lambda: shutil.rmtree(stage, ignore_errors=True))
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual(victim.read_text(), 'valuable existing content')
            self.assertFalse(evidence.is_symlink())
            self.assertIn('FAIL: Docker unavailable', evidence.read_text())
            self.assertIn(f'{label} evidence: {evidence}', result.stdout)
            self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)
            self.assertFalse(stage.exists(), 'private work directory was not cleaned')
    def test_http_helpers_keep_credentials_out_of_child_arguments(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            bindir = root / 'bin'
            bindir.mkdir()
            for name in ('curl', 'mktemp', 'rm'):
                real = shutil.which(name)
                self.assertIsNotNone(real)
                shim = bindir / name
                shim.write_text("""#!/usr/bin/env python3
import json, os, pathlib, stat, sys
name = pathlib.Path(sys.argv[0]).name
root = pathlib.Path(os.environ['TEST_ROOT'])
with (root / 'argv.jsonl').open('a') as log:
    log.write(json.dumps([name, *sys.argv[1:]]) + '\\n')
if name == 'curl':
    args = sys.argv[1:]
    body = pathlib.Path(args[args.index('--data-binary') + 1][1:])
    headers = pathlib.Path(args[args.index('--header') + 1][1:])
    assert stat.S_IMODE(body.parent.stat().st_mode) == 0o700
    with (root / 'requests.jsonl').open('a') as log:
        log.write(json.dumps({'body': body.read_text(), 'headers': headers.read_text(), 'url': args[-1]}) + '\\n')
    print('503' if '-w' in args else '{}', end='')
    sys.exit(int(os.environ.get('TEST_CURL_EXIT', '0')))
os.execv(""" + repr(real) + """, [sys.argv[0], *sys.argv[1:]])
""")
                shim.chmod(0o755)
            source = GATE.read_text()
            helpers = source[source.index('private_http(){'):source.index('\nfor _ in {1..30}; do docker run', source.index('private_http(){'))]
            helpers += '\n' + '\n'.join(line for line in source.splitlines()
                                         if line.startswith(('restored_api_post(){', 'restored_get(){')))
            body = json.dumps({'password': 'private-password', 'lines': 'one\ntwo'})
            (root / 'body').write_text(body)
            (root / 'token').write_text('private-bearer-token')
            script = root / 'exercise.sh'
            script.write_text('set -euo pipefail\ndir=$TEST_ROOT\n' + helpers + """
source_port=127.0.0.1:1234
target_port=127.0.0.1:5678
body=$(<"$dir/body")
token=$(<"$dir/token")
restored_token=$token
api_post /sessions "$body"
api_get /private "$token"
restored_api_post /sessions "$body"
restored_get /private
[[ "$(private_http GET https://localhost/status "$token" '' -w '%{http_code}')" == 503 ]]
export TEST_CURL_EXIT=7
if api_post /failure "$body" "$token"; then exit 90; else [[ $? == 7 ]]; fi
""")
            result = subprocess.run(['bash', str(script)], text=True, capture_output=True,
                                    env=dict(os.environ, PATH=f'{bindir}:{os.environ["PATH"]}', TEST_ROOT=str(root)))
            self.assertEqual(result.returncode, 0, result.stderr)
            for call in map(json.loads, (root / 'argv.jsonl').read_text().splitlines()):
                for arg in call:
                    self.assertNotIn('private-password', arg)
                    self.assertNotIn('private-bearer-token', arg)
            requests = list(map(json.loads, (root / 'requests.jsonl').read_text().splitlines()))
            self.assertEqual(requests[0]['body'], body)
            self.assertEqual(requests[2]['body'], body)
            self.assertEqual(requests[1]['url'], 'http://127.0.0.1:1234/private')
            self.assertEqual(requests[3]['url'], 'http://127.0.0.1:5678/private')
            for index in (1, 3, 4, 5):
                self.assertIn('Authorization: Bearer private-bearer-token\n', requests[index]['headers'])
            self.assertEqual(list(root.glob('http.*')), [])
            self.assertNotIn('--post-data', source)
            self.assertNotIn('-H "Authorization:', source)
            self.assertNotIn('POSTGRES_PASSWORD="$password"', source)


if __name__ == '__main__':
    unittest.main()
