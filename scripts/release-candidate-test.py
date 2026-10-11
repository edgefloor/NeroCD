import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('candidate', Path(__file__).with_name('release-candidate.py'))
candidate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(candidate)

class PublicationTests(unittest.TestCase):
    def check(self, release_status=200, release=None, manifest_status=404, manifest=None):
        releases = [] if release is None else [dict(release, tag_name='v1.2.3')]
        responses = iter([(200, {}), (release_status, releases), (200, {'token': 'fixture'}),
                          (manifest_status, manifest or {'errors': [{'code': 'MANIFEST_UNKNOWN'}]})])
        return candidate.ensure_unpublished('v1.2.3', 'actor', 'credential', lambda *args: next(responses))

    def test_unpublished_version_and_empty_draft_are_reusable(self):
        self.check()
        self.check(200, {'draft': True, 'assets': []})

    def test_published_release_or_any_uploaded_asset_locks_version(self):
        for release in [{'draft': False, 'assets': []}, {'draft': True, 'assets': [{'id': 1}]}]:
            with self.subTest(release=release), self.assertRaises(candidate.ReleaseError):
                self.check(200, release)

    def test_image_locks_version_even_without_release_page(self):
        with self.assertRaises(candidate.ReleaseError):
            self.check(manifest_status=200)

    def test_remote_failures_do_not_mean_unpublished(self):
        for status in [401, 403, 429, 500]:
            with self.subTest(status=status), self.assertRaises(candidate.ReleaseError):
                self.check(manifest_status=status)
        with self.assertRaises(candidate.ReleaseError):
            self.check(release_status=500)
        with self.assertRaises(candidate.ReleaseError):
            self.check(manifest={'errors': [{'code': 'DENIED'}]})
        with self.assertRaises(candidate.ReleaseError):
            self.check(200, {'assets': []})

    def test_drafts_on_later_pages_are_checked(self):
        responses = iter([(200, {}), (200, [{'tag_name': 'v0.0.1'}] * 100),
                          (200, [{'tag_name': 'v1.2.3', 'draft': True, 'assets': [{'id': 1}]}])])
        with self.assertRaises(candidate.ReleaseError):
            candidate.ensure_unpublished('v1.2.3', 'actor', 'credential', lambda *args: next(responses))

    def test_publication_guard_failure_never_changes_a_tag(self):
        with patch.object(candidate, 'validate_commit'), patch.object(candidate, 'git', return_value='a' * 40), \
             patch.object(candidate, 'remote_tag', return_value='b' * 40), \
             patch.object(candidate, 'ensure_unpublished', side_effect=candidate.ReleaseError('locked')), \
             patch.object(candidate, 'update_tag') as update:
            with self.assertRaises(candidate.ReleaseError):
                candidate.publish_tag('v1.2.3', 'a' * 40, Path('.'), 'actor', 'credential')
            update.assert_not_called()

    def test_successful_publication_uses_observed_tag_lease(self):
        with patch.object(candidate, 'validate_commit'), patch.object(candidate, 'git', return_value='a' * 40), \
             patch.object(candidate, 'remote_tag', return_value='b' * 40), \
             patch.object(candidate, 'ensure_unpublished') as guard, patch.object(candidate, 'update_tag') as update:
            candidate.publish_tag('v1.2.3', 'a' * 40, Path('.'), 'actor', 'credential')
            guard.assert_called_once()
            update.assert_called_once_with('v1.2.3', 'a' * 40, Path('.'), 'b' * 40)

    def test_access_and_authentication_failures_stop_publication(self):
        for responses in [[(403, {})], [(200, {}), (200, []), (401, {})],
                          [(200, {}), (200, []), (200, {})], [(200, {}), (200, {})]]:
            with self.subTest(responses=responses), self.assertRaises(candidate.ReleaseError):
                iterator = iter(responses)
                candidate.ensure_unpublished('v1.2.3', 'actor', 'credential', lambda *args: next(iterator))

    def test_network_failures_stop_publication(self):
        with self.assertRaises(candidate.ReleaseError):
            candidate.ensure_unpublished('v1.2.3', 'actor', 'credential',
                                         lambda *args: (_ for _ in ()).throw(candidate.ReleaseError('network unavailable')))

    def test_inputs_reject_mutable_refs_and_malformed_versions(self):
        candidate.validate_inputs('v1.2.3', 'a' * 40)
        for tag, revision in [('v01.2.3', 'a' * 40), ('v1.2.3', 'main'), ('v1.2.3;echo bad', 'a' * 40), ('v1.2.3-01', 'a' * 40), ('v1.2.3+build', 'a' * 40)]:
            with self.subTest(tag=tag, revision=revision), self.assertRaises(candidate.ReleaseError):
                candidate.validate_inputs(tag, revision)

class ValidationTests(unittest.TestCase):
    def test_commit_validation_needs_no_tag_and_rejects_off_main_commits(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            def git(*args):
                return subprocess.check_output(['git', '-C', str(repo), *args], text=True,
                                               stderr=subprocess.DEVNULL).strip()
            git('init', '-b', 'main')
            git('config', 'user.email', 'fixture@example.test')
            git('config', 'user.name', 'Fixture')
            git('commit', '--allow-empty', '-m', 'candidate')
            revision = git('rev-parse', 'HEAD')
            git('update-ref', 'refs/remotes/origin/main', revision)
            epoch = candidate.validate_commit('v1.2.3', revision, repo)
            self.assertEqual(epoch, git('show', '-s', '--format=%ct', revision))
            self.assertEqual(git('tag', '--list'), '')
            git('commit', '--allow-empty', '-m', 'unmerged candidate')
            with self.assertRaises(subprocess.CalledProcessError):
                candidate.validate_commit('v1.2.3', git('rev-parse', 'HEAD'), repo)
            with self.assertRaises(candidate.ReleaseError):
                candidate.validate_commit('v1.2.3', git('rev-parse', 'HEAD^{tree}'), repo)


class TagTests(unittest.TestCase):
    def test_unpublished_tag_can_be_retargeted_with_exact_lease(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            remote, repo = root/'remote.git', root/'repo'
            subprocess.run(['git', 'init', '--bare', str(remote)], check=True, capture_output=True)
            subprocess.run(['git', 'clone', str(remote), str(repo)], check=True, capture_output=True)
            def git(*args):
                return subprocess.check_output(['git', '-C', str(repo), *args], text=True).strip()
            git('config', 'user.email', 'fixture@example.test')
            git('config', 'user.name', 'Fixture')
            git('commit', '--allow-empty', '-m', 'first')
            first = git('rev-parse', 'HEAD')
            candidate.update_tag('v1.2.3', first, repo)
            git('commit', '--allow-empty', '-m', 'repair')
            second = git('rev-parse', 'HEAD')
            candidate.update_tag('v1.2.3', second, repo)
            self.assertEqual(git('cat-file', '-t', 'v1.2.3'), 'tag')
            self.assertIn(second, git('ls-remote', 'origin', 'refs/tags/v1.2.3^{}'))
            # A concurrent replacement must not be overwritten by an old observation.
            with self.assertRaises(subprocess.CalledProcessError):
                candidate.update_tag('v1.2.3', first, repo, observed='0' * 40)
            self.assertIn(second, git('ls-remote', 'origin', 'refs/tags/v1.2.3^{}'))

if __name__ == '__main__':
    unittest.main()
