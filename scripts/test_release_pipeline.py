"""Failure-boundary tests for the explicit publication coordinator and public proof."""
import hashlib
import io
import ssl
import urllib.error
import json
from pathlib import Path
import unittest
from unittest.mock import patch
import yaml

from release import ensure_tag, wait_for_run
from verify_release import parse_checksums, validate_manifest, verify_registries, public_bytes
from release_registry import RegistryError


class PublicProofTest(unittest.TestCase):
    def test_transient_download_retries_never_publish_or_disable_tls(self):
        with patch('verify_release.urllib.request.urlopen', side_effect=[ssl.SSLError('synthetic interrupted read'), io.BytesIO(b'verified')]) as request, patch('verify_release.time.sleep') as sleep:
            self.assertEqual(public_bytes('https://example.test/asset'), b'verified')
            self.assertEqual(request.call_count, 2)
            self.assertNotIn('Authorization', request.call_args.args[0].headers)
            self.assertEqual(request.call_args.kwargs, {'timeout': 60})
            sleep.assert_called_once_with(1)
        denied = urllib.error.HTTPError('https://example.test/asset', 403, 'denied', {}, None)
        with patch('verify_release.urllib.request.urlopen', side_effect=denied) as request, patch('verify_release.time.sleep') as sleep:
            with self.assertRaises(urllib.error.HTTPError):
                public_bytes('https://example.test/asset')
            self.assertEqual(request.call_count, 1)
            sleep.assert_not_called()
        with patch('verify_release.urllib.request.urlopen', side_effect=TimeoutError('synthetic')) as request, patch('verify_release.time.sleep'):
            with self.assertRaises(TimeoutError):
                public_bytes('https://example.test/asset')
            self.assertEqual(request.call_count, 3)

    def test_anonymous_registry_calls_and_same_immutable_build(self):
        raw = json.dumps({'manifests': [{'platform': {'os': 'linux', 'architecture': arch}}
                                       for arch in ('amd64', 'arm64')]}).encode()
        digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
        with patch('verify_release.read_manifest', return_value=(200, {}, raw)) as read:
            self.assertEqual(verify_registries('v2.1.0', digest), digest)
            self.assertEqual(read.call_count, 4)
            for call in read.call_args_list:
                self.assertEqual(call.args[0][-2:], ('', ''))
        for response in ((401, {}, b'{}'), (200, {}, b'{}'), (200, {'Docker-Content-Digest': 'wrong'}, raw)):
            with self.subTest(response=response), patch('verify_release.read_manifest', return_value=response):
                with self.assertRaises(RegistryError):
                    verify_registries('v2.1.0', digest)
        with patch('verify_release.read_manifest', side_effect=[(200, {}, raw), (200, {}, raw + b' ') ]):
            with self.assertRaises(RegistryError):
                verify_registries('v2.1.0')

    def test_manifest_binds_version_revision_all_archives_and_checksums(self):
        packages = [{'platform': platform, 'file': 'gobale_2.1.0_' + platform.replace('/', '_') + '.tar.gz',
                     'sha256': 'a' * 64, 'bytes': 123}
                    for platform in ('linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64')]
        manifest = {'version': '2.1.0', 'revision': 'b' * 40, 'packages': packages}
        sums = {row['file']: row['sha256'] for row in packages} | {'manifest.json': 'c' * 64}
        self.assertEqual(validate_manifest(manifest, sums, '2.1.0', 'b' * 40), packages)
        for changed in (manifest | {'revision': 'c' * 40}, manifest | {'version': '2.0.1'},
                        manifest | {'packages': packages[:3]}):
            with self.assertRaises(ValueError):
                validate_manifest(changed, sums, '2.1.0', 'b' * 40)
        with self.assertRaises(ValueError):
            validate_manifest(manifest, sums | {'unexpected.tar.gz': 'a' * 64}, '2.1.0', 'b' * 40)
        with self.assertRaises(ValueError):
            validate_manifest(manifest, sums | {packages[0]['file']: 'd' * 64}, '2.1.0', 'b' * 40)
        for raw in ((('a' * 64) + '  ../bad.tar.gz\n').encode(), ((('a' * 64) + '  a.tar.gz\n') * 2).encode()):
            with self.assertRaises(ValueError):
                parse_checksums(raw)


class CoordinatorTest(unittest.TestCase):
    def test_existing_tag_never_moves(self):
        with patch('release.run', side_effect=['remote-tag', 'b' * 40]), patch('release.execute') as execute:
            with self.assertRaisesRegex(ValueError, 'never moved'):
                ensure_tag('v2.1.0', 'a' * 40)
            self.assertEqual(execute.call_count, 1)
            self.assertEqual(execute.call_args.args[:2], ('git', 'fetch'))

    def test_ci_failure_stops_and_never_counts_another_revision(self):
        failed = {'headSha': 'a' * 40, 'databaseId': 1, 'status': 'completed', 'conclusion': 'failure', 'url': 'https://example.test/run/1'}
        other = failed | {'headSha': 'b' * 40, 'conclusion': 'success'}
        with patch('release.workflow_runs', return_value=[other, failed]):
            with self.assertRaisesRegex(ValueError, 'Workflow failed'):
                wait_for_run('ci.yml', lambda row: row['headSha'] == 'a' * 40)

    def test_public_jobs_use_native_architectures_and_no_publishing_credentials(self):
        root = Path(__file__).resolve().parents[1]
        workflow = yaml.safe_load((root / '.github/workflows/release.yml').read_text())
        job = workflow['jobs']['verify-public']
        self.assertEqual(set(job['needs']), {'prepare', 'image', 'release'})
        self.assertEqual(job['permissions'], {'contents': 'read'})
        matrix = job['strategy']['matrix']['include']
        self.assertEqual({r['runner'] for r in matrix}, {'ubuntu-24.04', 'ubuntu-24.04-arm'})
        raw = json.dumps(job)
        self.assertNotIn('secrets.', raw)
        self.assertNotIn('login-action', raw)
        self.assertIn('gobale-anonymous-docker', raw)
        self.assertIn('scripts/verify_release.py', raw)
        self.assertIn('scripts/docker_smoke.py', raw)


if __name__ == '__main__':
    unittest.main()
