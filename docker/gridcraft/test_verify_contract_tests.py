"""Regression tests for evidence that the real pinned engine tests executed."""
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('contracts', Path(__file__).with_name('verify-contract-tests.py'))
contracts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(contracts)


def event(action, name=None):
    value = {'Action': action, 'Package': contracts.PACKAGE}
    if name is not None:
        value['Test'] = name
    return json.dumps(value)


def passing():
    return [event('pass', name) for name in sorted(contracts.EXPECTED)] + [event('pass')]


class ContractEvidenceTests(unittest.TestCase):
    def test_complete_execution(self):
        self.assertEqual(contracts.verify(passing()), 10)

    def test_missing_test_or_package_verdict_is_not_execution(self):
        for lines in ([], passing()[1:], passing()[:-1]):
            with self.assertRaises(ValueError):
                contracts.verify(lines)

    def test_skip_or_failure_cannot_hide_behind_later_pass(self):
        for action in ('skip', 'fail'):
            with self.assertRaises(ValueError):
                contracts.verify([event(action, sorted(contracts.EXPECTED)[0])] + passing())

    def test_duplicate_and_unknown_tests_are_rejected(self):
        for extra in (event('pass', sorted(contracts.EXPECTED)[0]), event('pass', 'TestUnexpected'), event('pass')):
            with self.assertRaises(ValueError):
                contracts.verify(passing() + [extra])

    def test_invalid_evidence_is_rejected(self):
        for invalid in ('not json', '[]', '{}', 'x' * (1024 * 1024 + 1)):
            with self.assertRaises(ValueError):
                contracts.verify([invalid] + passing())

    def test_subtest_skip_is_rejected(self):
        with self.assertRaises(ValueError):
            contracts.verify([event('skip', sorted(contracts.EXPECTED)[0] + '/case')] + passing())
