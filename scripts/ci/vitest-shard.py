#!/usr/bin/env python3
"""Run Vitest partitions and require complete file/coverage evidence before merge."""
import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess


def command(*args):
    subprocess.run(['pnpm', 'exec', 'vitest', *args], check=True)


def relative(path, root):
    return Path(path).resolve().relative_to(Path(root).resolve()).as_posix()


def inventory(path, root):
    items = json.loads(Path(path).read_text())
    if any(item.get('projectName') for item in items):
        raise ValueError('Named Vitest projects require project-aware partition evidence')
    files = [relative(item['file'], root) for item in items]
    if not files or len(files) != len(set(files)):
        raise ValueError('Test inventory is empty or contains duplicate files')
    return sorted(files)


def report_files(report, root):
    if report.get('success') is not True or report.get('numPassedTests', 0) <= 0:
        raise ValueError('Partition did not report a successful nonempty test run')
    if any(item.get('status') != 'passed' for item in report['testResults']):
        raise ValueError('Partition contains a failed test file')
    files = [relative(item['name'], root) for item in report['testResults']]
    if len(files) != len(set(files)):
        raise ValueError('Partition reports duplicate test files')
    coverage = report.get('coverageMap')
    if not isinstance(coverage, dict) or not coverage:
        raise ValueError('Partition has no coverage map')
    return sorted(files), sorted(relative(path, root) for path in coverage)


def test_counts(report):
    return {name: report.get(field, 0) for name, field in [
        ('passed', 'numPassedTests'), ('pending', 'numPendingTests'), ('todo', 'numTodoTests'),
    ]}


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def source_sha():
    return os.environ.get('GITHUB_SHA') or subprocess.check_output(
        ['git', 'rev-parse', 'HEAD'], text=True).strip()


def run(index, count, directory, config, max_workers=None):
    if not 1 <= index <= count:
        raise ValueError('Partition index must be between 1 and count')
    directory.mkdir(parents=True, exist_ok=True)
    root = str(Path.cwd())
    command('list', '--config', config, '--filesOnly', '--json=' + str(directory / 'inventory.json'))
    expected = inventory(directory / 'inventory.json', root)
    worker_args = [] if max_workers is None else [f'--maxWorkers={max_workers}']
    command('run', '--config', config, '--coverage',
            '--coverage.reportsDirectory=' + str(directory / 'coverage'),
            '--shard', f'{index}/{count}', *worker_args,
            '--reporter=default', '--reporter=blob', '--reporter=json',
            '--outputFile.blob=' + str(directory / 'blob.json'),
            '--outputFile.json=' + str(directory / 'report.json'))
    report = json.loads((directory / 'report.json').read_text())
    files, coverage = report_files(report, root)
    if not files or set(files) - set(expected):
        raise ValueError('Partition ran empty or undiscovered test files')
    metadata = {
        'index': index, 'count': count, 'sha': source_sha(), 'root': root,
        'inventory': expected, 'files': files, 'coverage': coverage,
        'counts': test_counts(report),
        'blob_sha256': digest(directory / 'blob.json'),
        'report_sha256': digest(directory / 'report.json'),
    }
    (directory / 'metadata.json').write_text(json.dumps(metadata, indent=2) + '\n')


