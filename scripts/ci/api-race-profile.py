#!/usr/bin/env python3
"""Manual public-test diagnostics; never a required CI gate or budget sample."""
import argparse
from collections import Counter
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('api_race_shard', ROOT / 'scripts/ci/api-race-shard.py')
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)
CGROUP_FILES = ('cpu.max', 'cpu.stat', 'memory.events', 'io.stat',
                'cpu.pressure', 'memory.pressure', 'io.pressure')


def read_bounded(path, maximum=65536):
    try:
        with path.open('rb') as stream:
            value = stream.read(maximum + 1)
        return value.decode() if len(value) <= maximum else 'UNAVAILABLE: size bound'
    except (OSError, UnicodeError):
        return 'UNAVAILABLE'


def telemetry():
    """Only this process's cgroup and ancestors, never process/env dumps."""
    group = next((line[3:] for line in read_bounded(Path('/proc/self/cgroup')).splitlines()
                  if line.startswith('0::')), None)
    entries = []
    if group is not None:
        root = Path('/sys/fs/cgroup').resolve()
        current = (root / group.lstrip('/')).resolve()
        if current.is_relative_to(root):
            for _ in range(64):
                entries.append({'relative_path': str(current.relative_to(root)),
                                'files': {name: read_bounded(current / name) for name in CGROUP_FILES}})
                if current == root:
                    break
                current = current.parent
    return {'at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'cgroups': entries}


def guarded_identity():
    if (os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch'
            or os.environ.get('GITHUB_REF') != 'refs/heads/main'
            or os.environ.get('GITHUB_REF_PROTECTED') != 'true'):
        raise ValueError('diagnostics require dispatch on protected main')
    identity = {key: os.environ.get(key, '') for key in ('GITHUB_SHA', 'GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT')}
    runner.bootstrap_identity({'source_sha': identity['GITHUB_SHA'], 'run_id': identity['GITHUB_RUN_ID'],
                               'run_attempt': identity['GITHUB_RUN_ATTEMPT']})
    if subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip() != identity['GITHUB_SHA']:
        raise ValueError('diagnostic checkout differs from dispatch SHA')
    if runner.PACKAGE != runner.MODULE + '/internal/api' or runner.RACE_GCFLAGS != '-gcflags=modernc.org/...=-d=checkptr=0':
        raise ValueError('diagnostic package or race flags changed')
    return identity


def hash_file(path, maximum=512 << 20):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size == 0 or info.st_size > maximum:
        raise ValueError('invalid diagnostic artifact')
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


def profile(index, bootstrap, output, allocations=False):
    if type(index) is not int or not 0 <= index <= 5 or type(allocations) is not bool:
        raise ValueError('invalid diagnostic selection')
    identity = guarded_identity()
    runner.validate_bootstrap(bootstrap)
    output = Path(output).absolute()
    output.mkdir(mode=0o700)  # Exclusive: never overwrite an older profile.
    receipt = {'identity': identity, 'shard_index': index, 'shard_count': 6, 'allocations': allocations,
               'diagnostic_only': True, 'budget_baseline': False, 'status': 'incomplete',
               'before': telemetry(), 'hashes': {},
               'race_gcflags': runner.RACE_GCFLAGS, 'count': 1, 'test_timeout_seconds': 4600,
               'profile_overhead': 'elapsed is diagnostic only; not a baseline or aggregate gate sample'}
    code = 1
    try:
        receipt['go_version'] = subprocess.check_output(['go', 'version'], cwd=ROOT, text=True).strip()
        receipt['platform'] = subprocess.check_output(['go', 'env', 'GOOS', 'GOARCH'], cwd=ROOT, text=True).splitlines()
        receipt['runner_image'] = {key: os.environ.get(key, 'UNKNOWN') for key in ('ImageOS', 'ImageVersion', 'RUNNER_OS', 'RUNNER_ARCH')}
        receipt['gomaxprocs_env'] = os.environ.get('GOMAXPROCS', 'UNSET: effective test runtime value unknown')
        receipt['cache_restore_hit'] = 'UNKNOWN: existing setup-go-embed does not expose cache-hit output'
        receipt['cache_scope'] = 'existing pinned setup-go module/build cache; no cache flush or cold-page-cache claim'
        receipt['source_hashes'] = {name: hash_file(ROOT / name) for name in ('go.mod', 'go.sum', 'scripts/ci/api-race-shard.py', 'scripts/ci/go-test.sh')}
        code = runner.run(index, 6, 4600, bootstrap, profile_directory=output, allocations=allocations)
        receipt['native_exit'] = code
        if code:
            return code
        manifest_path = Path(os.environ.get('CI_RESULTS_DIR', ROOT / '.ci-results')) / 'api-race-shard.json'
        receipt['selection_manifest_sha256'] = hash_file(manifest_path)
        manifest = json.loads(manifest_path.read_text())
        if not manifest.get('diagnostic_profile') or manifest['source_sha'] != identity['GITHUB_SHA']:
            raise ValueError('diagnostic selection receipt identity mismatch')
        receipt['selected_count'] = len(manifest['selected'])
        receipt['inventory_count'] = manifest['inventory_count']
        receipt['inventory_sha256'] = manifest['inventory_sha256']
        receipt['bootstrap_sha256'] = manifest['bootstrap_sha256']
        events_path = manifest_path.with_name('go-test.jsonl')
        outcomes = Counter()
        child_skips = 0
        with events_path.open() as stream:
            for line in stream:
                event = json.loads(line)
                if event.get('Package') != runner.PACKAGE or not event.get('Test'):
                    continue
                action = event.get('Action')
                if action in ('pass', 'skip', 'fail') and '/' not in event['Test']:
                    outcomes[action] += 1
                elif action == 'skip':
                    child_skips += 1
        receipt['parent_outcomes'] = dict(outcomes)
        receipt['child_skip_events'] = child_skips
        if not outcomes['pass'] or outcomes['fail']:
            raise ValueError('diagnostic partition executed no passing parents or has failures')
        binary = output / 'api.test'
        receipt['binary_sha256'] = hash_file(binary)
        for name, flags in [('cpu', []), *([('alloc', ['-alloc_space'])] if allocations else [])]:
            path = output / (name + '.pprof')
            receipt['hashes'][path.name] = hash_file(path)
            for kind, options in [('top', []), ('cumulative', ['-cum'])]:
                result = subprocess.run(['go', 'tool', 'pprof', '-top', *options, *flags, str(binary), str(path)],
                                        cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                        timeout=15, check=True)
                if len(result.stdout.encode()) > 1 << 20:
                    raise ValueError('pprof summary exceeds bound')
                (output / f'{name}-{kind}.txt').write_text(result.stdout)
        receipt['status'] = 'profile_complete_not_budget_evidence'
        return 0
    finally:
        receipt['after'] = telemetry()
        (output / 'profile-receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
        # The public artifact contains a binary hash, never the executable.
        binary = output / 'api.test'
        if binary.is_file() and not binary.is_symlink():
            binary.unlink()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--shard', required=True, choices=[str(i) for i in range(6)])
    parser.add_argument('--allocations', required=True, choices=['true', 'false'])
    parser.add_argument('--bootstrap', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    try:
        sys.exit(profile(int(args.shard), args.bootstrap, args.output, args.allocations == 'true'))
    except (ValueError, OSError, subprocess.SubprocessError):
        # No exception body, environment, command argv or fixture content.
        print('::error::API diagnostic failed; inspect bounded receipt and native CI result', file=sys.stderr)
        sys.exit(1)
