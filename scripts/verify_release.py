#!/usr/bin/env python3
"""Verify public release assets and both registries without account credentials."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

from release_notes import release_metadata
from release_registry import RegistryError, read_manifest

REPOSITORY = 'mimalef70/gobale'
PLATFORMS = {'linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64'}
ROOT = Path(__file__).resolve().parents[1]


def public_bytes(url, maximum=2 * 1024 * 1024):
    # No gh, Docker config, cookies, Authorization, or private environment inputs.
    request = urllib.request.Request(url, headers={'User-Agent': 'GoBale-release-verifier'})
    # Only idempotent anonymous reads retry. Validation failures, absent assets
    # and denied access remain failures; publication is never retried here.
    for attempt in range(3):
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                data = response.read(maximum + 1)
                if len(data) > maximum:
                    raise ValueError('Public response exceeded its size bound')
                return data
        except urllib.error.HTTPError as error:
            error.close()
            if error.code not in {408, 429, 500, 502, 503, 504} or attempt == 2:
                raise
        except (OSError, urllib.error.URLError):
            if attempt == 2:
                raise
        time.sleep(2 ** attempt)


def public_json(url):
    return json.loads(public_bytes(url))


def public_registry_targets():
    return (
        ('ghcr.io/' + REPOSITORY, 'https://ghcr.io', 'https://ghcr.io/token',
         'ghcr.io', REPOSITORY, '', ''),
        ('docker.io/' + REPOSITORY, 'https://registry-1.docker.io', 'https://auth.docker.io/token',
         'registry.docker.io', REPOSITORY, '', ''),
    )


def verify_registries(tag, expected_digest=''):
    release_metadata(tag)
    digests = []
    for target in public_registry_targets():
        status, headers, raw = read_manifest(target, tag)
        digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
        if status != 200 or (expected_digest and digest != expected_digest):
            raise RegistryError(target[0] + ': anonymous version/digest verification failed')
        if headers.get('Docker-Content-Digest', digest) != digest:
            raise RegistryError('Registry digest header mismatch')
        index = json.loads(raw)
        platforms = {row.get('platform', {}).get('architecture') for row in index.get('manifests', [])
                     if row.get('platform', {}).get('os') == 'linux'}
        if platforms != {'amd64', 'arm64'}:
            raise RegistryError('Public index is missing a supported Linux architecture')
        # The immutable reference must resolve to exactly the same bytes too.
        status, _, by_digest = read_manifest(target, digest)
        if status != 200 or by_digest != raw:
            raise RegistryError('Public immutable digest does not resolve to the versioned index')
        digests.append(digest)
    if len(set(digests)) != 1:
        raise RegistryError('Public registries do not contain the same build')
    return digests[0]


def validate_manifest(manifest, checksums, version, revision):
    if manifest.get('version') != version or manifest.get('revision') != revision:
        raise ValueError('Public manifest version/revision mismatch')
    packages = manifest.get('packages', [])
    if len(packages) != 4 or {p.get('platform') for p in packages} != PLATFORMS:
        raise ValueError('Expected exactly four platform packages')
    expected = {'manifest.json'}
    for item in packages:
        name = 'gobale_' + version + '_' + item['platform'].replace('/', '_') + '.tar.gz'
        if item.get('file') != name or not re.fullmatch('[a-f0-9]{64}', item.get('sha256', '')):
            raise ValueError('Invalid package name or checksum')
        if checksums.get(name) != item['sha256'] or not 0 < item.get('bytes', 0) <= 128 * 1024 * 1024:
            raise ValueError('Package checksum or size mismatch')
        expected.add(name)
    if set(checksums) != expected:
        raise ValueError('Checksum inventory differs from release manifest')
    return packages


def parse_checksums(raw):
    result = {}
    for line in raw.decode('ascii').splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  ([A-Za-z0-9_.-]+)', line)
        if not match or match[2] in result:
            raise ValueError('Invalid or duplicate checksum entry')
        result[match[2]] = match[1]
    return result


def verify_assets(tag, revision):
    version = release_metadata(tag)['version']
    api = 'https://api.github.com/repos/' + REPOSITORY
    remote = public_json(api + '/commits/' + tag)
    if remote.get('sha') != revision:
        raise ValueError('Public tag points to a different revision')
    release = public_json(api + '/releases/tags/' + tag)
    if release.get('draft') or release.get('tag_name') != tag:
        raise ValueError('Release is missing or still a draft')
    base = 'https://github.com/' + REPOSITORY + '/releases/download/' + tag + '/'
    sums = parse_checksums(public_bytes(base + 'SHA256SUMS'))
    raw_manifest = public_bytes(base + 'manifest.json')
    if hashlib.sha256(raw_manifest).hexdigest() != sums.get('manifest.json'):
        raise ValueError('Manifest checksum mismatch')
    packages = validate_manifest(json.loads(raw_manifest), sums, version, revision)
    assets = release.get('assets', [])
    if len(assets) != 6 or {row['name'] for row in assets} != set(sums) | {'SHA256SUMS'}:
        raise ValueError('Public release assets are incomplete or unexpected')
    with tempfile.TemporaryDirectory(prefix='gobale-public-release-') as directory:
        for item in packages:
            data = public_bytes(base + item['file'], item['bytes'])
            if len(data) != item['bytes'] or hashlib.sha256(data).hexdigest() != item['sha256']:
                raise ValueError('Public archive checksum mismatch: ' + item['file'])
            path = Path(directory) / item['file']
            path.write_bytes(data)
            with tarfile.open(path) as archive:
                members = archive.getmembers()
                names = [m.name for m in members]
                if len(names) != len(set(names)) or any(not m.isfile() or m.name.startswith('/') or '..' in Path(m.name).parts for m in members):
                    raise ValueError('Unsafe archive inventory')
                if sum(m.size for m in members) > 256 * 1024 * 1024:
                    raise ValueError('Archive expands beyond the distribution limit')
                for required in ('gobale', 'LICENCE.txt', 'docs/openapi.yaml', 'CHANGELOG.md', 'THIRD_PARTY_NOTICES.txt'):
                    if required not in names:
                        raise ValueError('Archive lacks ' + required)
                for relative in ('LICENCE.txt', 'docs/openapi.yaml', 'CHANGELOG.md'):
                    if archive.extractfile(relative).read() != (ROOT / relative).read_bytes():
                        raise ValueError('Archive does not match reviewed source: ' + relative)
            print('Anonymous archive verified: ' + item['file'], flush=True)
    return release['html_url']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--sha', required=True)
    parser.add_argument('--digest', default='')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    release_metadata(args.tag)
    if not re.fullmatch('[a-f0-9]{40}', args.sha):
        parser.error('Expected the full reviewed Git commit SHA')
    digest = verify_registries(args.tag, args.digest)
    url = verify_assets(args.tag, args.sha)
    report = {'tag': args.tag, 'revision': args.sha, 'digest': digest, 'release': url,
              'anonymous_assets_verified': True, 'anonymous_registries_verified': True}
    if args.output:
        args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report), flush=True)


if __name__ == '__main__':
    main()
