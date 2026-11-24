#!/usr/bin/env python3
"""Fail closed on missing or non-successful jobs; permit only planned skips."""
import importlib.util
import json
import os
from pathlib import Path
import sys

GO = {'cli-subprocess-race', 'go', 'go-test', 'go-race', 'go-race-backup', 'go-race-cli-shards', 'go-race-cli', 'go-race-api-template', 'go-race-api', 'go-race-api-shards', 'go-platforms', 'go-lint', 'go-shuffle', 'managed-environments'}
CODE = {'mooring-actual', 'mooring', 'image', 'binary', 'frontend', 'frontend-test', 'frontend-test-types', 'frontend-test-shards', 'harness-pr', 'cli-command-smoke', 'onboarding-default-boots', 'pages-apps', 'playwright-pr', 'onboarding-journey'}
# Word and Sheet packages and their browser gate (#5362): required with any code change.
CRAFT = {'craft-packages-build', 'craft-packages'}
PARITY = {'go-parity': 'go_parity', 'frontend-parity': 'frontend_parity', 'docs-inventory': 'docs_inventory'}
ALWAYS = {'changes', 'merge-conflict-markers', 'shell'}
# Expensive lanes a pull request may skip (plan output full=false, #4752).
# Any other event must plan and pass them: a reduced plan fails closed there.
FULL = {'go-platforms', 'go-race', 'go-race-backup', 'go-race-cli-shards', 'go-race-cli', 'go-race-api-template', 'go-race-api-shards', 'go-race-api', 'cli-subprocess-race', 'go-shuffle', 'mooring-actual'}


def failures(needs, event=None):
    required = ALWAYS | GO | CODE | PARITY.keys() | {'release-rehearsal'} | CRAFT
    errors = [f'{name}: missing result' for name in sorted(required - needs.keys())]
    plan = needs.get('changes', {}).get('outputs', {})
    if any(plan.get(k) not in ('true', 'false') for k in ('code', 'go', 'release', 'craft', 'full', *PARITY.values())):
        errors.append('missing or invalid change plan')
    if plan.get('full') == 'false' and event != 'pull_request':
        errors.append(f'reduced matrix is only allowed on pull_request, not {event}')
    for name in sorted(required & needs.keys()):
        expected = 'success'
        if (name in GO and plan.get('go') == 'false') or (name in CODE | CRAFT and plan.get('code') == 'false'):
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


def require_native_coverage(directory, expected_sha, run_id):
    receipts = [json.loads(path.read_text()) for path in sorted(directory.rglob("platform-shard.json"))]
    platforms = {"linux-arm64", "macos-arm64"}
    if len(receipts) != 6 or {r["platform"] for r in receipts} != platforms:
        raise ValueError("need six native receipts across both arm64 platforms")
    if not expected_sha or not run_id or any(
        r["source_sha"] != expected_sha or r["run_id"] != run_id for r in receipts
    ):
        raise ValueError("native receipts come from another revision or workflow run")
    cli = "github.com/crewship-ai/crewship/cmd/crewship"
    for platform in sorted(platforms):
        shards = [r for r in receipts if r["platform"] == platform]
        if sorted(r["index"] for r in shards) != [0, 1, 2] or any(r["count"] != 3 for r in shards):
            raise ValueError(f"{platform}: missing or duplicate shard")
        inventory = shards[0]["inventory"]
        if len(inventory) < 90 or len(set(inventory)) != len(inventory) or cli not in inventory:
            raise ValueError(f"{platform}: invalid Go inventory")
        seen = []
        for shard in shards:
            selected = shard["selected"]
            if shard["inventory"] != inventory or not selected or sorted(shard["completed"]) != sorted(selected):
                raise ValueError(f"{platform}: inconsistent inventory or completion evidence")
            if shard["cli_build"] is not (cli in selected):
                raise ValueError(f"{platform}: unexpected CLI build receipt")
            seen.extend(selected)
        if sorted(seen) != sorted(inventory):
            raise ValueError(f"{platform}: packages are missing or duplicated")
        print(f"{platform}: all {len(inventory)} packages covered exactly once")


def require_backup_coverage(directory, expected_sha, run_id):
    if not expected_sha or not run_id:
        raise ValueError('missing workflow identity')
    path = Path(__file__).with_name('api-race-shard.py')
    spec = importlib.util.spec_from_file_location('race_shard', path)
    shard = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(shard)
    receipts = [json.loads(path.read_text()) for path in sorted(directory.rglob('api-race-shard.json'))]
    _, total = shard.validate_manifests(receipts, 2, expected_sha,
                                        expected_package=shard.BACKUP_PACKAGE, run_id=run_id)
    print(f'internal/backup: all {total} top-level tests covered exactly once')


if __name__ == '__main__':
    needs = json.loads(os.environ['CI_NEEDS'])
    errors = failures(needs, os.environ.get('GITHUB_EVENT_NAME'))
    if not errors:
        for job, directory, verify in (
            ('go-platforms', '.ci-native-platforms', require_native_coverage),
            ('go-race-backup', '.ci-backup-race', require_backup_coverage),
        ):
            if needs[job]['result'] != 'success':
                continue
            try:
                verify(Path(directory), os.environ['GITHUB_SHA'], os.environ['GITHUB_RUN_ID'])
            except (KeyError, OSError, TypeError, ValueError) as error:
                errors.append(f'{job}: {error}')
    for error in errors:
        print(f'::error::{error}')
    sys.exit(bool(errors))
