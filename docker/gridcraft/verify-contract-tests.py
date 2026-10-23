#!/usr/bin/env python3
"""Require execution of every pinned-engine contract test, not a green skip."""
import json
import sys

PACKAGE = 'github.com/crewship-ai/crewship/internal/appengine'
EXPECTED = frozenset({
    'TestRealGridCraftCellsSetOnFormulaSheet',
    'TestRealGridCraftReplayFromNearestCheckpointMatchesResultHashes',
    'TestRealGridCraftConcurrentCandidatesOnDifferentWorkbooks',
    'TestRecalcCheckCannotBeRewrittenBySurvivingProcess',
    'TestRealGridCraftImportEditRestartAndRetry',
    'TestRealGridCraftDependentFormulaRecalculation',
    'TestRealGridCraftRecalcVerificationDetectsTamperedValues',
    'TestRealGridCraftLargeSheetWithinCompactLimit',
    'TestRealGridCraftReportBindsReturnedBytes',
    'TestRealGridCraftIterativeCycleRejectsDerivedChanges',
})


def verify(lines):
    passed = set()
    package_passed = False
    for line in lines:
        if len(line) > 1024 * 1024:
            raise ValueError('oversized Go test event')
        event = json.loads(line)
        if not isinstance(event, dict) or event.get('Package') != PACKAGE:
            raise ValueError('unexpected Go test event/package')
        action = event.get('Action')
        if action in ('skip', 'fail'):
            raise ValueError('contract test skipped or failed: ' + str(event.get('Test', PACKAGE)))
        if action != 'pass':
            continue
        name = event.get('Test')
        if name is None:
            if package_passed:
                raise ValueError('duplicate package verdict')
            package_passed = True
        elif isinstance(name, str) and '/' in name:
            if name.split('/', 1)[0] not in EXPECTED:
                raise ValueError('unexpected subtest')
        elif name not in EXPECTED or name in passed:
            raise ValueError('unexpected or duplicate test verdict')
        else:
            passed.add(name)
    if not package_passed or passed != EXPECTED:
        raise ValueError('missing contract execution: ' + ', '.join(sorted(EXPECTED - passed)))
    return len(passed)


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('usage: verify-contract-tests.py GO_TEST_JSON')
    try:
        with open(sys.argv[1], encoding='utf-8') as source:
            count = verify(source)
        print(f'Pinned engine contracts: {count}/{len(EXPECTED)} executed and passed; no skips')
    except (OSError, ValueError, TypeError) as exc:
        raise SystemExit(str(exc)) from exc
