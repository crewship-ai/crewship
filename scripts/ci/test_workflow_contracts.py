"""Architecture invariants supplement actionlint's syntax/type validation."""
from pathlib import Path
import re
import json
import os
import subprocess
import tempfile
import textwrap
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
        self.assertIn('nextjs-static-export', self.job(ci, 'playwright-pr'))
        self.assertIn('path: out', self.job(ci, 'playwright-pr'))
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

    def test_superseded_nightly_skips_both_public_smoke_jobs(self):
        smoke = self.text('nightly-smoke.yml')
        for name in ('binary', 'docker'):
            self.assertIn("if: needs.resolve.outputs.published == 'true'", self.job(smoke, name))
        nightly = self.text('nightly.yml')
        self.assertIn('Prevent superseded binaries from advancing self-update', nightly)
        publication = self.step(nightly, 'Publish immutable nightly release')
        promotion = self.step(nightly, 'Promote only the verified image')
        self.assertIn("if: steps.current.outputs.publish == 'true'", publication)
        self.assertIn("if: steps.publication.outputs.published == 'true'", promotion)

    def step(self, text, name):
        return re.search(r'^      - name: ' + re.escape(name) + r'\n(.*?)(?=^      - |\Z)', text, re.M | re.S).group(1)

    def test_nightly_upload_rechecks_main_before_publication(self):
        step = self.step(self.text('nightly.yml'), 'Publish immutable nightly release')
        script = textwrap.dedent(re.search(r'        run: \|\n(.*?)(?=^        if:)', step, re.M | re.S).group(1))
        for current, api_status in [('a' * 40, 0), ('b' * 40, 0), ('', 1)]:
            with self.subTest(current=current, api_status=api_status), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                (root / 'dist').mkdir()
                manifest = root / 'dist/image-manifest.json'
                manifest.write_text(json.dumps({'published': True}))
                fake = root / 'gh'
                fake.write_text('#!/bin/bash\nprintf "%s\\n" "$*" >> "$COMMAND_LOG"\n'
                                'if [ "$1" = api ]; then echo "$CURRENT"; exit "$API_STATUS"; fi\n')
                fake.chmod(0o755)
                env = dict(os.environ, PATH=f'{root}:' + os.environ['PATH'],
                           SHA='a' * 40, CURRENT=current, API_STATUS=str(api_status),
                           VERSION='nightly-20260911-r123401', REPO='example/repo',
                           RUNNER_TEMP=temp, GITHUB_OUTPUT=str(root / 'outputs'),
                           COMMAND_LOG=str(root / 'commands'))
                result = subprocess.run(['bash', '-c', script], cwd=root, env=env, capture_output=True, text=True)
                commands = (root / 'commands').read_text().splitlines()
                self.assertIn('--draft --prerelease', commands[0])
                self.assertTrue(commands[1].startswith('api '))
                published = current == env['SHA'] and api_status == 0
                self.assertEqual(any(c.startswith('release edit ') for c in commands), published)
                if api_status:
                    self.assertNotEqual(result.returncode, 0)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(json.loads(manifest.read_text())['published'], published)
                    self.assertIn(f'published={str(published).lower()}', (root / 'outputs').read_text())
                    self.assertEqual(any(c.startswith('release delete ') for c in commands), not published)

    def test_release_rejects_invalid_image_tags_before_build(self):
        step = self.step(self.text('release.yml'), 'Require verified main ancestry and checks')
        validation = textwrap.dedent(step.split('        run: |\n', 1)[1].split('          git merge-base', 1)[0])
        for tag, valid in [('v1.2.3', True), ('v1.2.3-rc.1', True),
                           ('v1.2.3+build.1', False), ('v1.2.3-rc.1+build', False),
                           ('v1.2.3-' + 'a' * 128, False), ('nightly', False)]:
            with self.subTest(tag=tag):
                result = subprocess.run(['bash', '-c', validation], env=dict(os.environ, GITHUB_REF_NAME=tag))
                self.assertEqual(result.returncode == 0, valid)

    def test_snapshot_rehearsal_does_not_parse_the_latest_nightly_tag(self):
        job = self.job(self.text('ci.yml'), 'release-rehearsal')
        self.assertIn('GORELEASER_CURRENT_TAG: v0.0.0-ci', job)

    def test_both_publishers_scan_final_binary_artifacts(self):
        for workflow, job in [('nightly.yml', 'binaries'), ('release.yml', 'release')]:
            block = self.job(self.text(workflow), job)
            self.assertRegex(block, r'path: dist\n +fail-build: true\n +severity-cutoff: critical')

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
