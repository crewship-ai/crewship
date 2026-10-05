"""Runner contract regressions use Docker/Go doubles, never live acceptance."""
import importlib.util
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('native_runtime', Path(__file__).with_name('native-runtime.py'))
native = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(native)
IMAGE = 'ghcr.io/crewship-ai/native@sha256:' + 'a' * 64
LOCAL = 'sha256:' + 'b' * 64
CONTAINER = 'c' * 64


def inspected(**kwargs):
    return [dict(Id=LOCAL, RepoDigests=[IMAGE], Os='linux', Architecture='amd64', **kwargs)]


def events():
    result = []
    for package, test in native.TESTS.items():
        result += [{'Action': 'pass', 'Package': package, 'Test': test},
                   {'Action': 'pass', 'Package': package}]
    return result


class NativeRuntimeTests(unittest.TestCase):
    def test_only_explicit_ghcr_digest_is_allowed(self):
        self.assertEqual(native.pinned_image(IMAGE), IMAGE)
        for value in ['ghcr.io/team/native:latest', LOCAL, 'docker.io/team/native@sha256:' + 'a' * 64,
                      IMAGE[:-1], IMAGE.upper(), IMAGE + ':tag', 'ghcr.io/team:tag/native@sha256:' + 'a' * 64]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                native.pinned_image(value)

    def test_registry_digest_maps_to_distinct_local_config_id(self):
        identity = native.image_identity(IMAGE, inspected())
        self.assertEqual(identity['registry_reference'], IMAGE)
        self.assertEqual(identity['local_config_id'], LOCAL)
        for changed in [{'RepoDigests': []}, {'Architecture': 'arm64'}, {'Os': 'windows'}, {'Id': IMAGE}]:
            entry = inspected()[0]
            entry.update(changed)
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                native.image_identity(IMAGE, [entry])

    def test_named_passes_need_correct_package_and_package_success(self):
        self.assertEqual(len(native.require_passes(events())), 1)
        for altered in [events()[:-2], events()[:-1],
                        [dict(event, Package='foreign') for event in events()],
                        [dict(event, Action='skip') if event.get('Test') else event for event in events()],
                        events() + [{'Action': 'output', 'Output': 'WARNING: DATA RACE'}],
                        events() + [{'Action': 'skip', 'Package': next(iter(native.TESTS)), 'Test': 'required/subtest'}],
                        events() + [{'Action': 'fail', 'Package': next(iter(native.TESTS))}]]:
            with self.subTest(altered=altered), self.assertRaises(ValueError):
                native.require_passes(altered)

    def stubbed_run(self, output, go_events=None, go_exit=0, checksum=True):
        calls = []
        self.calls = calls
        def command(*args, **kwargs):
            calls.append(args)
            if args[:2] == ('docker', 'pull'):
                self.assertEqual(args[2:], ('--platform=linux/amd64', IMAGE))
            elif args[:3] == ('docker', 'image', 'inspect'):
                return json.dumps(inspected())
            elif args[:2] == ('docker', 'create'):
                self.assertIn(LOCAL, args)
                for setting in ['--pull=never', '--network=none', '--read-only', '--cap-drop=ALL',
                                '--security-opt=no-new-privileges', '--user=1001:1001',
                                '--cpus=0.5', '--memory=128m', '--pids-limit=32', '--entrypoint=/opt/codex']:
                    self.assertIn(setting, args)
                return CONTAINER
            elif args[:2] == ('docker', 'cp'):
                self.assertEqual(args[2], CONTAINER + ':/opt/codex')
                Path(args[3]).write_bytes(b'codex stub bytes')
            elif args[:2] == ('docker', 'start'):
                return native.CODEX_VERSION
            elif args[:2] == ('docker', 'inspect'):
                return json.dumps([{'State': {'Running': False, 'ExitCode': 0}}])
            elif args[:2] == ('docker', 'rm'):
                self.assertEqual(args[2:], ('--force', CONTAINER))
            elif args[:2] == ('git', 'rev-parse'):
                return 'source-sha'
            else:
                self.fail(f'Unexpected command: {args}')
            return ''
        def go(args, **kwargs):
            self.assertEqual(args[:3], ['go', 'test', '-race'])
            self.assertIn('restrictedruntime_live', args)
            self.assertIn('-count=1', args)
            self.assertIn('7m', args)
            self.assertEqual(kwargs['timeout'], 480)
            for package in native.TESTS:
                self.assertIn('./' + package.split('github.com/crewship-ai/crewship/')[1], args)
            self.assertEqual(kwargs['env']['CREWSHIP_RESTRICTED_LIVE'], '1')
            self.assertEqual(kwargs['env']['CREWSHIP_RESTRICTED_NATIVE_IMAGE'], LOCAL)
            kwargs['stdout'].write('\n'.join(json.dumps(event) for event in (events() if go_events is None else go_events)))
            return subprocess.CompletedProcess(args, go_exit)
        class Hash:
            def hexdigest(self):
                return native.CODEX_SHA256 if checksum else 'bad checksum'
        with patch.object(native, 'command', command), patch.object(native.subprocess, 'run', go), \
                patch.object(native.hashlib, 'sha256', lambda _: Hash()), redirect_stdout(io.StringIO()):
            native.run(IMAGE, output)
        return calls

    def test_runner_preserves_mapping_and_real_named_evidence_contract(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / 'evidence'
            calls = self.stubbed_run(output)
            identity = json.loads((output / 'image-identity.json').read_text())
            self.assertEqual(identity['status'], 'passed')
            self.assertEqual(identity['local_config_id'], LOCAL)
            self.assertEqual(identity['registry_reference'], IMAGE)
            self.assertEqual(len(identity['required_passes']), 1)
            self.assertTrue((output / 'go-test.jsonl').is_file())
            self.assertLess(next(i for i, call in enumerate(calls) if call[:2] == ('docker', 'cp')),
                            next(i for i, call in enumerate(calls) if call[:2] == ('docker', 'start')))

    def test_missing_or_skipped_named_run_and_nonzero_go_exit_fail_closed(self):
        for evidence, exit_code in [([], 0), (events()[:-2], 0), (events(), 1),
                                    ([dict(event, Action='skip') for event in events()], 0)]:
            with self.subTest(evidence=evidence, exit_code=exit_code), tempfile.TemporaryDirectory() as folder:
                output = Path(folder) / 'evidence'
                with self.assertRaises(ValueError):
                    self.stubbed_run(output, evidence, exit_code)
                self.assertEqual(json.loads((output / 'image-identity.json').read_text())['status'], 'failed')
                self.assertTrue((output / 'go-test.jsonl').is_file())

    def test_checksum_failure_does_not_execute_binary_and_removes_only_owned_container(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / 'evidence'
            with self.assertRaisesRegex(ValueError, 'checksum'):
                self.stubbed_run(output, checksum=False)
            self.assertEqual(json.loads((output / 'image-identity.json').read_text())['status'], 'failed')
            self.assertFalse((output / 'go-test.jsonl').exists())
            self.assertFalse(any(call[:2] == ('docker', 'start') for call in self.calls))
            self.assertEqual(self.calls[-1], ('docker', 'rm', '--force', CONTAINER))

    def test_malformed_create_output_removes_only_the_unique_owned_name(self):
        calls = []
        def docker(*args, **kwargs):
            calls.append(args)
            if args[:2] == ('docker', 'create'):
                return 'foreign-or-malformed-output'
            self.assertEqual(args[:3], ('docker', 'rm', '--force'))
            return ''
        with patch.object(native, 'command', docker):
            with self.assertRaisesRegex(ValueError, 'owned container ID'):
                native.verify_codex(LOCAL)
        self.assertEqual(len(calls), 2)
        name = calls[0][calls[0].index('--name') + 1]
        self.assertRegex(name, r'^crewship-ci-native-identity-[0-9a-f]{32}$')
        self.assertEqual(calls[1], ('docker', 'rm', '--force', name))

    def test_named_sources_and_binary_pins_match_product_contracts(self):
        dockerfile = (native.ROOT / 'cmd/crewship-restricted-native-runner/Dockerfile').read_text()
        self.assertIn(native.CODEX_SHA256 + '  /opt/codex', dockerfile)
        self.assertIn(native.CODEX_VERSION, dockerfile)
        for package, name in native.TESTS.items():
            directory = native.ROOT / package.split('github.com/crewship-ai/crewship/')[1]
            source = '\n'.join(path.read_text() for path in directory.glob('*_test.go'))
            self.assertIn('func ' + name + '(t *testing.T)', source)

    def test_existing_output_rejected_to_prevent_stale_evidence(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(native, 'command') as command:
            with self.assertRaises(FileExistsError):
                native.run(IMAGE, Path(folder))
            command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
