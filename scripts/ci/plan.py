#!/usr/bin/env python3
"""Conservative CI routing. Unknown inputs always require the full Go suite."""
import json
import os
import subprocess


def release_changed(paths):
    return any(path in {'Dockerfile', '.goreleaser.yml', 'go.mod', 'go.sum', 'package.json', 'pnpm-lock.yaml'}
               or path.startswith(('packaging/', 'scripts/ci/', '.github/workflows/', '.github/actions/')) for path in paths)


def classify(paths):
    code = go = False
    for path in paths:
        if path.startswith('docs/') or path in {'README.md', 'CHANGELOG.md', 'LICENSE', 'CONTRIBUTING.md', 'AGENTS.md', 'CODEX.md'}:
            continue
        code = True
        if path.startswith(('app/', 'components/', 'hooks/', 'lib/', 'stores/', 'public/', 'e2e/')) or path in {'next.config.ts', 'tsconfig.json', 'vitest.config.ts', 'postcss.config.mjs', 'eslint.config.mjs'}:
            continue
        # Markdown outside documentation can be embedded runtime data/skills.
        # Dependency, workflow, Docker, scripts and unrecognised paths run all.
        go = True
    return {'code': code, 'go': go}


def main():
    event = json.load(open(os.environ['GITHUB_EVENT_PATH']))
    name = os.environ['GITHUB_EVENT_NAME']
    if name == 'pull_request':
        base, head = event['pull_request']['base']['sha'], event['pull_request']['head']['sha']
    elif name == 'merge_group':
        base, head = event['merge_group']['base_sha'], event['merge_group']['head_sha']
    else:
        base = head = None
    # Pushes, including docs-only commits, get complete evidence for publication.
    if base:
        paths = subprocess.check_output(['git', 'diff', '--name-only', '-z', f'{base}...{head}']).decode().split('\0')
        result = classify([p for p in paths if p])
        result['release'] = release_changed(paths)
    else:
        result = {'code': True, 'go': True, 'release': name == 'workflow_dispatch'}
    with open(os.environ['GITHUB_OUTPUT'], 'a') as out:
        for key, value in result.items():
            print(f'{key}={str(value).lower()}', file=out)
    print(json.dumps(result))


if __name__ == '__main__':
    main()