def validate(directory, count, expected, sha, merge_root=None):
    metadata_paths = sorted(directory.glob('*/metadata.json'))
    if len(metadata_paths) != count:
        raise ValueError(f'Expected {count} partition artifacts, found {len(metadata_paths)}')
    seen = Counter()
    indices = set()
    coverage_inventory = None
    blobs = []
    totals = Counter()
    merge_root = Path.cwd() if merge_root is None else merge_root
    for path in metadata_paths:
        meta = json.loads(path.read_text())
        if Path(meta['root']).resolve() != Path(merge_root).resolve():
            raise ValueError('Native Vitest blobs require the same checkout path in every partition and merger')
        index = meta['index']
        if index in indices or index not in range(1, count + 1):
            raise ValueError('Duplicate or invalid partition index')
        indices.add(index)
        if meta['count'] != count or meta['sha'] != sha or meta['inventory'] != expected:
            raise ValueError('Partition source, count or full inventory differs from merge checkout')
        blob = path.with_name('blob.json')
        report_path = path.with_name('report.json')
        if digest(blob) != meta['blob_sha256'] or digest(report_path) != meta['report_sha256']:
            raise ValueError('Partition report/blob identity does not match its evidence')
        report = json.loads(report_path.read_text())
        files, coverage = report_files(report, meta['root'])
        counts = test_counts(report)
        if meta['counts'] != counts:
            raise ValueError('Partition pass/skip counts differ from its report')
        totals.update(counts)
        if files != meta['files'] or coverage != meta['coverage']:
            raise ValueError('Partition evidence differs from its report')
        if coverage_inventory is not None and coverage != coverage_inventory:
            raise ValueError('Partition coverage source inventories differ')
        coverage_inventory = coverage
        seen.update(files)
        blobs.append(blob)
    if set(seen) != set(expected) or any(occurrences != 1 for occurrences in seen.values()):
        raise ValueError(f'Incomplete/duplicate test-file coverage: missing={sorted(set(expected) - set(seen))}; '
                         f'unexpected={sorted(set(seen) - set(expected))}; '
                         f'duplicate={sorted(path for path, occurrences in seen.items() if occurrences != 1)}')
    return blobs, coverage_inventory, dict(totals)


def merge(directory, count, output, config):
    output.mkdir(parents=True, exist_ok=True)
    command('list', '--config', config, '--filesOnly', '--json=' + str(output / 'inventory.json'))
    expected = inventory(output / 'inventory.json', Path.cwd())
    blobs, coverage_inventory, counts = validate(directory, count, expected, source_sha())
    blob_dir = output / 'blobs'
    if blob_dir.exists():
        shutil.rmtree(blob_dir)
    blob_dir.mkdir()
    for index, path in enumerate(blobs):
        shutil.copyfile(path, blob_dir / f'partition-{index + 1}.json')
    # No shard-only overrides here: original thresholds and report writers run
    # once over all coverage maps, including untested included source files.
    command('--config', config, '--merge-reports', str(blob_dir), '--coverage')
    if not Path('coverage/lcov.info').is_file():
        raise ValueError('Merged coverage did not emit its required LCOV artifact')
    coverage = json.loads(Path('coverage/coverage-final.json').read_text())
    if sorted(relative(path, Path.cwd()) for path in coverage) != coverage_inventory:
        raise ValueError('Merged coverage lost source files from the partition denominator')
    print(f'Validated complete discovered-file coverage: {len(expected)} files in {count} partitions; '
          f'merged {len(coverage_inventory)} coverage source files with global thresholds. '
          f'Actual test counts: passed={counts["passed"]}, pending={counts["pending"]}, todo={counts["todo"]}.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='mode', required=True)
    runner = sub.add_parser('run')
    runner.add_argument('index', type=int)
    runner.add_argument('count', type=int)
    runner.add_argument('--directory', type=Path, default=Path('.ci-results/vitest'))
    runner.add_argument('--config', default='vitest.ci-shard.config.ts')
    runner.add_argument('--max-workers', type=int, help='Optional local resource bound; CI retains default workers')
    merger = sub.add_parser('merge')
    merger.add_argument('directory', type=Path)
    merger.add_argument('count', type=int)
    merger.add_argument('--output', type=Path, default=Path('.ci-results/vitest-merge'))
    merger.add_argument('--config', default='vitest.config.ts')
    args = parser.parse_args()
    if args.mode == 'run':
        run(args.index, args.count, args.directory, args.config, args.max_workers)
    else:
        merge(args.directory, args.count, args.output, args.config)
