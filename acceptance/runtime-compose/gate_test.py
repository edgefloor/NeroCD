#!/usr/bin/env python3
"""Check request values at the child-process boundary without Docker."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

GATE = Path(__file__).with_name('gate.sh')


class RequestArgumentsTest(unittest.TestCase):
    def test_requests_keep_secrets_out_of_all_child_arguments(self):
        jq = shutil.which('jq')
        self.assertIsNotNone(jq, 'jq is required')
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            bindir = root / 'bin'
            bindir.mkdir()
            # Every subprocess helper records argv, including mktemp and rm.
            # Shell builtins are the only operations that receive raw secrets.
            for name in ('curl', 'jq', 'mktemp', 'rm', 'cat'):
                real = shutil.which(name)
                self.assertIsNotNone(real)
                shim = bindir / name
                shim.write_text('''#!/usr/bin/env python3
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
        log.write(json.dumps({'body': body.read_text(), 'headers': headers.read_text()}) + '\\n')
    pathlib.Path(args[args.index('-o') + 1]).write_text('metric 1\\n')
    print('201' if args[args.index('-X') + 1] == 'POST' else '200', end='')
    sys.exit(int(os.environ.get('TEST_CURL_EXIT', '0')))
os.execv(''' + repr(real) + ''', [sys.argv[0], *sys.argv[1:]])
''')
                shim.chmod(0o755)
            source = GATE.read_text()
            helpers = source[source.index('http(){'):source.index('\nproxy_status(){')]
            password = 'private password " \\ with\na newline'
            token = 'private-bearer-token'
            body = json.dumps({'credential': 'private-body-secret', 'line': 'one\ntwo'})
            (root / 'password').write_text(password)
            (root / 'token').write_text(token)
            (root / 'body').write_text(body)
            script = root / 'exercise.sh'
            script.write_text('set -euo pipefail\ndir=$TEST_ROOT\nbase=http://fixture\n' + helpers + '''
admin_email=fixture@example.invalid
admin_password=$(<"$dir/password")
admin=$(<"$dir/token")
body=$(<"$dir/body")
code=$(http POST "$base/sessions" '' "$(session_body)" "$dir/session.json")
[[ "$code" == 201 ]]
code=$(http POST "$base/private" "$admin" "$body" "$dir/private.json")
[[ "$code" == 201 ]]
[[ "$(metrics)" == 'metric 1' ]]
export TEST_CURL_EXIT=7
if http POST "$base/failure" "$admin" "$body" "$dir/failure.json"; then exit 90; else [[ $? == 7 ]]; fi
''')
            env = dict(os.environ, TEST_ROOT=str(root), PATH=f'{bindir}:{os.environ["PATH"]}')
            result = subprocess.run(['bash', str(script)], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in (root / 'argv.jsonl').read_text().splitlines()]
            for call in calls:
                for arg in call:
                    for secret in (password, 'private password', token, 'private-body-secret'):
                        self.assertNotIn(secret, arg, call)
            requests = [json.loads(line) for line in (root / 'requests.jsonl').read_text().splitlines()]
            self.assertEqual(json.loads(requests[0]['body']), {'email': 'fixture@example.invalid', 'password': password})
            self.assertEqual(requests[1]['body'], body)
            self.assertEqual(requests[1]['headers'], f'Content-Type: application/json\nAuthorization: Bearer {token}\n')
            self.assertEqual(requests[2]['body'], '')
            self.assertEqual(list(root.glob('http.*')), [])
            # Cover the live call sites outside the extracted helper as well.
            self.assertNotIn('--arg password', source)
            self.assertNotIn('-H "Authorization:', source)


if __name__ == '__main__':
    unittest.main()
