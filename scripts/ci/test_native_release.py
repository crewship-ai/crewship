"""Exercise the actual shell acceptance gate with synthetic Go event streams."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest



SCRIPT = Path(__file__).with_name('native-noatime-fixture.sh')
NAME = 'TestLiveNativeFrozenToolsAndDurableAccounting'


class NativeReleaseGateTest(unittest.TestCase):
    def test_publisher_tests_the_immutable_digest_before_signing(self):
        root = Path(__file__).resolve().parents[2]
        source = (root / '.github/workflows/native-worker-release.yml').read_text()
        publisher = source.split('\n  publish:', 1)[1]
        self.assertIn("if: inputs.publish && github.ref == 'refs/heads/main'", publisher)
        self.assertIn('needs: prepare', publisher)
        steps = publisher.split('\n      - ')[1:]
        scan = next(i for i, step in enumerate(steps) if 'anchore/scan-action@' in step)
        canary = next(i for i, step in enumerate(steps) if 'native-noatime-fixture.sh' in step)
        sign = next(i for i, step in enumerate(steps) if 'cosign sign' in step)
        self.assertLess(scan, canary)
        self.assertLess(canary, sign)
        self.assertNotIn('\n        if:', steps[canary])
        self.assertIn('@${{ steps.build.outputs.digest }}', steps[canary])
        self.assertIn(' green', steps[canary])
        self.assertNotIn('\n        if:', steps[sign])

    def test_actual_atime_header_preserves_every_security_and_atime_combination(self):
        root = Path(__file__).resolve().parents[2]
        source = root / 'cmd/crewship-restricted-native-runner/bubblewrap/testdata/atime.c'
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(temporary) / 'atime-test'
            subprocess.run(['cc', '-std=c99', '-Wall', '-Wextra', '-Werror',
                            str(source), '-o', str(binary)], check=True, timeout=30)
            subprocess.run([str(binary)], check=True, timeout=5)

    def invoke(self, events, code=0, expected='green', existing=False):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fake = root / 'go'
            fake.write_text('#!' + sys.executable + '\nimport os,sys\n'
                            'print(os.environ["NATIVE_TEST_EVENTS"])\n'
                            'sys.exit(int(os.environ["NATIVE_TEST_EXIT"]))\n')
            fake.chmod(0o755)
            output = root / 'output'
            if existing:
                output.mkdir()
                (output / 'preserve').write_text('retained')
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       NATIVE_TEST_EVENTS='\n'.join(json.dumps(e) for e in events),
                       NATIVE_TEST_EXIT=str(code))
            result = subprocess.run(['bash', str(SCRIPT), 'sha256:' + 'a' * 64,
                                     str(output), expected], env=env,
                                    capture_output=True, text=True, timeout=10)
            if existing:
                self.assertEqual((output / 'preserve').read_text(), 'retained')
            return result.returncode

    def test_green_requires_one_actual_run_and_pass_and_successful_exit(self):
        run = {'Test': NAME, 'Action': 'run'}
        passed = {'Test': NAME, 'Action': 'pass'}
        self.assertEqual(self.invoke([run, passed]), 0)
        for events, code in (([], 0), ([passed], 0), ([run], 0),
                             ([run, passed, passed], 0), ([run, run, passed], 0),
                             ([run, {'Test': NAME, 'Action': 'skip'}], 0),
                             ([run, {'Test': NAME, 'Action': 'fail'}], 1),
                             ([run, passed], 1)):
            with self.subTest(events=events, code=code):
                self.assertNotEqual(self.invoke(events, code), 0)

    def test_red_requires_the_executed_resolver_remount_failure(self):
        events = [{'Test': NAME, 'Action': 'run'},
                  {'Test': NAME, 'Action': 'output',
                   'Output': 'remount "/newroot/etc/resolv.conf": Operation not permitted'},
                  {'Test': NAME, 'Action': 'fail'}]
        self.assertEqual(self.invoke(events, 1, 'red'), 0)
        self.assertNotEqual(self.invoke(events, 0, 'red'), 0)
        events[1]['Output'] = 'restricted runtime authority denied'
        self.assertNotEqual(self.invoke(events, 1, 'red'), 0)
        events[1]['Output'] = 'unrelated failure'
        self.assertNotEqual(self.invoke(events, 1, 'red'), 0)

    def test_refuses_an_existing_output_directory(self):
        self.assertNotEqual(self.invoke([], existing=True), 0)


if __name__ == '__main__':
    unittest.main()
