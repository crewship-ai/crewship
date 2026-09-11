"""Architecture invariants supplement actionlint's syntax/type validation."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


class WorkflowContracts(unittest.TestCase):
    def text(self, name):
        return (ROOT / '.github/workflows' / name).read_text()

    def job(self, text, name):
        return re.search(r'^  ' + re.escape(name) + r':\n(.*?)(?=^  [a-z][\w-]*:\n|\Z)', text, re.M | re.S).group(1)

    def test_race_shards_are_parallel_and_required(self):
        ci = self.text('ci.yml')
        race = self.job(ci, 'go-race')
        self.assertNotIn('go-race-cli', re.search(r'^    needs:.*$', race, re.M).group())
        result = self.job(ci, 'result')
        needs = set(re.search(r'needs: \[(.*?)\]', result).group(1).split(', '))
        jobs = set(re.findall(r'^  ([a-z][\w-]*):$', ci.split('jobs:\n', 1)[1], re.M))
        self.assertEqual(needs, jobs - {'result'})
        self.assertIn('if: always()', result)
        self.assertIn('merge_group:', ci)
        self.assertNotIn('paths-ignore:', ci)

    def test_browser_jobs_share_real_build_and_keep_independent_db(self):
        ci = self.text('ci.yml')
        for key in ('playwright-pr', 'onboarding-journey'):
            job = self.job(ci, key)
            self.assertIn('ci-server-${{ github.sha }}', job)
            self.assertNotIn('pnpm build', job)
            self.assertIn('Generate ephemeral', job)
        self.assertIn('needs_bootstrap', self.job(ci, 'onboarding-journey'))

    def test_every_browser_spec_is_classified_once(self):
        text = self.text('nightly-e2e.yml')
        paths = []
        for name in ('GATE_SPECS', 'DRIFT_SPECS', 'EXCLUDED_SPECS'):
            block = re.search(r'^  ' + name + r': >-\n((?:    [^\n]+\n)+)', text, re.M).group(1)
            paths.extend(block.split())
        self.assertEqual(len(paths), len(set(paths)), 'duplicate test bucket membership')
        self.assertEqual(set(paths), {str(p.relative_to(ROOT)) for p in (ROOT / 'e2e').glob('*.spec.ts')})

    def test_image_build_is_native_and_cross_compiles(self):
        dockerfile = (ROOT / 'Dockerfile').read_text()
        self.assertIn('FROM --platform=$BUILDPLATFORM node:', dockerfile)
        self.assertIn('FROM --platform=$BUILDPLATFORM golang:', dockerfile)
        self.assertIn('GOOS=$TARGETOS GOARCH=$TARGETARCH', dockerfile)
        self.assertNotIn('corepack@latest', dockerfile)
        self.assertIn('COPY --from=backend /crewship-sidecar /usr/local/bin/crewship-sidecar', dockerfile)
        self.assertIn('COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh', dockerfile)

    def test_nightly_does_not_publish_from_unverified_push(self):
        text = self.text('nightly.yml')
        self.assertIn('verified_commit.py', text)
        self.assertIn('SOURCE_REPO', text)
        self.assertIn('SOURCE_EVENT', text)
        self.assertIn('Smoke both image platforms before promotion', text)
        self.assertIn('nightly-image-manifest', text)
        self.assertNotIn('ghcr.io/${{ github.repository_owner }}/crewship:nightly\n', text)
        smoke = self.text('nightly-smoke.yml')
        self.assertIn('needs.resolve.outputs.image', smoke)
        self.assertIn('cosign verify --certificate-identity', smoke)
        self.assertNotIn('docker pull "ghcr.io/$OWNER/crewship:nightly"', smoke)


if __name__ == '__main__':
    unittest.main()
