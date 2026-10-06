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

    def test_osv_covers_every_shipped_npm_lockfile(self):
        security = self.text('security.yml')
        osv = self.job(security, 'osv-scan')
        scanned = set(re.findall(r'--lockfile=([^\s\\]+)', osv))
        # Both the application and its isolated Pages compiler ship npm code.
        # Discover their locks so a future nested compiler cannot silently lose
        # scanning when someone edits the workflow.
        shipped = {'pnpm-lock.yaml'} | {
            str(p.relative_to(ROOT)) for p in (ROOT / 'tools' / 'pages-build').rglob('pnpm-lock.yaml')
            if 'node_modules' not in p.parts
        }
        self.assertTrue(shipped - {'pnpm-lock.yaml'})
        self.assertLessEqual(shipped | {'go.mod'}, scanned)
        router = self.job(security, 'changes')
        for pattern in ('**/package.json', '**/pnpm-lock.yaml', '**/pnpm-workspace.yaml'):
            self.assertIn("'" + pattern + "'", router)
    def test_cli_subprocess_race_instruments_the_actual_artifact(self):
        ci = self.text('ci.yml')
        lane = self.job(ci, 'cli-subprocess-race')
        self.assertIn('python3 scripts/ci/cli-subprocess-race.py', lane)
        self.assertNotIn('continue-on-error:', lane)
        self.assertIn('include-hidden-files: true', lane)
        self.assertIn('if-no-files-found: error', lane)
        helper = (ROOT / 'cmd/crewship/cmd_model_test.go').read_text()
        self.assertIn('TEST_CREWSHIP_CLI_RACE', helper)
        self.assertIn('args = append(args, "-race")', helper)
        conversation = (ROOT / 'cmd/crewship/cmd_conversation_test.go').read_text()
        self.assertIn('return buildCrewshipBinary(t)', conversation)
        self.assertNotIn('exec.Command("go", "build"', conversation)
    def test_actual_tree_passes_reusable_image_gate(self):
        result = subprocess.run(['bash', 'scripts/pr-image-build-paths.sh'],
                                cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Reusable image build is required by CI Result', result.stdout)

    def test_cross_language_parity_lanes_remain_required(self):
        from parity import TESTS
        ci = self.text('ci.yml')
        changes = self.job(ci, 'changes')
        for name, flag in [('go-parity', 'go_parity'), ('frontend-parity', 'frontend_parity'), ('docs-inventory', 'docs_inventory')]:
            self.assertIn(f'{flag}: ${{{{ steps.plan.outputs.{flag} }}}}', changes)
            job = self.job(ci, name)
            self.assertIn(f"if: needs.changes.outputs.{flag} == 'true'", job)
            self.assertNotIn('continue-on-error', job)
        self.assertIn('python3 scripts/ci/parity.py', self.job(ci, 'go-parity'))
        self.assertIn('go run ./scripts/docs-inventory -strict', self.job(ci, 'docs-inventory'))
        self.assertIn('lib/__tests__/telemetry-call-sites.test.ts', self.job(ci, 'frontend-parity'))
        self.assertIn('vitest run hooks/__tests__/realtime-allowlist-docs-parity.test.ts',
                      self.job(ci, 'frontend-parity'))
        # Renaming/deleting a contract requires updating its runner, rather
        # than silently selecting zero tests with Go's successful exit code.
        for path, names in TESTS.items():
            source = '\n'.join(p.read_text() for p in (ROOT / path).glob('*_test.go'))
            for name in names:
                self.assertRegex(source, r'func ' + re.escape(name) + r'\(t \*testing.T\)')

    def test_test_files_reading_docs_have_routing_contracts(self):
        from plan import GO_DOC_TESTS, FRONTEND_DOC_TESTS
        declared = {(doc, test) for mapping in [GO_DOC_TESTS, FRONTEND_DOC_TESTS]
                    for doc, test in mapping.items()}
        discovered = set()
        # Conservative literal discovery covers direct fs reads and constants
        # passed to reads. New computed paths need an explicit reviewed map.
        for root in ['cmd', 'internal', 'app', 'components', 'hooks', 'lib', 'stores', 'scripts']:
            for path in (ROOT / root).rglob('*'):
                if not path.name.endswith(('_test.go', '.test.ts', '.test.tsx')):
                    continue
                source = path.read_text()
                if not re.search(r'os\.ReadFile\(|readFileSync\(|readRepoFile\(', source):
                    continue
                if root == 'scripts':
                    # Script tests construct synthetic docFile/report records and
                    # write temporary pages, sometimes using real page names.
                    # Discover read arguments and named doc constants instead;
                    # constants may live in a sibling package source file.
                    package_source = '\n'.join(p.read_text() for p in path.parent.glob('*.go'))
                    constants = re.findall(r'const\s+(\w+)\s*=\s*[\"\'](docs/[^\"\']+\.mdx?)[\"\']', package_source)
                    docs = {doc for name, doc in constants if re.search(r'\b' + re.escape(name) + r'\b', source)}
                    reads = re.findall(r'(?:os\.ReadFile|readRepoFile)\([^\n]+', source)
                    for read in reads:
                        docs.update(re.findall(r'[\"\'](?:\.\./)*(docs/[^\"\']+\.mdx?)[\"\']', read, re.I))
                else:
                    docs = re.findall(r'[\"\'](?:\.\./)*(docs/[^\"\']+\.mdx?)[\"\']', source, re.I)
                for doc in docs:
                    discovered.add((doc, path.relative_to(ROOT).as_posix()))
        # This literal is synthetic JSON in a seed-pack fixture; its fs read
        # targets a Python script. There is no product document to route.
        fixture = ('docs/x.mdx', 'cmd/crewship/seeddata/packs_test.go')
        self.assertIn(fixture, discovered)
        self.assertFalse((ROOT / fixture[0]).exists())
        discovered.remove(fixture)
        self.assertEqual(discovered, declared, 'Map new test-read documentation into its required parity lane')
        frontend = self.job(self.text('ci.yml'), 'frontend-parity')
        for path in FRONTEND_DOC_TESTS.values():
            self.assertIn(path, frontend)

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

    def test_race_partition_workers_and_complete_evidence_are_required(self):
        ci = self.text('ci.yml')
        for job, indices in [('go-race', '[0, 1]'), ('go-race-api-shards', '[0, 1, 2, 3]')]:
            worker = self.job(ci, job)
            self.assertIn('fail-fast: false', worker)
            self.assertIn('shard: ' + indices, worker)
            self.assertNotIn('continue-on-error:', worker)
            self.assertIn('${{ matrix.shard }}-${{ github.sha }}', worker)
        api = self.job(ci, 'go-race-api')
        self.assertIn('name: Go Race (internal/api)', api)
        self.assertIn('needs: [changes, go-race-api-shards]', api)
        self.assertIn('test "$SHARD_RESULT" = success', api)
        self.assertIn('api-race-shard.py report .ci-api-shards 4 2300', api)
        self.assertNotIn('merge-multiple: true', api)
        self.assertIn('path: .ci-api-shards/', api)
        self.assertIn('if: always()', api)
        workers = self.job(ci, 'go-race-api-shards')
        baseline = int(re.search(r'RACE_API_BASELINE_SECONDS: "(\d+)"', workers).group(1))
        count, report_baseline = map(int, re.search(r'api-race-shard.py report \.ci-api-shards (\d+) (\d+)', api).groups())
        self.assertEqual(baseline, report_baseline)
        self.assertEqual(count, 4)
        cap = int(re.search(r'timeout-minutes: (\d+)', workers).group(1))
        env_cap = int(re.search(r'JOB_CAP_MINUTES: "(\d+)"', workers).group(1))
        overhead = int(re.search(r'JOB_OVERHEAD_MINUTES: "(\d+)"', workers).group(1))
        self.assertEqual(cap, env_cap)
        self.assertGreater(cap * 60, baseline * 2 + overhead * 60)

    def test_managed_environment_lane_is_required_and_credential_free(self):
        ci = self.text('ci.yml')
        lane = self.job(ci, 'managed-environments')
        self.assertIn("if: needs.changes.outputs.go == 'true'", lane)
        self.assertIn('python3 scripts/ci/managed-environments.py', lane)
        self.assertIn('docker pull alpine@sha256:', lane)
        self.assertNotIn('secrets.', lane)
        self.assertIn('managed-environments', self.job(ci, 'result'))

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
                self.assertEqual(commands[1], 'api repos/example/repo/commits/main --jq .sha')
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

    def test_required_go_lint_preserves_shipped_migrations(self):
        job = self.job(self.text('ci.yml'), 'go-lint')
        self.assertIn('go run ./scripts/lint-migrations "$BASE_SHA"', job)
        self.assertIn('github.event.merge_group.base_sha', job)
        self.assertIn('github.event.before', job)
        self.assertIn('fetch-depth: 0', job)

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
