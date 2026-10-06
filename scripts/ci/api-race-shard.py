#!/usr/bin/env python3
"""Run exhaustive API race partitions; verify their combined coverage and budget.

run INDEX COUNT TIMEOUT_SECONDS enumerates the race-built package, retains each
parent with all subtests, and writes evidence to CI_RESULTS_DIR (.ci-results).
report DIRECTORY COUNT BASELINE_SECONDS validates downloaded shard artifacts.
Uses only local Go/git tooling; never contacts a running Crewship instance.
"""
import hashlib
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
MODULE = 'github.com/crewship-ai/crewship'
# One script partitions every race package that outgrew a single runner.
# Default stays internal/api so existing invocations keep their meaning.
PACKAGE = os.environ.get('RACE_SHARD_PACKAGE', MODULE + '/internal/api')
if not PACKAGE.startswith(MODULE + '/') or not re.fullmatch(r'[\w./-]+', PACKAGE) or '..' in PACKAGE:
    raise SystemExit(f'::error::invalid RACE_SHARD_PACKAGE {PACKAGE!r}')
PACKAGE_DIR = './' + PACKAGE[len(MODULE) + 1:]
# Linux limits EACH argv string, independently of total ARG_MAX. Leave room
# for the terminator and fail clearly as the suite grows, never truncate it.
MAX_PATTERN_BYTES = 120_000


def fingerprint(names):
    return hashlib.sha256('\n'.join(sorted(names)).encode()).hexdigest()


def partition(inventory, count):
    if not 1 <= count <= 32:
        raise ValueError('require 1 <= shard count <= 32')
    names = []
    for line in inventory.splitlines():
        if re.fullmatch(r'(Test|Example|Fuzz)\w*', line):
            names.append(line)
    if len(names) != len(set(names)):
        raise ValueError('duplicate top-level test in Go inventory')
    names.sort()
    shards = [names[i::count] for i in range(count)]
    if not names or any(not shard for shard in shards):
        raise ValueError('empty API test inventory or partition')
    return names, shards


def selection_pattern(names):
    # Names originate from Go identifiers, which contain no regexp operators.
    pattern = '^(' + '|'.join(names) + ')$'
    if len(pattern.encode()) >= MAX_PATTERN_BYTES:
        raise ValueError('API shard regexp exceeds safe argv size; increase shard count')
    return pattern


def completed_seconds(directory, selected):
    events = [json.loads(line) for line in (directory / 'go-test.jsonl').read_text().splitlines()]
    finished = [e['Test'] for e in events if e.get('Package') == PACKAGE
                and e.get('Action') in ('pass', 'skip') and e.get('Test')
                and '/' not in e['Test']]
    if sorted(finished) != sorted(selected):
        raise ValueError('executed top-level tests do not match selected inventory')
    results = [e for e in events if e.get('Package') == PACKAGE and not e.get('Test')
               and e.get('Action') in ('pass', 'fail', 'skip')]
    if len(results) != 1 or results[0]['Action'] != 'pass':
        raise ValueError('missing successful API package result')
    seconds = results[0].get('Elapsed')
    if not isinstance(seconds, (int, float)) or not math.isfinite(seconds) or seconds <= 0:
        raise ValueError('invalid API package duration')
    return seconds


def run(index, count, timeout):
    if not 0 <= index < count or timeout <= 0:
        raise ValueError('invalid shard index or timeout')
    directory = Path(os.environ.get('CI_RESULTS_DIR', ROOT / '.ci-results')).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    manifest_path = directory / 'api-race-shard.json'
    # Invalidate success before enumeration too, so its failure cannot leave
    # a successful manifest from an earlier attempt in retained artifacts.
    manifest_path.unlink(missing_ok=True)
    # Match build tags/instrumentation to execution, including tests guarded
    # by //go:build race. A failed enumeration must abort the run.
    inventory = subprocess.check_output(['go', 'test', PACKAGE_DIR, '-race', '-list', '.'],
                                        cwd=ROOT, text=True)
    names, shards = partition(inventory, count)
    selected = shards[index]
    pattern = selection_pattern(selected)
    manifest = {'index': index, 'count': count, 'inventory_count': len(names),
                'inventory_sha256': fingerprint(names), 'selected': selected,
                'package': PACKAGE, 'source_sha': os.environ.get('GITHUB_SHA', '')}
    print(f'{PACKAGE_DIR} race shard {index}/{count}: {len(selected)} of {len(names)} parents', flush=True)
    result = subprocess.run(['bash', str(ROOT / 'scripts/ci/go-test.sh'), PACKAGE,
                             '-race', '-count=1', '-timeout', f'{timeout}s', '-run', pattern],
                            cwd=ROOT, env=dict(os.environ, CI_RESULTS_DIR=str(directory)))
    if result.returncode:
        return result.returncode
    manifest['seconds'] = completed_seconds(directory, selected)
    manifest_path.write_text(json.dumps(manifest, indent=2) + '\n')
    return 0


