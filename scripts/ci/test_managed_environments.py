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

    def test_existing_commands_preserve_their_original_privilege_profiles(self):
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

    def test_protected_tests_run_as_distinct_unprivileged_uid_with_empty_environment(self):
        commands = managed.host_commands('/tmp/artifacts', '/tmp/private-home', 998)
        self.assertEqual(len(commands), len(managed.HOST_TESTS))
        for command, (package, names) in zip(commands, managed.HOST_TESTS.items()):
            self.assertEqual(command[:6], ['go', 'tool', 'test2json', '-t', '-p', managed.PREFIX + package])
            self.assertIn('--reuid=65534', command)
            self.assertIn('--regid=998', command)
            self.assertIn('--clear-groups', command)
            self.assertEqual(command[command.index('env') + 1], '-i')
            self.assertIn('CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED=1', command)
            self.assertIn('CREWSHIP_HOST_CAPTURE_BINARY=/tmp/artifacts/crewship', command)
            self.assertIn('CREWSHIP_HOST_CAPTURE_SIDECAR=/tmp/artifacts/crewship-sidecar', command)
            self.assertIn('HOME=/tmp/private-home', command)
            self.assertIn('TMPDIR=/tmp', command)
            self.assertIn('/tmp/artifacts/' + package.replace('/', '-') + '.test', command)
            pattern = next(arg for arg in command if arg.startswith('-test.run='))
            for name in names:
                self.assertIn(name, pattern)

    def test_protected_evidence_is_required_even_when_legacy_suite_passes(self):
        legacy = {(managed.PREFIX + package, name)
                  for package, names in managed.TESTS.items() for name in names}
        events = [event for event in self.passing()
                  if (event['Package'], event['Test']) in legacy]
        self.assertTrue(managed.failures(events, 0))
