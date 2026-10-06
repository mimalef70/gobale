#!/usr/bin/env python3
"""Build/verify the version-coupled embedded UI; never opens runtime storage."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / 'src/ui/web/dist'


def verify():
    version = re.search(r'const AppVersion = "([^"]+)"', (ROOT / 'src/config/settings.go').read_text())[1]
    contract = hashlib.sha256((ROOT / 'docs/openapi.yaml').read_bytes()).hexdigest()
    manifest = json.loads((DIST / 'build-manifest.json').read_text())
    if manifest.get('schema_version') != 1 or manifest.get('version') != version or manifest.get('openapi_sha256') != contract:
        raise SystemExit('Embedded UI version/contract is stale; run make ui-build')
    files = manifest.get('files', {})
    if not {'index.html', 'THIRD_PARTY_NOTICES.txt'} <= files.keys():
        raise SystemExit('Embedded UI index or license notices missing')
    actual = {p.relative_to(DIST).as_posix() for p in DIST.rglob('*') if p.is_file()} - {'build-manifest.json', '.placeholder'}
    if actual != files.keys():
        raise SystemExit('Embedded UI file inventory differs from manifest')
    for name, digest in files.items():
        path = Path(name)
        if path.is_absolute() or '..' in path.parts or any(x.startswith('.') for x in path.parts):
            raise SystemExit('Unsafe embedded UI file path')
        file = DIST / path
        if file.is_symlink() or hashlib.sha256(file.read_bytes()).hexdigest() != digest:
            raise SystemExit('Embedded UI file checksum mismatch')
    print(f'Embedded UI verified: version {version}, {len(files)} local files')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--skip-install', action='store_true')
    args = parser.parse_args()
    if not args.check:
        if not args.skip_install:
            subprocess.run(['npm', 'ci', '--no-fund'], cwd=ROOT / 'ui', check=True)
        subprocess.run(['npm', 'run', 'build'], cwd=ROOT / 'ui', check=True)
    verify()


if __name__ == '__main__':
    main()
