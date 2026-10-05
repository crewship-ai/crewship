#!/usr/bin/env python3
"""Conservative CI routing. Unknown inputs always require the full Go suite."""
import json
import os
from pathlib import PurePosixPath
import subprocess


def release_changed(paths):
    return any(path in {'Dockerfile', '.goreleaser.yml', 'go.mod', 'go.sum', 'package.json', 'pnpm-lock.yaml'}
               or path.startswith(('packaging/', 'scripts/ci/', '.github/workflows/', '.github/actions/')) for path in paths)


# Docs may contain executable/build inputs. Only prose and display assets are
# safe to skip; an unknown extension under docs/ is still an unknown input.
DOC_EXTENSIONS = {'.md', '.mdx', '.txt', '.svg', '.png', '.jpg', '.jpeg', '.gif', '.webp', '.avif', '.ico'}
DOC_ROOT_FILES = {'README.md', 'CHANGELOG.md', 'LICENSE', 'CONTRIBUTING.md', 'AGENTS.md', 'CODEX.md'}
GO_PARITY_INPUTS = {
    'docs/configuration/providers.mdx',
    'lib/crew-icons.ts', 'lib/colors.ts', 'lib/notification-categories.ts',
    'components/features/admin/backups/backups-model.ts',
}
FRONTEND_PARITY_INPUTS = {'docs/api-reference/websocket.mdx'}


def changed_paths(base, head):
    # Rename detection can hide a deleted runtime input behind its new docs
    # path. Emit both paths so routing retains the source-side requirements.
    return [p for p in subprocess.check_output([
        'git', 'diff', '--no-renames', '--name-only', '-z', f'{base}...{head}',
    ]).decode().split('\0') if p]


def classify(paths):
    code = go = False
    for path in paths:
        if (path.startswith('docs/') and PurePosixPath(path).suffix.lower() in DOC_EXTENSIONS) or path in DOC_ROOT_FILES:
            continue
        code = True
        if path.startswith(('app/', 'components/', 'hooks/', 'lib/', 'stores/', 'public/', 'e2e/')) or path in {'next.config.ts', 'tsconfig.json', 'vitest.config.ts', 'postcss.config.mjs', 'eslint.config.mjs'}:
            continue
        # Markdown outside documentation can be embedded runtime data/skills.
        # Dependency, workflow, Docker, scripts and unrecognised paths run all.
        go = True
    return {
        'code': code, 'go': go,
        # Full suites already execute these tests. Dedicated lanes restore
        # cross-language evidence only when the full owning suite is skipped.
        'go_parity': not go and bool(set(paths) & GO_PARITY_INPUTS),
        'frontend_parity': not code and bool(set(paths) & FRONTEND_PARITY_INPUTS),
    }


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
        paths = changed_paths(base, head)
        result = classify(paths)
        result['release'] = release_changed(paths)
    else:
        result = classify(['unknown/full-suite'])
        result['release'] = name == 'workflow_dispatch'
    with open(os.environ['GITHUB_OUTPUT'], 'a') as out:
        for key, value in result.items():
            print(f'{key}={str(value).lower()}', file=out)
    print(json.dumps(result))


if __name__ == '__main__':
    main()
