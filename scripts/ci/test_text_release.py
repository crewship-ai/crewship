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
        self.assertIn('bash scripts/ci/verify-text-image-contents.sh "$IMAGE"', publish)
        self.assertLess(publish.index('verify-text-image-contents.sh'), publish.index('cosign sign --yes'))
        self.assertIn('provenance: mode=max', publish)
        self.assertIn('sbom: true', publish)

    def test_image_contents_are_verified_without_a_shell_in_the_image(self):
        # #5103: the runner image is built from scratch, so a check that runs
        # /bin/sh inside it can never pass. Both workflows verify from the host.
        for name in ('text-worker-release.yml', 'text-worker-candidate.yml'):
            text = (ROOT / '.github/workflows' / name).read_text()
            self.assertNotIn('--entrypoint /bin/sh', text)
            self.assertIn('scripts/ci/verify-text-image-contents.sh', text)
        script = (ROOT / 'scripts/ci/verify-text-image-contents.sh').read_text()
        self.assertIn('sha256sum -c runner.sha256', script)
        self.assertIn('/usr/share/licenses/crewship-text/go/manifest.tsv', script)
        dockerfile = (ROOT / 'cmd/crewship-restricted-runner/Dockerfile').read_text()
        self.assertIn('ARG BASE=scratch', dockerfile)

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
        required = {
            ('github.com/crewship-ai/crewship/internal/restricteddispatch', 'TestLiveProductionWorkerDurableAccounting'),
            ('github.com/crewship-ai/crewship/internal/api', 'TestLiveRestrictedTextRouterProductionWorker'),
        }
        good_events = [{'Package': package, 'Test': name, 'Action': action}
                       for package, name in required for action in ('run', 'pass')]
        cases = [(good_events, True), ([], False)]
        for package, name in required:
            other = [event for event in good_events if (event['Package'], event['Test']) != (package, name)]
            cases.append((other, False))
            for actions in [('run', 'skip'), ('run', 'fail'), ('run', 'pass', 'pass'), ('pass',)]:
                cases.append((other + [{'Package': package, 'Test': name, 'Action': action} for action in actions], False))
            cases.append((other + [{'Package': 'wrong/package', 'Test': name, 'Action': action} for action in ('run', 'pass')], False))
        with tempfile.TemporaryDirectory() as folder:
            report = Path(folder) / 'events.jsonl'
            for events, good in cases:
                report.write_text(''.join(json.dumps(event)+'\n' for event in events))
                result = subprocess.run(['python3', '-c', code, str(report)], capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, good, events)

    def test_candidate_covers_real_router_authority_paths(self):
        text = (ROOT / '.github/workflows/text-worker-candidate.yml').read_text()
        for path in ('internal/api/**', 'internal/access/**', 'internal/auth/**', 'internal/restrictedworkflow/**'):
            self.assertIn('- '+path, text)
        for name in ('text-worker-candidate.yml', 'text-worker-release.yml'):
            workflow = (ROOT / '.github/workflows' / name).read_text()
            self.assertIn('go test -tags restrictedruntime_live ./internal/restricteddispatch ./internal/api', workflow)
            self.assertIn('TestLiveRestrictedTextRouterProductionWorker)$', workflow)
            self.assertIn('-p 1 -count=1 -timeout 10m -json', workflow)



if __name__ == '__main__':
    unittest.main()
