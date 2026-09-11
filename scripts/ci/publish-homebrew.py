#!/usr/bin/env python3
"""Publish both generated formulas in one fast-forward commit after release."""
import argparse
import json
from pathlib import Path
import re
import subprocess


def api(path, body=None):
    command = ['gh', 'api', path]
    if body is not None:
        command += ['--method', 'POST', '--input', '-']
    return json.loads(subprocess.check_output(command, input=json.dumps(body).encode() if body is not None else None))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('tag')
    parser.add_argument('--tap', default='crewship-ai/homebrew-tap')
    args = parser.parse_args()
    if not re.fullmatch(r'v\d+\.\d+\.\d+', args.tag):
        print('Prerelease: leave stable Homebrew formulas unchanged')
        raise SystemExit(0)
    repo = 'repos/' + args.tap
    branch = api(repo)['default_branch']
    head = api(f'{repo}/git/ref/heads/{branch}')['object']['sha']
    base_tree = api(f'{repo}/git/commits/{head}')['tree']['sha']
    entries = []
    for name in ('crewship', 'crewship-cli'):
        path = f'Formula/{name}.rb'
        content = (Path('dist/homebrew') / path).read_text()
        if f'/download/{args.tag}/' not in content:
            raise SystemExit(f'{path} does not reference the released version')
        entries.append({'path': path, 'mode': '100644', 'type': 'blob', 'content': content})
    tree = api(f'{repo}/git/trees', {'base_tree': base_tree, 'tree': entries})['sha']
    if tree == base_tree:
        print('Homebrew formulas already match this release')
        raise SystemExit(0)
    commit = api(f'{repo}/git/commits', {'message': f'chore: update Crewship formulas to {args.tag}', 'tree': tree, 'parents': [head]})['sha']
    # Explicit PATCH, with force=false: concurrent tap changes must not be overwritten.
    subprocess.run(['gh', 'api', '--method', 'PATCH', f'{repo}/git/refs/heads/{branch}', '--input', '-'],
                   input=json.dumps({'sha': commit, 'force': False}).encode(), check=True)


if __name__ == '__main__':
    main()
