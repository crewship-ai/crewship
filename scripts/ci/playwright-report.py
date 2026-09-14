#!/usr/bin/env python3
"""Validate nightly report completeness before reporting failures or recovery."""
import argparse
from collections import Counter, defaultdict
import json
from pathlib import Path


def summarize(report, expected_files):
    if not isinstance(report, dict) or not isinstance(report.get('suites'), list):
        raise ValueError('missing Playwright suites')
    if report.get('errors'):
        raise ValueError('Playwright reported a global error; test results are incomplete')
    counts = defaultdict(Counter)
    outcomes = {}

    def visit(suite, parents=()):
        titles = parents + (suite.get("title", ""),)
        for spec in suite.get('specs', []):
            file = Path(spec.get('file', suite.get('file', ''))).name
            if not file:
                raise ValueError('test has no source file')
            for test in spec.get('tests', []):
                status = test.get('status')
                if status not in ('expected', 'unexpected', 'flaky', 'skipped'):
                    raise ValueError(f'unknown test status: {status!r}')
                results = test.get('results', [])
                if status == 'skipped':
                    category = 'skipped' if test.get('expectedStatus') == 'skipped' else 'not_run'
                elif status == 'expected' and test.get('expectedStatus') != 'passed':
                    category = 'failed'
                elif not results:
                    category = 'not_run'
                else:
                    category = {'expected': 'passed', 'unexpected': 'failed', 'flaky': 'flaky'}[status]
                key = json.dumps([file, *titles, spec.get('title', ''), test.get('projectName', '')], ensure_ascii=False)
                if key in outcomes:
                    raise ValueError(f'duplicate test identity: {key}')
                outcomes[key] = category
                counts[file][category] += 1
        for child in suite.get('suites', []):
            visit(child, titles)

    for suite in report['suites']:
        visit(suite)
    expected = {Path(file).name for file in expected_files}
    if not expected or set(counts) != expected:
        raise ValueError(f'report file inventory mismatch: missing={sorted(expected - set(counts))}, extra={sorted(set(counts) - expected)}')
    totals = Counter()
    for count in counts.values():
        totals.update(count)
    if not totals['passed'] and not totals['failed'] and not totals['flaky']:
        raise ValueError('no tests executed; skipped tests cannot establish recovery')
    keys = ('passed', 'failed', 'flaky', 'skipped', 'not_run')
    return {
        'outcomes': outcomes,
        'totals': {key: totals[key] for key in keys},
        'files': {file: {key: count[key] for key in keys} for file, count in sorted(counts.items())},
        'clean': not (totals['failed'] or totals['flaky'] or totals['not_run']),
    }


def regressions(summary, baseline):
    allowed = baseline.get('outcomes')
    if not isinstance(allowed, dict) or not allowed:
        raise ValueError('baseline must contain measured test outcomes')
    problems = []
    for identity, outcome in summary['outcomes'].items():
        previous = allowed.get(identity)
        if outcome != 'passed' and outcome != previous:
            problems.append({'test': identity, 'before': previous, 'after': outcome})
    # Disappearing tests are not fixes. Remove them from the baseline only
    # through a reviewed change (for example, promotion into the gate bucket).
    for identity in allowed.keys() - summary['outcomes'].keys():
        problems.append({'test': identity, 'before': allowed[identity], 'after': 'missing'})
    return problems


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', type=Path)
    parser.add_argument('report', type=Path)
    parser.add_argument('specs', nargs='+')
    args = parser.parse_args()
    try:
        summary = summarize(json.loads(args.report.read_text()), args.specs)
        if args.baseline:
            summary['regressions'] = regressions(summary, json.loads(args.baseline.read_text()))
        print(json.dumps(summary))
    except (ValueError, OSError, TypeError, AttributeError) as error:
        parser.exit(1, f'Invalid nightly report: {error}\n')


if __name__ == '__main__':
    main()
