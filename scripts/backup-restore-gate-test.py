#!/usr/bin/env python3
"""Check private evidence and the stable handoff without running Docker."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

GATE = Path(__file__).with_name('backup-restore-gate.sh')


class EvidenceTest(unittest.TestCase):
    def test_publication_replaces_symlink_without_touching_target(self):
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
            env = dict(os.environ, PATH=f'{bindir}:{os.environ["PATH"]}', TEST_ROOT=str(root),
                       NEROCD_BACKUP_RESTORE_EVIDENCE=str(evidence))
            result = subprocess.run(['bash', str(GATE)], env=env, text=True, capture_output=True, timeout=20)
            stage = Path((root / 'stage').read_text().strip())
            self.addCleanup(lambda: shutil.rmtree(stage, ignore_errors=True))
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual(victim.read_text(), 'valuable existing content')
            self.assertFalse(evidence.is_symlink())
            self.assertIn('FAIL: Docker unavailable', evidence.read_text())
            self.assertIn(f'backup/restore evidence: {evidence}', result.stdout)
            self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)
            self.assertFalse(stage.exists(), 'private work directory was not cleaned')


if __name__ == '__main__':
    unittest.main()
