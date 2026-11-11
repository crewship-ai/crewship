#!/usr/bin/env python3
"""Run exhaustive API race partitions; verify their combined coverage and budget.

run INDEX COUNT TIMEOUT_SECONDS enumerates the race-built package, retains each
parent with all subtests, and writes evidence to CI_RESULTS_DIR (.ci-results).
report DIRECTORY COUNT BASELINE_SECONDS validates downloaded shard artifacts.
Uses only local Go/git tooling; never contacts a running Crewship instance.
"""
from bisect import bisect_left
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
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
RACE_GCFLAGS = os.environ.get('RACE_GCFLAGS', '-gcflags=modernc.org/...=-d=checkptr=0')
# Linux limits EACH argv string, independently of total ARG_MAX. Leave room
# for the terminator and fail clearly as the suite grows, never truncate it.
MAX_PATTERN_BYTES = 120_000
BOOTSTRAP_PACKAGE = MODULE + '/internal/testutil'
BOOTSTRAP_TEST = 'TestCIRaceTemplateBootstrap'


def bundle_file(directory, name, maximum):
    path = Path(directory) / name
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > maximum:
        raise ValueError(f'invalid bootstrap file: {name}')
    return path.read_bytes()


def bootstrap_seconds(raw):
    events = [json.loads(line) for line in raw.decode().splitlines()]
    if any(e.get('Action') in ('fail', 'skip') for e in events):
        raise ValueError('bootstrap test execution failed or skipped work')
    parents = [e for e in events if e.get('Test') and '/' not in e['Test']
               and e.get('Action') in ('pass', 'skip', 'fail')]
    if (len(parents) != 1 or parents[0].get('Package') != BOOTSTRAP_PACKAGE
            or parents[0]['Test'] != BOOTSTRAP_TEST or parents[0]['Action'] != 'pass'):
        raise ValueError('bootstrap parent did not execute successfully exactly once')
    results = [e for e in events if not e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')]
    if len(results) != 1 or results[0].get('Package') != BOOTSTRAP_PACKAGE or results[0]['Action'] != 'pass':
        raise ValueError('missing successful bootstrap package result')
    seconds = results[0].get('Elapsed')
    if type(seconds) not in (int, float) or not math.isfinite(seconds) or seconds <= 0:
        raise ValueError('invalid bootstrap package elapsed time')
    return seconds


def bootstrap_identity(value):
    sha, run, attempt = (value.get(key, '') for key in ('source_sha', 'run_id', 'run_attempt'))
    current_attempt = os.environ.get('GITHUB_RUN_ATTEMPT', '')
    if (not re.fullmatch(r'[a-f0-9]{40}', sha) or sha != os.environ.get('GITHUB_SHA')
            or not re.fullmatch(r'[1-9][0-9]*', run) or run != os.environ.get('GITHUB_RUN_ID')
            or not re.fullmatch(r'[1-9][0-9]*', attempt)
            or not re.fullmatch(r'[1-9][0-9]*', current_attempt) or int(attempt) > int(current_attempt)):
        raise ValueError('bootstrap identity differs from this workflow revision/run')


def validate_bootstrap(directory):
    if PACKAGE != MODULE + '/internal/api':
        raise ValueError('shared bootstrap is only supported for the API race package')
    directory = Path(directory)
    if not stat.S_ISDIR(directory.lstat().st_mode):
        raise ValueError('bootstrap must be a real directory')
    raw_receipt = bundle_file(directory, 'bootstrap-receipt.json', 64 << 10)
    receipt = json.loads(raw_receipt)
    bootstrap_identity(receipt)
    if (receipt.get('version') != 1 or receipt.get('package') != BOOTSTRAP_PACKAGE
            or receipt.get('test') != BOOTSTRAP_TEST or receipt.get('race_gcflags') != RACE_GCFLAGS):
        raise ValueError('bootstrap receipt format or race runner differs')
    raw_metadata = bundle_file(directory, 'template-metadata.json', 64 << 10)
    raw_events = bundle_file(directory, 'go-test.jsonl', 16 << 20)
    for field, raw in [('metadata_sha256', raw_metadata), ('events_sha256', raw_events)]:
        if receipt.get(field) != hashlib.sha256(raw).hexdigest():
            raise ValueError('bootstrap evidence digest differs')
    metadata = json.loads(raw_metadata)
    bootstrap_identity(metadata)
    if any(metadata.get(key) != receipt.get(key) for key in ('source_sha', 'run_id', 'run_attempt')):
        raise ValueError('bootstrap receipt and template identities differ')
    if metadata.get('version') != 1 or metadata.get('race') is not True:
        raise ValueError('bootstrap template was not race-built')
    database = bundle_file(directory, 'template.db', 256 << 20)
    if (not database or type(metadata.get('template_size')) is not int
            or metadata['template_size'] != len(database)
            or metadata.get('template_sha256') != hashlib.sha256(database).hexdigest()):
        raise ValueError('bootstrap database digest or size differs')
    seconds = bootstrap_seconds(raw_events)
    if type(receipt.get('seconds')) not in (int, float) or receipt['seconds'] != seconds:
        raise ValueError('bootstrap duration differs from actual package execution')
    return seconds, hashlib.sha256(raw_receipt).hexdigest()


def bootstrap(directory, timeout):
    if PACKAGE != MODULE + '/internal/api' or timeout <= 0:
        raise ValueError('invalid API bootstrap request')
    # The bootstrap always migrates a fresh file under race instrumentation.
    # Compilation/listing is treated exactly like the existing shard runner;
    # actual migration, export and package cleanup remain inside package elapsed.
    actual_sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    if actual_sha != os.environ.get('GITHUB_SHA'):
        raise ValueError('bootstrap checkout does not match workflow source revision')
    directory = Path(directory).absolute()
    directory.mkdir(mode=0o700, parents=True)  # Refuse stale/reused success evidence.
    env = dict(os.environ, CREWSHIP_TESTUTIL_EXPORT_BUNDLE=str(directory), CI_RESULTS_DIR=str(directory))
    env.pop('CREWSHIP_TESTUTIL_IMPORT_BUNDLE', None)
    result = subprocess.run(['bash', str(ROOT / 'scripts/ci/go-test.sh'), BOOTSTRAP_PACKAGE,
                             '-race', '-count=1', '-timeout', f'{timeout}s', '-run', '^' + BOOTSTRAP_TEST + '$'],
                            cwd=ROOT, env=env)
    if result.returncode:
        return result.returncode
    raw_events = bundle_file(directory, 'go-test.jsonl', 16 << 20)
    raw_metadata = bundle_file(directory, 'template-metadata.json', 64 << 10)
    metadata = json.loads(raw_metadata)
    receipt = {'version': 1, 'package': BOOTSTRAP_PACKAGE, 'test': BOOTSTRAP_TEST,
               **{key: metadata[key] for key in ('source_sha', 'run_id', 'run_attempt')},
               'seconds': bootstrap_seconds(raw_events), 'race_gcflags': RACE_GCFLAGS,
               'metadata_sha256': hashlib.sha256(raw_metadata).hexdigest(),
               'events_sha256': hashlib.sha256(raw_events).hexdigest()}
    receipt_path = directory / 'bootstrap-receipt.json'
    receipt_path.write_text(json.dumps(receipt, indent=2) + '\n')
    try:
        seconds, _ = validate_bootstrap(directory)
    except (ValueError, KeyError, OSError):
        receipt_path.unlink(missing_ok=True)
        raise
    print(f'Actual race template bootstrap package: {seconds:.3f}s (included in aggregate)', flush=True)
    return 0


def fingerprint(names):
    return hashlib.sha256('\n'.join(sorted(names)).encode()).hexdigest()


def partition(inventory, count, *, adjacent=False):
    if not 1 <= count <= 32:
        raise ValueError('require 1 <= shard count <= 32')
    names = []
    for line in inventory.splitlines():
        if re.fullmatch(r'(Test|Example|Fuzz)\w*', line):
            names.append(line)
    if len(names) != len(set(names)):
        raise ValueError('duplicate top-level test in Go inventory')
    names.sort()
    if adjacent:
        if PACKAGE != MODULE + '/internal/api':
            raise ValueError('adjacent partitions require the API package')
        # Adjacent names permit small inventory-checked prefix expressions.
        # Keep exactly the same number of processes and every parent/subtest.
        shards = [names[i * len(names) // count:(i + 1) * len(names) // count]
                  for i in range(count)]
    else:
        shards = [names[i::count] for i in range(count)]
    # These API parents build the same real CLI. Keep their build/cache reuse
    # within one measured shard even when unrelated tests change sorted ranks.
    # Every other parent keeps its original assignment in the selected mode.
    anchor = 'TestDocumentCreationSubmissionReplayThroughWebSocketAndBinary'
    peer = 'TestPrivateArtifactConversationAudienceThroughBinary'
    if PACKAGE == MODULE + '/internal/api' and anchor in names and peer in names:
        anchor_index = next(i for i, part in enumerate(shards) if anchor in part)
        peer_index = next(i for i, part in enumerate(shards) if peer in part)
        if anchor_index != peer_index:
            shards[peer_index].remove(peer)
            shards[anchor_index].append(peer)
            shards[anchor_index].sort()
    if not names or any(not shard for shard in shards):
        raise ValueError('empty API test inventory or partition')
    return names, shards


def selection_pattern(names, inventory=None):
    # Other package runners retain exact literal selection. The API runner can
    # compact only against its complete immutable Go-enumerated inventory.
    terms = list(names)
    if inventory is not None:
        chosen = set(names)
        complete = set(inventory)
        if not chosen or len(chosen) != len(names) or len(complete) != len(inventory) or not chosen <= complete:
            raise ValueError('invalid selection or complete inventory')
        excluded = sorted(complete - chosen)
        prefixes, exact = set(), set()
        for name in sorted(chosen):
            position = bisect_left(excluded, name)
            shared = 0
            # In sorted order, the nearest excluded neighbors determine the
            # longest prefix this name shares with ANY excluded parent.
            for neighbor in excluded[max(0, position - 1):position + 1]:
                common = 0
                for left, right in zip(name, neighbor):
                    if left != right:
                        break
                    common += 1
                shared = max(shared, common)
            if shared == len(name):
                exact.add(name)
            else:
                prefixes.add(name[:shared + 1])
        terms = [re.escape(name) for name in sorted(exact)]
        terms += [re.escape(prefix) + '.*' for prefix in sorted(prefixes)]
    pattern = '^(' + '|'.join(terms) + ')$'
    if len(pattern.encode()) >= MAX_PATTERN_BYTES:
        raise ValueError('API shard regexp exceeds safe argv size; increase shard count')
    if inventory is not None:
        # Never broaden the expected set to fit an expression. Actual Go
        # execution is independently checked against these same selected roots.
        if {name for name in inventory if re.fullmatch(pattern, name)} != set(names):
            raise ValueError('compact expression differs from selected inventory')
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


def run(index, count, timeout, bootstrap_directory=None, *, profile_directory=None, allocations=False):
    if not 0 <= index < count or timeout <= 0:
        raise ValueError('invalid shard index or timeout')
    directory = Path(os.environ.get('CI_RESULTS_DIR', ROOT / '.ci-results')).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    manifest_path = directory / 'api-race-shard.json'
    # Invalidate success before enumeration too, so its failure cannot leave
    # a successful manifest from an earlier attempt in retained artifacts.
    manifest_path.unlink(missing_ok=True)
    env = dict(os.environ, CI_RESULTS_DIR=str(directory))
    bootstrap_digest = None
    if bootstrap_directory is not None:
        _, bootstrap_digest = validate_bootstrap(bootstrap_directory)
        env['CREWSHIP_TESTUTIL_IMPORT_BUNDLE'] = str(Path(bootstrap_directory).absolute())
    profile_flags = []
    if profile_directory is not None:
        if PACKAGE != MODULE + '/internal/api' or count != 6 or bootstrap_directory is None:
            raise ValueError('profiles require one of six API partitions and a measured bootstrap')
        profile_directory = Path(profile_directory).absolute()
        if profile_directory.is_symlink() or not profile_directory.is_dir():
            raise ValueError('profile output must be a real directory')
        profile_flags = [f'-cpuprofile={profile_directory / "cpu.pprof"}',
                         f'-outputdir={profile_directory}', f'-o={profile_directory / "api.test"}']
        if allocations:
            profile_flags += [f'-memprofile={profile_directory / "alloc.pprof"}', '-memprofilerate=524288']
    elif allocations:
        raise ValueError('allocations require diagnostic profiling')
    # Match build tags/instrumentation to execution, including tests guarded
    # by //go:build race. A failed enumeration must abort the run.
    # Same flags as scripts/ci/go-test.sh adds under -race, so the listing
    # compile is reused by the run instead of rebuilding SQLite.
    inventory = subprocess.check_output(['go', 'test', PACKAGE_DIR, '-race', RACE_GCFLAGS, '-list', '.'],
                                        cwd=ROOT, env=env, text=True)
    names, shards = partition(inventory, count, adjacent=PACKAGE == MODULE + '/internal/api')
    selected = shards[index]
    pattern = selection_pattern(selected, names if PACKAGE == MODULE + '/internal/api' else None)
    manifest = {'index': index, 'count': count, 'inventory_count': len(names),
                'inventory_sha256': fingerprint(names), 'selected': selected,
                'package': PACKAGE, 'source_sha': os.environ.get('GITHUB_SHA', ''),
                'selection_pattern_sha256': hashlib.sha256(pattern.encode()).hexdigest(),
                'selection_pattern_bytes': len(pattern.encode()),
                'selection_mode': 'inventory-prefix-v1' if PACKAGE == MODULE + '/internal/api' else 'exact-names-v1'}
    if bootstrap_digest is not None:
        manifest['bootstrap_sha256'] = bootstrap_digest
    if profile_directory is not None:
        manifest['diagnostic_profile'] = True
    print(f'{PACKAGE_DIR} race shard {index}/{count}: {len(selected)} of {len(names)} parents', flush=True)
    result = subprocess.run(['bash', str(ROOT / 'scripts/ci/go-test.sh'), PACKAGE,
                             '-race', '-count=1', '-timeout', f'{timeout}s', '-run', pattern] + profile_flags,
                            cwd=ROOT, env=env)
    if result.returncode:
        return result.returncode
    manifest['seconds'] = completed_seconds(directory, selected)
    manifest_path.write_text(json.dumps(manifest, indent=2) + '\n')
    return 0


def validate_manifests(manifests, count, expected_sha=None, bootstrap_digest=None):
    if len(manifests) != count or sorted(m.get('index', -1) for m in manifests) != list(range(count)):
        raise ValueError('missing or duplicate API shard evidence')
    reference = manifests[0]
    combined = []
    seconds = 0
    for manifest in manifests:
        if manifest.get('diagnostic_profile'):
            raise ValueError('diagnostic profiles cannot supply ordinary budget evidence')
        if manifest.get('bootstrap_sha256') != bootstrap_digest:
            raise ValueError('API shards do not reference the required measured bootstrap')
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


def combined_alarm(baseline):
    """The erosion alarm: RACE_COMBINED_ALARM_SECONDS when the workflow names one
    explicitly (a measured, owner-approved limit), else the historical 1.6x baseline."""
    explicit = os.environ.get('RACE_COMBINED_ALARM_SECONDS')
    if explicit is None:
        return baseline * 1.6
    if not explicit.isdigit() or int(explicit) < baseline:
        raise ValueError('RACE_COMBINED_ALARM_SECONDS must be whole seconds not below the measured baseline')
    return int(explicit)


def report(directory, count, baseline, bootstrap_directory=None):
    if baseline <= 0 or count <= 0:
        raise ValueError('invalid API report budget or shard count')
    alarm = combined_alarm(baseline)
    manifests = [json.loads(path.read_text()) for path in Path(directory).rglob('api-race-shard.json')]
    bootstrap_elapsed, bootstrap_digest = (0, None) if bootstrap_directory is None else validate_bootstrap(bootstrap_directory)
    seconds, total = validate_manifests(manifests, count, os.environ.get('GITHUB_SHA'), bootstrap_digest)
    seconds += bootstrap_elapsed
    summary = (f'### Go Race ({PACKAGE_DIR[2:]}) — combined budget\n\n'
               f'{total} top-level tests covered exactly once across {count} shards.\n\n'
               f'Summed package seconds: {seconds:.3f}; baseline (same summed metric): {baseline}s; '
               f'erosion alarm: {alarm:.0f}s ({seconds / baseline:.2f}x baseline).\n'
               'Sum includes each shard’s package setup/cleanup; individual timings remain attached.\n')
    if bootstrap_directory is not None:
        summary += f'Includes the genuine race template bootstrap package exactly once: {bootstrap_elapsed:.3f}s.\n'
    print(summary)
    with open(os.environ.get('GITHUB_STEP_SUMMARY', os.devnull), 'a') as output:
        output.write(summary)
    if seconds > alarm:
        print(f'::error::{PACKAGE_DIR[2:]} race combined duration exceeds '
              'the erosion alarm; re-measure from green main runs before changing the budget.')
        return 1
    return 0


if __name__ == '__main__':
    try:
        if len(sys.argv) == 4 and sys.argv[1] == 'bootstrap':
            code = bootstrap(sys.argv[2], int(sys.argv[3]))
        elif len(sys.argv) in (5, 6) and sys.argv[1] == 'run':
            code = run(*map(int, sys.argv[2:5]), *sys.argv[5:])
        elif len(sys.argv) in (5, 6) and sys.argv[1] == 'report':
            code = report(sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), *sys.argv[5:])
        else:
            raise ValueError('usage: api-race-shard.py bootstrap DIRECTORY TIMEOUT_SECONDS | run INDEX COUNT TIMEOUT_SECONDS [BOOTSTRAP_DIRECTORY] | report DIRECTORY COUNT BASELINE_SECONDS [BOOTSTRAP_DIRECTORY]')
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f'::error::{error}', file=sys.stderr)
        code = 1
    sys.exit(code)
