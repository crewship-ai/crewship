import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('managed', Path(__file__).with_name('managed-environments.py'))
managed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(managed)


class ManagedEnvironmentEvidenceTests(unittest.TestCase):
    def passing(self):
        return [{'Package': package, 'Test': name, 'Action': action}
                for package, name in managed.REQUIRED for action in ('run', 'pass')]

    def test_requires_every_live_test_not_only_green_package(self):
        self.assertEqual(managed.failures(self.passing(), 0), [])
        self.assertTrue(managed.failures([], 0))
        for missing in managed.REQUIRED:
            with self.subTest(missing=missing):
                events = [event for event in self.passing()
                          if (event['Package'], event['Test']) != missing]
                self.assertTrue(managed.failures(events, 0))

    def test_skips_failure_and_exit_failure_cannot_pass(self):
        for action in ('skip', 'fail'):
            events = self.passing()
            events[-1]['Action'] = action
            self.assertTrue(managed.failures(events, 0))
        self.assertTrue(managed.failures(self.passing(), 1))

    def test_wrong_package_or_missing_start_cannot_satisfy_evidence(self):
        events = self.passing()
        events[-1]['Package'] = 'unrelated/package'
        self.assertTrue(managed.failures(events, 0))
        self.assertTrue(managed.failures([e for e in self.passing() if e['Action'] == 'pass'], 0))

    def test_every_required_child_needs_run_and_pass_without_skip_or_fail(self):
        for child in managed.REQUIRED_CHILDREN:
            for action in ('skip', 'fail'):
                with self.subTest(child=child, action=action):
                    events = self.passing() + [{'Package': child[0], 'Test': child[1], 'Action': action}]
                    self.assertTrue(managed.failures(events, 0))
            events = [event for event in self.passing()
                      if not ((event['Package'], event['Test']) == child and event['Action'] == 'run')]
            self.assertTrue(managed.failures(events, 0))

    def test_staged_invocation_has_separate_bounded_budget(self):
        legacy, staged = managed.commands()
        self.assertIn('-timeout=3m', legacy)
        self.assertIn('-timeout=12m', staged)
        self.assertEqual(staged[-1], './' + managed.STAGED_PACKAGE)
        self.assertEqual(staged[staged.index('-run') + 1], '^' + managed.STAGED + '$')
        legacy_pattern = legacy[legacy.index('-run') + 1]
        self.assertNotIn(managed.STAGED, legacy_pattern)
        for package, name in managed.REQUIRED_TOP_LEVEL:
            if name != managed.STAGED:
                self.assertIn(name, legacy_pattern)
                self.assertIn('./' + package.removeprefix(managed.PREFIX), legacy)

    def test_deleted_required_staged_child_cannot_pass(self):
        for name in managed.STAGED_CHILDREN:
            missing = managed.STAGED + '/' + name
            events = [event for event in self.passing() if event['Test'] != missing]
            self.assertTrue(managed.failures(events, 0), missing)

    def test_skipped_or_failed_child_cannot_hide_behind_parent_pass(self):
        package, parent = sorted(managed.REQUIRED_TOP_LEVEL)[0]
        for action in ('skip', 'fail'):
            events = self.passing() + [{'Package': package, 'Test': parent + '/control', 'Action': action}]
            self.assertTrue(managed.failures(events, 0))

    def test_catalogued_child_requires_start_pass_and_no_rejection(self):
        package, parent = sorted(managed.REQUIRED_TOP_LEVEL)[0]
        child = (package, parent + '/mandatory')
        with patch.object(managed, 'REQUIRED', managed.REQUIRED | {child}):
            complete = self.passing()
            self.assertEqual(managed.failures(complete, 0), [])
            for action in ('run', 'pass'):
                events = [e for e in complete if not ((e['Package'], e['Test']) == child and e['Action'] == action)]
                self.assertTrue(managed.failures(events, 0))
            for action in ('skip', 'fail'):
                events = complete + [{'Package': child[0], 'Test': child[1], 'Action': action}]
                self.assertTrue(managed.failures(events, 0))
