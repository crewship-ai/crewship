import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('report', Path(__file__).with_name('playwright-report.py'))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


def fixture(*tests):
    return {'suites': [{'file': 'a.spec.ts', 'suites': [{'specs': [{'file': 'a.spec.ts', 'tests': [dict(t, projectName=str(i)) for i, t in enumerate(tests)]}]}]}], 'errors': []}


def test(status='expected', expected='passed', results=True):
    return dict(status=status, expectedStatus=expected, results=[{}] if results else [])


class PlaywrightReportTests(unittest.TestCase):
    def summarize(self, value):
        return report.summarize(value, ['e2e/a.spec.ts'])

    def test_counts_expected_skips_separately_from_serial_tests_not_run(self):
        summary = self.summarize(fixture(test(), test('unexpected'), test('flaky'), test('skipped', 'skipped'), test('skipped')))
        self.assertEqual(summary['totals'], dict(passed=1, failed=1, flaky=1, skipped=1, not_run=1))
        self.assertFalse(summary['clean'])

    def test_incomplete_and_global_error_reports_cannot_claim_recovery(self):
        for value in [{}, {'suites': []}, fixture(test('skipped', 'skipped')), dict(fixture(test()), errors=[{'message': 'setup failed'}])]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.summarize(value)
        with self.assertRaises(ValueError):
            report.summarize(fixture(test()), ['a.spec.ts', 'b.spec.ts'])

    def test_unexecuted_or_unknown_outcomes_are_not_green(self):
        self.assertFalse(self.summarize(fixture(test(), test(results=False)))['clean'])
        with self.assertRaises(ValueError):
            self.summarize(fixture(test('new-status')))

    def test_clean_requires_execution_and_accepts_deliberate_skips(self):
        self.assertTrue(self.summarize(fixture(test(), test('skipped', 'skipped')))['clean'])

    def test_ratchet_rejects_new_failures_skips_and_missing_tests(self):
        original = self.summarize(fixture(test(), test('unexpected')))
        self.assertEqual(report.regressions(original, original), [])
        self.assertEqual(report.regressions(self.summarize(fixture(test(), test())), original), [])
        for current in [fixture(test('unexpected'), test('unexpected')),
                        fixture(test('skipped', 'skipped'), test('unexpected')),
                        fixture(test())]:
            self.assertTrue(report.regressions(self.summarize(current), original))

    def test_expected_failure_is_not_a_passing_test(self):
        summary = self.summarize(fixture(test(), test('expected', 'failed')))
        self.assertEqual(summary['totals']['passed'], 1)
        self.assertEqual(summary['totals']['failed'], 1)
        self.assertFalse(summary['clean'])

    def test_duplicate_identity_is_invalid(self):
        value = fixture(test())
        value['suites'].append(value['suites'][0])
        with self.assertRaises(ValueError):
            self.summarize(value)


if __name__ == '__main__':
    unittest.main()