def validate_manifests(manifests, count, expected_sha=None):
    if len(manifests) != count or sorted(m.get('index', -1) for m in manifests) != list(range(count)):
        raise ValueError('missing or duplicate API shard evidence')
    reference = manifests[0]
    combined = []
    seconds = 0
    for manifest in manifests:
        # Identity: one package and one source revision. Download patterns
        # already pin both; this keeps a mixed evidence set from validating.
        if manifest.get('package') != PACKAGE or manifest.get('source_sha') != reference.get('source_sha'):
            raise ValueError('race shard evidence comes from another package or revision')
        if expected_sha and manifest.get('source_sha') != expected_sha:
            raise ValueError('race shard evidence does not match this revision')
        if (manifest.get('count') != count or
                manifest.get('inventory_count') != reference.get('inventory_count') or
                manifest.get('inventory_sha256') != reference.get('inventory_sha256')):
            raise ValueError('API shards enumerated different inventories')
        selected = manifest.get('selected', [])
        if not selected or any(not isinstance(name, str) for name in selected):
            raise ValueError('missing selected API tests')
        combined.extend(selected)
        elapsed = manifest.get('seconds')
        if not isinstance(elapsed, (int, float)) or not math.isfinite(elapsed) or elapsed <= 0:
            raise ValueError('invalid API shard duration')
        seconds += elapsed
    if len(combined) != len(set(combined)):
        raise ValueError('API partitions overlap')
    if (len(combined) != reference.get('inventory_count') or
            fingerprint(combined) != reference.get('inventory_sha256')):
        raise ValueError('API partitions do not cover the full inventory')
    return seconds, len(combined)


def report(directory, count, baseline):
    if baseline <= 0 or count <= 0:
        raise ValueError('invalid API report budget or shard count')
    manifests = [json.loads(path.read_text()) for path in Path(directory).rglob('api-race-shard.json')]
    seconds, total = validate_manifests(manifests, count, os.environ.get('GITHUB_SHA'))
    alarm = baseline * 1.6
    summary = (f'### Go Race ({PACKAGE_DIR[2:]}) — combined budget\n\n'
               f'{total} top-level tests covered exactly once across {count} shards.\n\n'
               f'Summed package seconds: {seconds:.3f}; baseline (same summed metric): {baseline}s; '
               f'erosion alarm: {alarm:.0f}s ({seconds / baseline:.2f}x baseline).\n'
               'Sum includes each shard’s package setup/cleanup; individual timings remain attached.\n')
    print(summary)
    with open(os.environ.get('GITHUB_STEP_SUMMARY', os.devnull), 'a') as output:
        output.write(summary)
    if seconds > alarm:
        main_push = os.environ.get('GITHUB_EVENT_NAME') == 'push'
        print(f'::{"error" if main_push else "warning"}::API race combined duration exceeds '
              'the erosion alarm; re-measure from green main runs before changing the budget.')
        return int(main_push)
    return 0


if __name__ == '__main__':
    try:
        if len(sys.argv) == 5 and sys.argv[1] == 'run':
            code = run(*map(int, sys.argv[2:]))
        elif len(sys.argv) == 5 and sys.argv[1] == 'report':
            code = report(sys.argv[2], int(sys.argv[3]), int(sys.argv[4]))
        else:
            raise ValueError('usage: api-race-shard.py run INDEX COUNT TIMEOUT_SECONDS | report DIRECTORY COUNT BASELINE_SECONDS')
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f'::error::{error}', file=sys.stderr)
        code = 1
    sys.exit(code)
