"""Behavioral contracts for repository skill helpers, using isolated tools and files."""
import concurrent.futures
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts/skills' / f'{name}.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ToolTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=f'{self.bin}:/usr/bin:/bin',
                        XDG_CACHE_HOME=str(self.root / 'cache'), TMPDIR=str(self.root), PYTHONDONTWRITEBYTECODE='1')

    def executable(self, name, source):
        path = self.bin / name
        path.write_text('#!/bin/bash\nset -eu\n' + source)
        path.chmod(0o755)
        return path

    def shell(self, relative, *args):
        return subprocess.run(['bash', str(ROOT / relative), *args], cwd=self.root,
                              env=self.env, text=True, capture_output=True)

    def test_empty_benchmarks_report_overall_failure_in_json(self):
        self.executable('go', 'echo PASS\n')
        result = self.shell('.agents/skills/go-performance/scripts/bench-compare.sh', '--json')
        self.assertEqual(result.returncode, 1, result.stderr)
        payload = json.loads(result.stdout)
        self.assertEqual(payload['exit_code'], 1)
        self.assertEqual(payload['tool_exit_code'], 0)
        self.assertEqual(payload['status'], 'failed')

    def test_setup_does_not_replace_config_when_linter_is_missing(self):
        target = self.root / '.golangci.yml'
        target.write_text('existing configuration\n')
        result = self.shell('.agents/skills/go-linting/scripts/setup-lint.sh', '--force')
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertEqual(target.read_text(), 'existing configuration\n')

    def test_setup_verifies_before_replacing_config(self):
        target = self.root / '.golangci.yml'
        target.write_text('existing configuration\n')
        self.executable('golangci-lint', 'exit 1\n')
        result = self.shell('.agents/skills/go-linting/scripts/setup-lint.sh', '--force')
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertEqual(target.read_text(), 'existing configuration\n')
        self.assertEqual(list(self.root.glob('.golangci.*.yml')), [])

    def test_setup_publishes_valid_config(self):
        log = self.root / 'calls'
        self.executable('golangci-lint', f'echo "$1" >> "{log}"\n')
        result = self.shell('.agents/skills/go-linting/scripts/setup-lint.sh', '--json')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(log.read_text().splitlines(), ['config', 'run'])
        self.assertTrue((self.root / '.golangci.yml').is_file())
        json.loads(result.stdout)

    def test_setup_distinguishes_tool_error_from_lint_findings(self):
        self.executable('golangci-lint', '[[ "$1" == config ]] && exit 0\nexit 3\n')
        result = self.shell('.agents/skills/go-linting/scripts/setup-lint.sh', '--json')
        self.assertEqual(result.returncode, 2, result.stderr)
        payload = json.loads(result.stdout)
        self.assertEqual(payload['exit_code'], 2)
        self.assertEqual(payload['tool_exit_code'], 3)
        self.assertEqual(payload['status'], 'failed')

    def pre_review_fixture(self, installed=True):
        relative = Path('.agents/skills/go-code-review/scripts/pre-review.sh')
        script = self.root / relative
        script.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / relative, script)
        pinned = self.root / 'pinned-lint'
        if installed:
            pinned.write_text('#!/bin/sh\nexit 0\n')
            pinned.chmod(0o755)
        self.executable('make', f'if [[ "$*" == *skill-lint-path* ]]; then echo "{pinned}"; else echo "$*" > "{self.root / "make-call"}"; fi\n')
        self.executable('go', f'echo "$*" >> "{self.root / "go-call"}"\n')
        self.executable('gofmt', f'echo "$*" > "{self.root / "fmt-call"}"\n')
        return script

    def test_pre_review_uses_pinned_make_target_without_duplicate_vet(self):
        script = self.pre_review_fixture()
        result = subprocess.run(['bash', str(script), '--json'], env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('PRODUCT_PACKAGES=./cmd/... ./internal/...', (self.root / 'make-call').read_text())
        self.assertFalse((self.root / 'go-call').exists())
        self.assertEqual(json.loads(result.stdout)['golangci_lint']['status'], 'passed')

    def test_pre_review_base_uses_changed_code_target(self):
        script = self.pre_review_fixture()
        self.executable('git', 'echo ' + 'a' * 40 + '\n')
        result = subprocess.run(['bash', str(script), '--json', '--base', 'HEAD', './internal/runner'],
                                env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        command = (self.root / 'make-call').read_text()
        self.assertIn('lint-new', command)
        self.assertIn('PRODUCT_PACKAGES=./internal/runner', command)
        self.assertIn('LINT_BASE_REV=' + 'a' * 40, command)

    def test_pre_review_rejects_shell_syntax_in_make_package_override(self):
        script = self.pre_review_fixture()
        result = subprocess.run(['bash', str(script), './internal/runner;touch marker'],
                                env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 2)
        self.assertFalse((self.root / 'make-call').exists())

    def test_pre_review_missing_tool_stops_before_analysis(self):
        script = self.pre_review_fixture(installed=False)
        result = subprocess.run(['bash', str(script)], env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertFalse((self.root / 'fmt-call').exists())
        self.assertFalse((self.root / 'go-call').exists())

    def test_pre_review_force_reports_skipped_lint_and_runs_vet(self):
        script = self.pre_review_fixture(installed=False)
        result = subprocess.run(['bash', str(script), '--json', '--force'], env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['golangci_lint']['status'], 'not_run')
        self.assertIn('vet ./cmd/... ./internal/...', (self.root / 'go-call').read_text())

    def launcher_fixture(self):
        # A fake compiler lets this test exercise publication and invalidation
        # independently of compiler startup time and machine-specific caches.
        self.executable('go', '''if [[ "$1" == env ]]; then
printf '{"GOVERSION":"%s","GOHOSTOS":"linux","GOHOSTARCH":"amd64"}' "${FIXTURE_VERSION:-go1}"
else
[[ "${FIXTURE_FAIL:-0}" != 1 ]] || exit 1
printf 'build\n' >> "$XDG_CACHE_HOME/builds"
while [[ "$1" != -o ]]; do shift; done
printf '#!/bin/sh\necho complete\n' > "$2"
chmod +x "$2"
fi
''')
        source = self.root / 'analyzer.go'
        source.write_text('package main\n')
        return source

    def launch(self, source, env=None):
        import sys
        return subprocess.run([sys.executable, '-B', str(ROOT / 'scripts/skills/run_analyzer.py'), str(source)],
                              env=env or self.env, text=True, capture_output=True)

    def test_analyzer_cache_reuses_and_invalidates_on_source_and_toolchain(self):
        source = self.launcher_fixture()
        for _ in range(2):
            result = self.launch(source)
            self.assertEqual(result.returncode, 0, result.stderr)
        source.write_text('package main // changed\n')
        self.assertEqual(self.launch(source).returncode, 0)
        self.assertEqual(self.launch(source, dict(self.env, FIXTURE_VERSION='go2')).returncode, 0)
        self.assertEqual((self.root / 'cache/builds').read_text().splitlines(), ['build'] * 3)

    def test_failed_build_does_not_publish_an_executable(self):
        source = self.launcher_fixture()
        self.assertEqual(self.launch(source, dict(self.env, FIXTURE_FAIL='1')).returncode, 2)
        self.assertEqual(list((self.root / 'cache/golang-skills').iterdir()), [])

    def test_concurrent_builders_only_execute_complete_binaries(self):
        source = self.launcher_fixture()
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            results = list(pool.map(lambda _: self.launch(source), range(2)))
        for result in results:
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, 'complete\n')
        self.assertEqual(len(list((self.root / 'cache/golang-skills').iterdir())), 1)

    def test_ui_manifest_is_deterministic_includes_hidden_files_and_detects_changes(self):
        module = load('ui_manifest')
        (self.root / 'index.html').write_text('first')
        (self.root / '.gitkeep').touch()
        first = module.manifest(self.root)
        self.assertIn('.gitkeep', first['files'])
        self.assertEqual(first, module.manifest(self.root))
        (self.root / 'index.html').write_text('second')
        (self.root / '.gitkeep').unlink()
        (self.root / 'app.js').write_text('new')
        self.assertEqual(module.compare(module.manifest(self.root), first),
                         {'changed': ['index.html'], 'removed': ['.gitkeep'], 'added': ['app.js']})

    def test_ui_manifest_rejects_missing_build_and_symlinks(self):
        module = load('ui_manifest')
        with self.assertRaises(ValueError):
            module.manifest(self.root)
        (self.root / 'index.html').write_text('build')
        (self.root / 'asset').symlink_to(self.root / 'index.html')
        with self.assertRaises(ValueError):
            module.manifest(self.root)

    def test_runner_profiles_match_existing_test_declarations(self):
        import re
        module = load('runner_checks')
        for profile, groups in module.PROFILES.items():
            for package, pattern in groups:
                names = []
                for path in (ROOT / package).glob('*_test.go'):
                    names.extend(re.findall(r'^func (Test\w+)\(', path.read_text(), re.M))
                self.assertTrue(any(re.match(pattern, name) for name in names), (profile, package))
            self.assertTrue(all('-race' in command for command in module.commands(profile, True)))


if __name__ == '__main__':
    unittest.main()
