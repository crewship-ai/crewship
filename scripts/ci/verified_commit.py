#!/usr/bin/env python3
"""Require the latest trusted main push verdict for each workflow at this SHA."""
import argparse
import json
import re
import subprocess
import time

WORKFLOWS = ('ci.yml', 'security.yml', 'codeql.yml')


def verdict(runs, sha, repo):
    trusted = [r for r in runs if r.get('head_sha') == sha and r.get('event') == 'push'
               and r.get('head_branch') == 'main' and r.get('head_repository', {}).get('full_name') == repo]
    if not trusted:
        return 'pending'
    latest = max(trusted, key=lambda r: (r['id'], r.get('run_attempt', 1)))
    if latest.get('status') != 'completed':
        return 'pending'
    return 'success' if latest.get('conclusion') == 'success' else 'failure'


def api(path):
    return json.loads(subprocess.check_output(['gh', 'api', path], timeout=45))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('repo')
    parser.add_argument('sha')
    parser.add_argument('--wait-seconds', type=int, default=600)
    args = parser.parse_args()
    if not re.fullmatch(r'[0-9a-f]{40}', args.sha):
        parser.error('expected a full commit SHA')
    deadline = time.monotonic() + args.wait_seconds
    while True:
        states = {}
        for workflow in WORKFLOWS:
            runs = api(f'repos/{args.repo}/actions/workflows/{workflow}/runs?head_sha={args.sha}&event=push&per_page=100')['workflow_runs']
            states[workflow] = verdict(runs, args.sha, args.repo)
        print(json.dumps(states), flush=True)
        if all(v == 'success' for v in states.values()):
            return
        if 'failure' in states.values() or time.monotonic() >= deadline:
            raise SystemExit('Commit is not verified by CI, Security and CodeQL on main; refusing publication.')
        time.sleep(20)


if __name__ == '__main__':
    main()
