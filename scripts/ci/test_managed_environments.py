import os
import importlib.util
from pathlib import Path
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('managed', Path(__file__).with_name('managed-environments.py'))
managed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(managed)


class ManagedEnvironmentEvidenceTests(unittest.TestCase):
    def test_real_agent_sdk_requires_clean_and_retained_upgrade_without_sudo(self):
        name = 'TestAgentCLIRealDockerCleanAndRetainedUpgrade'
        package = managed.PREFIX + 'internal/provider/docker'
        for test in (name, name + '/clean', name + '/retained-upgrade'):
            key = (package, test)
            self.assertIn(key, managed.REQUIRED)
            missing = [e for e in self.passing() if (e['Package'], e['Test']) != key]
            self.assertTrue(managed.failures(missing, 0))
        command = next(c for c in managed.commands() if name in c[c.index('-run') + 1])
        self.assertNotIn('sudo', command)
        self.assertIn('-timeout=5m', command)

    def test_native_vault_container_tool_cannot_skip_or_omit_receipt(self):
        name = 'TestNativeVaultRealCrewContainerGlab'
        key = (managed.PREFIX + 'internal/orchestrator', name)
        self.assertIn(key, managed.REQUIRED)
        missing = [e for e in self.passing() if (e['Package'], e['Test']) != key]
        self.assertTrue(managed.failures(missing, 0))
        skipped = missing + [{'Package': key[0], 'Test': name, 'Action': 'skip'}]
        self.assertTrue(managed.failures(skipped, 0))
        command = next(c for c in managed.commands() if name in c[c.index('-run') + 1])
        self.assertNotIn('sudo', command)
        self.assertIn('./internal/orchestrator', command)

    def test_actual_cli_trio_requires_each_shell_subtest_and_nonroot_execution(self):
        expected = {'CLAUDE_CODE', 'CODEX_CLI', 'OPENCODE'}
        package = managed.PREFIX + 'internal/orchestrator'
        for adapter in expected:
            self.assertIn((package, 'TestNativeCLIShellEnvironment/' + adapter), managed.REQUIRED)
            for mode in ('direct', 'tmux', 'fallback'):
                self.assertIn((package, 'TestNativeCLIShellEnvironment/' + adapter + '/' + mode), managed.REQUIRED)
        commands = managed.commands()
        command = next(c for c in commands if 'TestNativeCLIShellEnvironment' in c[c.index('-run') + 1])
        self.assertNotIn('sudo', command)
        self.assertIn('./internal/orchestrator', command)

    def test_vault_pipeline_requires_each_actual_cli_not_only_parent_pass(self):
        package = managed.PREFIX + 'internal/orchestrator'
        root = 'TestNativeVaultCLITrioProtocols'
        self.assertIn((package, root), managed.REQUIRED)
        for adapter in ('CLAUDE_CODE', 'CODEX_CLI', 'OPENCODE'):
            key = (package, root + '/' + adapter)
            self.assertIn(key, managed.REQUIRED)
            events = [e for e in self.passing() if (e['Package'], e['Test']) != key]
            self.assertTrue(managed.failures(events, 0))
        command = next(c for c in managed.commands() if root in c[c.index('-run') + 1])
        self.assertNotIn('sudo', command)
    def test_native_mise_packaging_requires_clean_and_upgrade_receipts(self):
        package = managed.PREFIX + 'internal/devcontainer'
        root = 'TestCodexNativePackaging'
        for name in (root, root + '/clean', root + '/upgrade'):
            key = (package, name)
            self.assertIn(key, managed.REQUIRED)
            events = [e for e in self.passing() if (e['Package'], e['Test']) != key]
            self.assertTrue(managed.failures(events, 0))

    def test_all_guarded_host_fixture_sources_are_mandatory(self):
        root = Path(__file__).resolve().parents[2]
        self.assertEqual(managed.host_source_inventory_errors(root), [])
        package = 'internal/runoutput'
        name = 'TestHostCaptureHonorsHostUIDBoundary'
        with mock.patch.dict(managed.HOST_TESTS,
                             {package: managed.HOST_TESTS[package] - {name}}):
            errors = managed.host_source_inventory_errors(root)
            self.assertTrue(any(name in error for error in errors))

    def test_exact_tmux_target_receipt_is_required_despite_green_parent(self):
        key = (managed.PREFIX + 'internal/orchestrator',
               'TestNativeTmuxProbeDoesNotTurnClientFailureIntoStopped/exact-session-targets')
        self.assertIn(key, managed.REQUIRED)
        events = [e for e in self.passing() if (e['Package'], e['Test']) != key]
        self.assertTrue(managed.failures(events, 0))

    def test_native_tmux_requires_shell_ownership_cancellation_and_bootstrap_receipts(self):
        package = managed.PREFIX + 'internal/orchestrator'
        for shell in ('dash', 'busybox'):
            for name in ('TestNativeTmuxReaderKernelOwnership', 'TestNativeTmuxObserverCancellationReapsReaderAndRetainsOwner', 'TestNativeTmuxFreshServerRunSettles'):
                self.assertIn((package, name + '/' + shell), managed.REQUIRED)
            for mode in ('tmux', 'fallback'):
                for missing in ('env', 'run.sh', 'early-exit'):
                    self.assertIn((package, 'TestNativeTmuxMissingSnapshotCannotSucceed/' + shell + '/' + mode + '/' + missing), managed.REQUIRED)

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
        legacy, launcher, cli, packaging, sdk = managed.commands()
        self.assertEqual(legacy[0], 'go')
        self.assertNotIn('sudo', legacy)
        self.assertNotIn('./internal/orchestrator', legacy)
        self.assertEqual(launcher[:2], ['sudo', 'env'])
        self.assertEqual(launcher[-1], './internal/orchestrator')
        self.assertEqual(launcher[launcher.index('-run') + 1], '^(TestCodexOuterActualNativeDocker|TestManagedLaunchRealDocker)$')
        self.assertNotIn('sudo', packaging)
        self.assertIn('./internal/devcontainer', packaging)
        self.assertIn('-timeout=10m', packaging)
        self.assertIn('-timeout=3m', legacy)
        self.assertIn('-timeout=3m', cli)
        self.assertIn('-timeout=8m', launcher)
        self.assertNotIn('sudo', cli)
        self.assertEqual(cli[cli.index('-run') + 1], '^(TestNativeCLIShellEnvironment|TestNativeTmuxCurrentRunCannotInheritRevokedToken|TestNativeTmuxFallbackStreamsFailure|TestNativeTmuxFreshServerRunSettles|TestNativeTmuxMissingSnapshotCannotSucceed|TestNativeTmuxObserverCancellationReapsReaderAndRetainsOwner|TestNativeTmuxProbeDoesNotTurnClientFailureIntoStopped|TestNativeTmuxReaderKernelOwnership|TestNativeTmuxRefusalTeardownPreservesOutsideHome|TestNativeTmuxRefusesExistingSetupDirectory|TestNativeVaultCLITrioProtocols)$')
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

    def test_real_codex_artifact_is_explicit_across_sudo(self):
        with mock.patch.dict(os.environ, {'CREWSHIP_TEST_CODEX_NATIVE': '/fixture/native-codex'}):
            legacy, elevated, cli, packaging, sdk = managed.commands()
        self.assertNotIn('-codex-outer-native-artifact=/fixture/native-codex', legacy)
        self.assertNotIn('-codex-outer-native-artifact=/fixture/native-codex', cli)
        self.assertIn('-codex-outer-native-artifact=/fixture/native-codex', elevated)
        self.assertIn('TestCodexOuterActualNativeDocker', elevated[elevated.index('-run') + 1])
        with mock.patch.dict(os.environ, {'CREWSHIP_TEST_CODEX_NATIVE': 'relative'}):
            with self.assertRaises(ValueError):
                managed.commands()

    def test_selected_go_toolchain_survives_sudo_and_nested_fixture_builds(self):
        binary = '/trusted/toolchain/bin/go'
        legacy, elevated, cli, packaging, sdk = managed.commands(binary)
        self.assertEqual(legacy[0], binary)
        self.assertEqual(cli[0], binary)
        self.assertEqual(elevated[elevated.index('test') - 1], binary)
        path = next(arg for arg in elevated if arg.startswith('PATH='))
        self.assertTrue(path.startswith('PATH=/trusted/toolchain/bin:'))
        self.assertIn('GOTOOLCHAIN=local', elevated)
        for command in managed.host_commands('/tmp/artifacts', '/tmp/private-home', 998, binary):
            self.assertEqual(command[0], binary)
            # The restricted workload environment does not gain a toolchain
            # cache path; test2json runs outside the UID transition.
            self.assertIn('PATH=' + managed.os.environ['PATH'], command)
