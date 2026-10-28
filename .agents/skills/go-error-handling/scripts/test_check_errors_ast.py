#!/usr/bin/env python3
"""Exercise error/log matching boundaries and adversarial source ordering."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


class ErrorAnalyzerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.binary = cls.root / 'check-errors'
        subprocess.run(['go', 'build', '-o', str(cls.binary), str(HERE / 'check-errors-ast.go')],
                       check=True, capture_output=True)

    def analyze(self, source, **options):
        path = self.root / 'fixture.go'
        path.write_text(source)
        result = subprocess.run([str(self.binary), '--json', '--no-bare-return', str(path)],
                                capture_output=True, text=True, timeout=20, **options)
        self.assertIn(result.returncode, (0, 1), result.stderr)
        return json.loads(result.stdout)['findings']

    def test_five_line_window_uses_nearest_strictly_preceding_log(self):
        for distance in (1, 5, 6):
            with self.subTest(distance=distance):
                source = ('package p\nfunc f(err error) error {\n'
                          'log.Print(err)\n' + '\n' * (distance - 1) +
                          'return err\nlog.Print(err)\n}\n')
                findings = self.analyze(source)
                self.assertEqual(len(findings), int(distance <= 5))
                if findings:
                    self.assertEqual(findings[0]['line'], 3)
                    self.assertIn(f'returned (line {3 + distance})', findings[0]['message'])
        findings = self.analyze('package p\nfunc f(err error) error {\n'
                                'log.Print(err)\nlog.Print(err); return err\n}\n')
        self.assertEqual([item['line'] for item in findings], [3])
        findings = self.analyze('package p\nfunc f(err error) error {\n'
                                'log.Print(err)\nlog.Print(err)\nreturn err\n}\n')
        self.assertEqual([item['line'] for item in findings], [4])

    def test_line_directives_do_not_break_ordered_lookup(self):
        source = ('package p\nfunc f(err error) error {\n'
                  '//line fixture.go:50\nlog.Print(err)\n'
                  '//line fixture.go:3\nlog.Print(err)\n'
                  '//line fixture.go:55\nreturn err\n}\n')
        self.assertEqual([item['line'] for item in self.analyze(source)], [50])

    def test_same_line_and_later_logs_do_not_match(self):
        for body in ('log.Print(err); return err', 'return err\nlog.Print(err)', 'return err'):
            with self.subTest(body=body):
                self.assertEqual(self.analyze('package p\nfunc f(err error) error {\n' + body + '\n}\n'), [])

    @unittest.skipUnless(os.name == 'posix', 'CPU resource limits require Unix')
    def test_many_returns_before_logs_have_bounded_cpu_cost(self):
        import resource

        def limit_cpu():
            resource.setrlimit(resource.RLIMIT_CPU, (5, 5))

        # This is only 5.4 MB of source. The old backward scan performed
        # 40 billion comparisons; ordered lookup never visits the later logs.
        source = ('package p\nfunc f(err error) error {\n' + 'return err\n' * 200_000 +
                  'log.Print(err)\n' * 200_000 + '}\n')
        self.assertEqual(self.analyze(source, preexec_fn=limit_cpu), [])


if __name__ == '__main__':
    unittest.main()
