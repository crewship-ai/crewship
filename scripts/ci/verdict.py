#!/usr/bin/env python3
"""Fail closed on missing or non-successful jobs; permit only planned skips."""
import json
import os
import sys

GO = {'cli-subprocess-race', 'go', 'go-race', 'go-race-cli-shards', 'go-race-cli', 'go-race-api', 'go-race-api-shards', 'go-platforms', 'go-lint', 'go-shuffle', 'managed-environments'}
CODE = {'image', 'binary', 'frontend', 'frontend-test', 'harness-pr', 'cli-command-smoke', 'onboarding-default-boots', 'pages-apps', 'playwright-pr', 'onboarding-journey'}
PARITY = {'go-parity': 'go_parity', 'frontend-parity': 'frontend_parity', 'docs-inventory': 'docs_inventory'}
ALWAYS = {'changes', 'merge-conflict-markers', 'shell'}


def failures(needs):
    required = ALWAYS | GO | CODE | PARITY.keys() | {'release-rehearsal'}
    errors = [f'{name}: missing result' for name in sorted(required - needs.keys())]
    plan = needs.get('changes', {}).get('outputs', {})
    if any(plan.get(k) not in ('true', 'false') for k in ('code', 'go', 'release', *PARITY.values())):
        errors.append('missing or invalid change plan')
    for name in sorted(required & needs.keys()):
        expected = 'success'
        if (name in GO and plan.get('go') == 'false') or (name in CODE and plan.get('code') == 'false'):
            expected = 'skipped'
        if name in PARITY and plan.get(PARITY[name]) == 'false':
            expected = 'skipped'
        if name == 'release-rehearsal' and plan.get('release') == 'false':
            expected = 'skipped'
        actual = needs[name].get('result')
        if actual != expected:
            errors.append(f'{name}: expected {expected}, got {actual}')
    return errors


if __name__ == '__main__':
    errors = failures(json.loads(os.environ['CI_NEEDS']))
    for error in errors:
        print(f'::error::{error}')
    sys.exit(bool(errors))
