import importlib.util
from pathlib import Path
import unittest

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

    def test_sudo_is_limited_to_managed_launcher_fixture(self):
        legacy, launcher = managed.commands()
        self.assertEqual(legacy[0], 'go')
        self.assertNotIn('sudo', legacy)
        self.assertNotIn('./internal/orchestrator', legacy)
        self.assertEqual(launcher[:2], ['sudo', 'env'])
        self.assertEqual(launcher[-1], './internal/orchestrator')
        self.assertEqual(launcher[launcher.index('-run') + 1], '^(TestManagedLaunchRealDocker)$')
        for package, names in managed.TESTS.items():
            for name in names:
                command = launcher if package == 'internal/orchestrator' else legacy
                self.assertIn(name, command[command.index('-run') + 1])
                self.assertIn('./' + package, command)
