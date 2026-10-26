from pathlib import Path
import json
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class TextReleaseTests(unittest.TestCase):
    def test_existing_context_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as folder:
            sentinel = Path(folder) / 'keep'
            sentinel.write_text('previous candidate')
            result = subprocess.run(['bash', str(ROOT / 'scripts/ci/prepare-text-release.sh'), folder],
                                    capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(sentinel.read_text(), 'previous candidate')
            self.assertEqual(list(Path(folder).iterdir()), [sentinel])

    def test_publisher_requires_verified_main_before_privileged_job(self):
        text = (ROOT / '.github/workflows/text-worker-release.yml').read_text()
        prepare, publish = text.split('\n  publish:\n', 1)
        self.assertIn("if: github.ref == 'refs/heads/main'", prepare)
        self.assertIn('SHA: ${{ github.sha }}', prepare)
        self.assertIn('python3 scripts/ci/verified_commit.py "$REPO" "$SHA"', prepare)
        self.assertIn('default: false', prepare)
        self.assertNotIn('pull_request', text)
        self.assertNotIn('packages: write', prepare)
        self.assertNotIn('id-token: write', prepare)
        self.assertIn("if: inputs.publish && github.ref == 'refs/heads/main'", publish)
        self.assertIn('needs: prepare', publish)
        self.assertLess(publish.index('severity-cutoff: critical'), publish.index('cosign sign --yes'))
        self.assertIn('cosign sign --yes "$IMAGE"', publish)
        self.assertLess(publish.index('go test -tags restrictedruntime_live'), publish.index('cosign sign --yes'))
        self.assertIn('docker pull "$IMAGE"', publish)
        self.assertIn('CREWSHIP_RESTRICTED_PRODUCTION_IMAGE="$immutable_image_id"', publish)
        self.assertIn('runner.sha256', publish)
        self.assertIn('provenance: mode=max', publish)
        self.assertIn('sbom: true', publish)

    def test_pr_canary_never_accepts_skip_missing_or_failed_test(self):
        text = (ROOT / '.github/workflows/text-worker-candidate.yml').read_text()
        self.assertNotIn('packages: write', text)
        self.assertNotIn('id-token: write', text)
        self.assertNotIn('secrets.', text)
        self.assertNotIn('cosign sign', text)
        self.assertIn('ref: ${{ github.event.pull_request.head.sha }}', text)
        release = (ROOT / '.github/workflows/text-worker-release.yml').read_text()
        pattern = r"<<'PYTEST'\n(.*?)\n          PYTEST"
        code = re.search(pattern, text, re.S).group(1)
        self.assertEqual(re.search(pattern, release, re.S).group(1), code)
        code = '\n'.join(line.removeprefix('          ') for line in code.splitlines())
        name = 'TestLiveProductionWorkerDurableAccounting'
        with tempfile.TemporaryDirectory() as folder:
            report = Path(folder) / 'events.jsonl'
            for actions, good in [(['run', 'pass'], True), (['run', 'skip'], False),
                                  (['run', 'fail'], False), ([], False), (['run', 'pass', 'pass'], False)]:
                report.write_text(''.join(json.dumps({'Test': name, 'Action': action})+'\n' for action in actions))
                result = subprocess.run(['python3', '-c', code, str(report)], capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, good, actions)


if __name__ == '__main__':
    unittest.main()
