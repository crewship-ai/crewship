#!/usr/bin/env python3
"""Fail closed on missing or non-successful jobs; permit only planned skips."""
import json
import os
import sys

GO = {'cli-subprocess-race', 'go', 'go-test', 'go-race', 'go-race-cli-shards', 'go-race-cli', 'go-race-api-template', 'go-race-api', 'go-race-api-shards', 'go-platforms', 'go-lint', 'go-shuffle', 'managed-environments'}
CODE = {'mooring-actual', 'mooring', 'image', 'binary', 'frontend', 'frontend-test', 'frontend-test-types', 'frontend-test-shards', 'harness-pr', 'cli-command-smoke', 'onboarding-default-boots', 'pages-apps', 'playwright-pr', 'onboarding-journey'}
PARITY = {'go-parity': 'go_parity', 'frontend-parity': 'frontend_parity', 'docs-inventory': 'docs_inventory'}
ALWAYS = {'changes', 'merge-conflict-markers', 'shell'}
# Expensive lanes a pull request may skip (plan output full=false, #4752).
# Any other event must plan and pass them: a reduced plan fails closed there.
FULL = {'go-platforms', 'go-race', 'go-race-cli-shards', 'go-race-cli', 'go-race-api-template', 'go-race-api-shards', 'go-race-api', 'cli-subprocess-race', 'go-shuffle', 'mooring-actual'}


def failures(needs, event=None):
    required = ALWAYS | GO | CODE | PARITY.keys() | {'release-rehearsal'}
    errors = [f'{name}: missing result' for name in sorted(required - needs.keys())]
    plan = needs.get('changes', {}).get('outputs', {})
    if any(plan.get(k) not in ('true', 'false') for k in ('code', 'go', 'release', 'full', *PARITY.values())):
        errors.append('missing or invalid change plan')
    if plan.get('full') == 'false' and event != 'pull_request':
        errors.append(f'reduced matrix is only allowed on pull_request, not {event}')
    for name in sorted(required & needs.keys()):
        expected = 'success'
        if (name in GO and plan.get('go') == 'false') or (name in CODE and plan.get('code') == 'false'):
            expected = 'skipped'
        if name in PARITY and plan.get(PARITY[name]) == 'false':
            expected = 'skipped'
        if name == 'release-rehearsal' and plan.get('release') == 'false':
            expected = 'skipped'
        if name in FULL and plan.get('full') == 'false':
            expected = 'skipped'
        actual = needs[name].get('result')
        if actual != expected:
            errors.append(f'{name}: expected {expected}, got {actual}')
    return errors


if __name__ == '__main__':
    errors = failures(json.loads(os.environ['CI_NEEDS']), os.environ.get('GITHUB_EVENT_NAME'))
    for error in errors:
        print(f'::error::{error}')
    sys.exit(bool(errors))
