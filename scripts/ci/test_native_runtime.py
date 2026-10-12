"""Runner contract regressions use Docker/Go doubles, never live acceptance."""
import importlib.util
from contextlib import redirect_stdout
import io
import os
import signal
import sys
import time
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

    def stubbed_run(self, output, go_events=None, go_exit=0, checksum=True, go_error=None):
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
            elif args[:2] == ('docker', 'ps') or args[:3] == ('docker', 'volume', 'ls'):
                self.assertIn('--filter', args)
                self.assertRegex(args[-1], r'^label=ai\.crewship\.native-acceptance\.run=[0-9a-f]{32}$')
                return ''
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
            if go_error:
                raise go_error
            return subprocess.CompletedProcess(args, go_exit)
        class Hash:
            def hexdigest(self):
                return native.CODEX_SHA256 if checksum else 'bad checksum'
        with patch.object(native, 'command', command), patch.object(native, 'bounded_process', go), \
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
            self.assertIn(('docker', 'rm', '--force', CONTAINER), self.calls)

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
                native.verify_codex(LOCAL, 'd' * 32)
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

    def test_proxy_labels_only_creations(self):
        label = ['--label', native.RUN_LABEL + '=' + 'd' * 32]
        for args, want in [(['create', 'image'], ['create', *label, 'image']),
                           (['run', 'image'], ['run', *label, 'image']),
                           (['volume', 'create', 'name'], ['volume', 'create', *label, 'name']),
                           (['image', 'inspect', LOCAL], ['image', 'inspect', LOCAL])]:
            self.assertEqual(native.proxy_args(args, 'd' * 32), want)
        with self.assertRaises(ValueError):
            native.proxy_args(['create'], 'foreign')

    def test_cleanup_removes_only_label_selected_containers_then_volumes(self):
        calls = []
        def docker(*args, **kwargs):
            calls.append(args)
            if args[:2] == ('docker', 'ps'):
                return CONTAINER if len(calls) == 1 else ''
            if args[:3] == ('docker', 'volume', 'ls'):
                return 'owned-volume' if len(calls) == 4 else ''
            return ''
        with patch.object(native, 'command', docker):
            result = native.cleanup_owned('d' * 32)
        self.assertEqual(result, {'containers': [CONTAINER], 'volumes': ['owned-volume']})
        self.assertIn('--no-trunc', calls[0])
        self.assertEqual(calls[1], ('docker', 'rm', '--force', CONTAINER))
        self.assertEqual(calls[4], ('docker', 'volume', 'rm', 'owned-volume'))
        for call in (calls[0], calls[2], calls[3], calls[5]):
            self.assertEqual(call[-1], 'label=' + native.RUN_LABEL + '=' + 'd' * 32)

    def test_timeout_and_cancellation_write_failure_and_cleanup_receipts(self):
        for error in (subprocess.TimeoutExpired(['go', 'test'], 480), native.CancelledError('cancelled')):
            with self.subTest(error=error), tempfile.TemporaryDirectory() as folder:
                output = Path(folder) / 'evidence'
                with self.assertRaises(type(error)):
                    self.stubbed_run(output, go_error=error)
                identity = json.loads((output / 'image-identity.json').read_text())
                self.assertEqual(identity['status'], 'failed')
                self.assertEqual(identity['cleanup'], {'containers': [], 'volumes': []})
                self.assertTrue(identity['error'])

    def test_cleanup_failure_never_reports_pass(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / 'evidence'
            with patch.object(native, 'cleanup_owned', side_effect=RuntimeError('cleanup failed')):
                with self.assertRaisesRegex(RuntimeError, 'cleanup failed'):
                    self.stubbed_run(output)
            identity = json.loads((output / 'image-identity.json').read_text())
            self.assertEqual(identity['status'], 'failed')
            self.assertEqual(identity['cleanup_error'], 'cleanup failed')

    def test_timeout_kills_child_even_when_child_ignores_term(self):
        with tempfile.TemporaryDirectory() as folder:
            pidfile = Path(folder) / 'child.pid'
            script = ('import subprocess, sys, time; '
                      'p=subprocess.Popen([sys.executable,"-c",'
                      '"import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)"]); '
                      'open(sys.argv[1],"w").write(str(p.pid)); time.sleep(60)')
            started = time.monotonic()
            with self.assertRaises(subprocess.TimeoutExpired):
                native.bounded_process([sys.executable, '-c', script, str(pidfile)], timeout=1)
            self.assertLess(time.monotonic() - started, 7)
            # An orphan can remain a zombie until the host init reaps it.
            state = Path('/proc') / pidfile.read_text() / 'stat'
            self.assertTrue(not state.exists() or state.read_text().split()[2] == 'Z')

    def test_sigterm_cancels_and_restores_handlers(self):
        before = signal.getsignal(signal.SIGTERM)
        with self.assertRaises(native.CancelledError):
            with native.cancellation():
                os.kill(os.getpid(), signal.SIGTERM)
        self.assertEqual(signal.getsignal(signal.SIGTERM), before)

    def test_existing_output_rejected_to_prevent_stale_evidence(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(native, 'command') as command:
            with self.assertRaises(FileExistsError):
                native.run(IMAGE, Path(folder))
            command.assert_not_called()


@unittest.skipUnless(os.getenv('CREWSHIP_NATIVE_CLEANUP_DOCKER') == '1',
                     'explicit Docker cleanup receipt opt-in required')
class NativeDockerCleanupTests(unittest.TestCase):
    """Synthetic Alpine cleanup evidence; never native runtime acceptance."""

    def receipt(self, cancel):
        run_id = native.uuid.uuid4().hex
        foreign = native.uuid.uuid4().hex
        timer = None
        try:
            native.command('docker', *native.proxy_args(
                ['volume', 'create', 'crewship-native-receipt-' + foreign], foreign))
            native.command('docker', *native.proxy_args(
                ['volume', 'create', 'crewship-native-receipt-' + run_id], run_id))
            container = native.command('docker', *native.proxy_args(
                ['create', '--pull=never', '--network=none', '--read-only', '--cap-drop=ALL',
                 '--memory=32m', '--pids-limit=8', '--mount',
                 'type=volume,source=crewship-native-receipt-' + run_id + ',target=/owned',
                 'alpine:3', 'sleep', '60'], run_id)).strip()
            native.command('docker', 'start', container)
            with self.assertRaises(native.CancelledError if cancel else subprocess.TimeoutExpired):
                with native.cancellation():
                    if cancel:
                        import threading
                        timer = threading.Timer(1, lambda: os.kill(os.getpid(), signal.SIGTERM))
                        timer.start()
                    native.bounded_process([sys.executable, '-c', 'import time; time.sleep(60)'],
                                           timeout=10 if cancel else 1)
            cleaned = native.cleanup_owned(run_id)
            self.assertEqual(cleaned['containers'], [container])
            self.assertEqual(cleaned['volumes'], ['crewship-native-receipt-' + run_id])
            self.assertEqual(native.command('docker', 'volume', 'ls', '-q', '--filter',
                             'label=' + native.RUN_LABEL + '=' + foreign).strip(),
                             'crewship-native-receipt-' + foreign)
            print(json.dumps({'scenario': 'sigterm' if cancel else 'timeout', 'run_id': run_id,
                              'cleanup': cleaned, 'foreign_volume_preserved': True}))
        finally:
            if timer:
                timer.cancel()
            native.cleanup_owned(run_id)
            native.cleanup_owned(foreign)

    def test_real_docker_timeout_cleanup(self):
        self.receipt(False)

    def test_real_docker_sigterm_cleanup(self):
        self.receipt(True)


if __name__ == '__main__':
    unittest.main()
