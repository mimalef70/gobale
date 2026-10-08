#!/usr/bin/env python3
"""Publish a clean reviewed commit and wait for CI, publication and public verification.

This is an explicit release command, never a background/on-commit publisher.
Version/changelog edits and review happen before committing and invoking it.
"""
import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
import time

from release_notes import extract_release_notes, release_metadata
from verify_release import REPOSITORY, verify_assets, verify_registries

ROOT = Path(__file__).resolve().parents[1]


def run(*command):
    return subprocess.check_output(command, cwd=ROOT, text=True).strip()


def execute(*command):
    subprocess.run(command, cwd=ROOT, check=True)


def workflow_runs(workflow, **filters):
    command = ['gh', 'run', 'list', '--repo', REPOSITORY, '--workflow', workflow,
               '--limit', '100', '--json', 'databaseId,status,conclusion,headSha,headBranch,event,displayTitle,url']
    for key, value in filters.items():
        command.extend(['--' + key, value])
    return json.loads(run(*command))


def wait_for_run(workflow, predicate, **filters):
    deadline = time.monotonic() + 7200
    last = None
    while time.monotonic() < deadline:
        matches = [row for row in workflow_runs(workflow, **filters) if predicate(row)]
        if matches:
            row = matches[0]
            state = (row['databaseId'], row['status'], row['conclusion'])
            if state != last:
                print(workflow + ': ' + row['url'] + ' ' + row['status'] + ' ' + str(row['conclusion']), flush=True)
                last = state
            if row['status'] == 'completed':
                if row['conclusion'] != 'success':
                    raise ValueError('Workflow failed; inspect ' + row['url'] + ' before retrying. Nothing is overwritten.')
                return row
        time.sleep(20)
    raise ValueError('Workflow did not finish within two hours; inspect Actions before resuming')


def check_clean_version(version):
    if run('git', 'status', '--porcelain'):
        raise ValueError('Review and commit all release changes first; release never commits an unreviewed worktree')
    configured = re.search(r'const AppVersion = "([^"]+)"', (ROOT / 'src/config/settings.go').read_text())[1]
    if configured != version:
        raise ValueError('Requested version differs from binary version')
    extract_release_notes((ROOT / 'CHANGELOG.md').read_text(), version)
    execute(sys.executable, 'scripts/generate_openapi.py', '--check')


def ensure_tag(tag, sha):
    remote = run('git', 'ls-remote', 'origin', 'refs/tags/' + tag, 'refs/tags/' + tag + '^{}')
    if remote:
        execute('git', 'fetch', 'origin', 'refs/tags/' + tag + ':refs/tags/' + tag)
        if run('git', 'rev-parse', tag + '^{commit}') != sha:
            raise ValueError('Existing tag points elsewhere; published tags are never moved')
        return
    existing = subprocess.run(['git', 'rev-parse', '--verify', '--quiet', 'refs/tags/' + tag], cwd=ROOT, capture_output=True)
    if existing.returncode == 0:
        if run('git', 'rev-parse', tag + '^{commit}') != sha:
            raise ValueError('Local tag points elsewhere; inspect instead of overwriting')
    else:
        execute('git', 'tag', '-a', tag, sha, '-m', 'GoBale ' + tag)
    execute('git', 'push', 'origin', 'refs/tags/' + tag)


def publish(version):
    tag = 'v' + version
    release_metadata(tag)
    check_clean_version(version)
    identity = json.loads(run('gh', 'repo', 'view', '--json', 'nameWithOwner,defaultBranchRef'))
    if identity['nameWithOwner'].lower() != REPOSITORY or identity['defaultBranchRef']['name'] != 'main':
        raise ValueError('Expected the reviewed GoBale repository with main as default branch')
    # Secrets are inspected by name only; values never enter this process.
    secrets = {row['name'] for row in json.loads(run('gh', 'secret', 'list', '--repo', REPOSITORY, '--json', 'name'))}
    if not {'DOCKERHUB_USERNAME', 'DOCKERHUB_TOKEN'} <= secrets:
        raise ValueError('Docker Hub publishing secrets are missing')
    origin = run('git', 'remote', 'get-url', 'origin')
    if origin not in ('https://github.com/' + REPOSITORY + '.git', 'git@github.com:' + REPOSITORY + '.git'):
        raise ValueError('Origin must be the reviewed GoBale GitHub repository')
    sha = run('git', 'rev-parse', 'HEAD')
    execute('git', 'fetch', 'origin', 'main')
    execute('git', 'merge-base', '--is-ancestor', 'origin/main', sha)
    execute('git', 'push', 'origin', 'HEAD:refs/heads/main')
    wait_for_run('ci.yml', lambda row: row['headSha'] == sha and row['headBranch'] == 'main', commit=sha, event='push', branch='main')
    # API reference publication is part of the release completion contract.
    pages = workflow_runs('pages.yml', commit=sha)
    if not pages:
        execute('gh', 'workflow', 'run', 'pages.yml', '--repo', REPOSITORY, '--ref', 'main')
    wait_for_run('pages.yml', lambda row: row['headSha'] == sha, commit=sha)
    if run('gh', 'api', 'repos/' + REPOSITORY + '/commits/main', '--jq', '.sha') != sha:
        raise ValueError('main advanced during CI; inspect before tagging or publishing')
    ensure_tag(tag, sha)
    wait_for_run('ci.yml', lambda row: row['headSha'] == sha and row['headBranch'] == tag, commit=sha, event='push', branch=tag)
    previous = workflow_runs('release.yml')
    matching = [row for row in previous if row['displayTitle'] == 'Release ' + tag and row['headSha'] == sha]
    if matching:
        chosen = matching[0]['databaseId']
        print('Resuming existing release run; no publication is repeated.', flush=True)
    else:
        previous_ids = {row['databaseId'] for row in previous}
        execute('gh', 'workflow', 'run', 'release.yml', '--repo', REPOSITORY, '--ref', 'main', '-f', 'tag=' + tag)
        chosen = None
    completed = wait_for_run('release.yml', lambda row: row['headSha'] == sha and row['displayTitle'] == 'Release ' + tag and
                             (row['databaseId'] == chosen if chosen else row['databaseId'] not in previous_ids))
    digest = verify_registries(tag)
    url = verify_assets(tag, sha)
    print(json.dumps({'status': 'verified', 'tag': tag, 'revision': sha, 'release': url,
                      'workflow': completed['url'], 'digest': digest,
                      'dockerhub': 'https://hub.docker.com/r/' + REPOSITORY,
                      'native_linux_restart_checks': ['amd64', 'arm64']}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True, help='Prepared version without v prefix')
    args = parser.parse_args()
    try:
        publish(args.version)
    except (ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, 'Release stopped: ' + str(error) + '\n')


if __name__ == '__main__':
    main()
