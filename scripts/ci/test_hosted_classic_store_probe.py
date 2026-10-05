"""Fail-closed evidence classification for the disposable hosted probe."""
from pathlib import Path
import json
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('hosted-classic-store-probe.sh').read_text()
IMAGE = 'ghcr.io/crewship-ai/crewship@sha256:b74257f22023eb498852d9f4a024c749aff5780bd3961dd5e3b37494b283ae13'
AMD = '    image linux/amd64: 141 go texts + 1123 npm texts match their manifests (sha256)\n'
ARM = AMD.replace('amd64', 'arm64')
UNAVAILABLE = f'UNVERIFIED  image: linux/arm64 unavailable for {IMAGE}:\n'
VERIFIED = f'VERIFIED    image {IMAGE}: legal files + manifest texts hash-match on:'


def embedded_code(marker):
    return SCRIPT.split(marker + " <<'PY'\n", 1)[1].split('\nPY\n', 1)[0]


class HostedClassicStoreProbe(unittest.TestCase):
    def classify(self, checker, rc, log):
        code = embedded_code('python3 - "$CHECKER" "$rc" "$out/checker.log" "$IMAGE"')
        with tempfile.NamedTemporaryFile(mode='w') as f:
            f.write(log)
            f.flush()
            return subprocess.run(['python3', '-c', code, checker, str(rc), f.name, IMAGE],
                                  capture_output=True, text=True).returncode

    def test_red_requires_specific_architecture_evidence(self):
        red = AMD + UNAVAILABLE + '    cannot overwrite digest\n' + VERIFIED + ' linux/amd64\n'
        self.assertEqual(self.classify('old', 1, red), 0)
        for rc, log in [(2, red), (1, 'network failure\n'), (1, red.replace(AMD, '')),
                        (1, UNAVAILABLE + AMD + VERIFIED + ' linux/amd64\n'),
                        (1, red + 'FAIL  missing license\n'), (0, red)]:
            with self.subTest(rc=rc, log=log):
                self.assertNotEqual(self.classify('old', rc, log), 0)

    def test_green_requires_both_hash_checks_and_strict_success(self):
        green = AMD + ARM + VERIFIED + ' linux/amd64 linux/arm64\n'
        # The production checker footer contains the word UNVERIFIED.
        green += 'check-release-artifacts: done (channels above; any UNVERIFIED is explicit)\n'
        self.assertEqual(self.classify('fixed', 0, green), 0)
        for rc, log in [(1, green), (0, green.replace(ARM, '')), (0, green + UNAVAILABLE),
                        (0, green.replace('1123', '1122')), (0, green + 'FAIL  bad hash\n')]:
            with self.subTest(rc=rc, log=log):
                self.assertNotEqual(self.classify('fixed', rc, log), 0)

    def test_runner_drift_is_blocked(self):
        code = embedded_code('python3 - "$out/docker-info.json"')
        for version, driver, status, expected in [
                ('28.0.4', 'overlay2', [], 0),
                ('29.0.0', 'overlay2', [], 78),
                ('28.0.4', 'overlayfs', [['driver-type', 'io.containerd.snapshotter.v1']], 78),
                ('28.0.4', 'overlay2', [['driver-type', 'containerd']], 78)]:
            with self.subTest(version=version, driver=driver), tempfile.NamedTemporaryFile(mode='w') as f:
                json.dump({'ServerVersion': version, 'Driver': driver, 'DriverStatus': status}, f)
                f.flush()
                result = subprocess.run(['python3', '-c', code, f.name], capture_output=True)
                self.assertEqual(result.returncode, expected)

    def test_red_retry_rejects_network_errors_and_unexpected_success(self):
        code = embedded_code('python3 - "$retry_rc" "$out/red-retry-stderr.txt"')
        for rc, stderr, expected in [
                (1, 'Error response from daemon: cannot overwrite digest sha256:abc', 0),
                (1, 'Error response from daemon: connection timed out', 1),
                (1, 'manifest unknown', 1),
                (124, 'cannot overwrite digest', 1),
                (0, '', 1)]:
            with self.subTest(rc=rc, stderr=stderr), tempfile.NamedTemporaryFile(mode='w') as f:
                f.write(stderr)
                f.flush()
                result = subprocess.run(['python3', '-c', code, str(rc), f.name], capture_output=True)
                self.assertEqual(result.returncode, expected)
