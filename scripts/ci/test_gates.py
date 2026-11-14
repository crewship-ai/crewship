#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from plan import classify, changed_paths, craft_changed, full_matrix, release_changed
from verdict import ALWAYS, CODE, CRAFT, FULL, GO, PARITY, failures
from parity import TESTS, missing_tests


class GateTests(unittest.TestCase):
    def test_api_bootstrap_cannot_be_missing_or_skipped_for_go_change(self):
        self.assertIn('go-race-api-template', GO)
        plan = {'code': 'true', 'go': 'true', 'release': 'true', 'craft': 'true', 'full': 'true', **{key: 'false' for key in PARITY.values()}}
        good = {name: {'result': 'success'} for name in ALWAYS | CODE | GO | CRAFT | {'release-rehearsal'}}
        good.update({name: {'result': 'skipped'} for name in PARITY})
        good['changes']['outputs'] = plan
        self.assertEqual(failures(good), [])
        for result in ['failure', 'cancelled', 'skipped']:
            bad = dict(good, **{'go-race-api-template': {'result': result}})
            self.assertTrue(any('go-race-api-template' in error for error in failures(bad)))
        del good['go-race-api-template']
        self.assertTrue(any('go-race-api-template' in error for error in failures(good)))

    def test_routing(self):
        for paths, expected in [
            (['docs/guide.mdx'], {'code': False, 'go': False, 'docs_inventory': True}),
            (['components/button.tsx', 'docs/guide.mdx'], {'code': True, 'go': False, 'docs_inventory': True}),
            (['internal/api/foo.go'], {'code': True, 'go': True}),
            (['internal/skills/bundled/tool/SKILL.md'], {'code': True, 'go': True}),
            (['pnpm-lock.yaml'], {'code': True, 'go': True}),
            (['.github/workflows/ci.yml'], {'code': True, 'go': True}),
            (['new-runtime/config.toml'], {'code': True, 'go': True}),
            # Release-note fragments are prose like CHANGELOG.md (#4751); only
            # top-level .md files count, anything else there is unknown input.
            (['changelog.d/4751-fragments.md'], {'code': False, 'go': False}),
            (['components/button.tsx', 'changelog.d/4751-fragments.md'], {'code': True, 'go': False}),
            (['changelog.d/nested/4751-fragments.md'], {'code': True, 'go': True}),
            (['changelog.d/4751-fragments.sh'], {'code': True, 'go': True}),
        ]:
            with self.subTest(paths=paths):
                self.assertEqual(classify(paths), {'docs_inventory': False, **expected, 'go_parity': False, 'frontend_parity': False})

    def test_documentation_allowlist_and_unknown_inputs(self):
        for path in ['docs/guide.md', 'docs/guide.mdx', 'docs/llms.txt',
                     'docs/images/example.png', 'docs/images/icon.svg',
                     'docs/images/example.webp', 'docs/images/example.JPG']:
            with self.subTest(path=path):
                self.assertEqual(classify([path]), {
                    'code': False, 'go': False, 'go_parity': False, 'frontend_parity': False,
                    'docs_inventory': path in {'docs/guide.md', 'docs/guide.mdx'},
                })
        for path in ['docs/config.json', 'docs/task.go', 'docs/example.ts',
                     'docs/workflow.yaml', 'docs/check.sh', 'docs/extensionless',
                     'docs/new.unknown', 'docs/guide.mdx.exe']:
            with self.subTest(path=path):
                self.assertTrue(classify([path])['go'])
                self.assertTrue(classify([path])['code'])

    def test_cross_language_parity_routing(self):
        for path in ['docs/configuration/providers.mdx', 'docs/configuration/artifacts.mdx',
                     'docs/api-reference/openapi.mdx',
                     'docs/guides/docker-socket-proxy.mdx', 'lib/crew-icons.ts',
                     'lib/colors.ts', 'lib/notification-categories.ts',
                     'components/features/admin/backups/backups-model.ts']:
            with self.subTest(path=path):
                plan = classify([path])
                self.assertFalse(plan['go'])
                self.assertTrue(plan['go_parity'])
        self.assertEqual(classify(['docs/api-reference/websocket.mdx']), {
            'code': False, 'go': False, 'go_parity': False, 'frontend_parity': True, 'docs_inventory': True,
        })
        # Mixed changes already run the full owning suite; do not add a
        # redundant parity lane or lose evidence when both docs change.
        plan = classify(['internal/api/example.go', 'docs/configuration/providers.mdx'])
        self.assertTrue(plan['go'])
        self.assertFalse(plan['go_parity'])
        plan = classify(['hooks/use-realtime.tsx', 'docs/api-reference/websocket.mdx'])
        self.assertTrue(plan['code'])
        self.assertFalse(plan['frontend_parity'])
        plan = classify(['docs/configuration/providers.mdx', 'docs/api-reference/websocket.mdx'])
        self.assertTrue(plan['go_parity'])
        self.assertTrue(plan['frontend_parity'])

    def test_telemetry_docs_and_cli_inventory_are_not_skipped(self):
        plan = classify(['docs/guides/chat-telemetry.mdx'])
        self.assertTrue(plan['frontend_parity'])
        self.assertTrue(plan['docs_inventory'])
        self.assertFalse(plan['code'])
        for path in ['docs/cli/commands.mdx', 'docs/api-reference/agents.mdx', 'CONTRIBUTING.md']:
            with self.subTest(path=path):
                plan = classify([path])
                self.assertTrue(plan['docs_inventory'])
                self.assertFalse(plan['go'])
        self.assertFalse(classify(['docs/images/icon.svg'])['docs_inventory'])
        self.assertFalse(classify(['README.md'])['docs_inventory'])
        self.assertFalse(classify(['docs/cli/commands.mdx', 'internal/api/example.go'])['docs_inventory'])

    def test_source_rename_to_docs_retains_deleted_input(self):
        # Exercise git, not an invented name-only list: rename detection
        # normally reports only the docs destination for unchanged content.
        with tempfile.TemporaryDirectory() as root:
            def git(*args):
                return subprocess.check_output(['git', '-C', root, *args], text=True).strip()
            git('init', '-q')
            git('config', 'user.name', 'CI routing test')
            git('config', 'user.email', 'ci-routing@example.test')
            Path(root, 'internal').mkdir()
            Path(root, 'internal/source.go').write_text('package source\n')
            git('add', '.')
            git('commit', '-qm', 'source')
            base = git('rev-parse', 'HEAD')
            Path(root, 'docs').mkdir()
            Path(root, 'internal/source.go').rename(Path(root, 'docs/example.md'))
            git('add', '-A')
            git('commit', '-qm', 'rename source to docs')
            head = git('rev-parse', 'HEAD')
            self.assertEqual(git('diff', '--find-renames', '--name-only', f'{base}...{head}'),
                             'docs/example.md')
            previous = os.getcwd()
            try:
                os.chdir(root)
                paths = changed_paths(base, head)
                self.assertEqual(set(paths), {'internal/source.go', 'docs/example.md'})
                self.assertTrue(classify(paths)['go'])
            finally:
                os.chdir(previous)

    def test_parity_verdict_requires_execution_only_when_planned(self):
        for name, flag in PARITY.items():
            for result in ['failure', 'cancelled', 'skipped', None]:
                needs = self.results('false', 'false')
                needs['changes']['outputs'][flag] = 'true'
                needs[name]['result'] = result
                self.assertTrue(failures(needs), (name, result))
            needs[name]['result'] = 'success'
            self.assertEqual(failures(needs), [])
            needs['changes']['outputs'][flag] = 'false'
            self.assertTrue(failures(needs))
            needs[name]['result'] = 'skipped'
            self.assertEqual(failures(needs), [])
            del needs[name]
            self.assertTrue(failures(needs))
            needs = self.results()
            del needs['changes']['outputs'][flag]
            self.assertTrue(failures(needs))

    def test_main_push_and_dispatch_keep_full_suites(self):
        with tempfile.TemporaryDirectory() as root:
            event = Path(root, 'event.json')
            event.write_text('{}')
            for name in ['push', 'workflow_dispatch']:
                output = Path(root, name + '.out')
                environment = {
                    **os.environ, 'GITHUB_EVENT_NAME': name,
                    'GITHUB_EVENT_PATH': str(event), 'GITHUB_OUTPUT': str(output),
                }
                result = json.loads(subprocess.check_output(
                    ['python3', str(Path(__file__).with_name('plan.py'))],
                    env=environment, text=True,
                ))
                self.assertTrue(result['code'])
                self.assertTrue(result['go'])
                self.assertEqual(result['release'], name == 'workflow_dispatch')
                self.assertTrue(result['craft'])
                self.assertFalse(result['go_parity'])
                self.assertFalse(result['frontend_parity'])
                self.assertFalse(result['docs_inventory'])

    def test_parity_named_execution_evidence(self):
        events = [
            {'Package': 'github.com/crewship-ai/crewship/' + path.removeprefix('./'),
             'Test': name, 'Action': 'pass'}
            for path, names in TESTS.items() for name in names
        ]
        self.assertEqual(missing_tests(events), [])
        for index in range(len(events)):
            self.assertTrue(missing_tests(events[:index] + events[index + 1:]))
            changed = [dict(event) for event in events]
            changed[index]['Action'] = 'skip'
            self.assertTrue(missing_tests(changed))
        self.assertTrue(missing_tests([]))

    def results(self, code='true', go='true'):
        needs = {n: {'result': 'success'} for n in ALWAYS | CODE | GO}
        needs['changes']['outputs'] = {'code': code, 'go': go, 'release': 'false', 'craft': 'false', 'full': 'true', 'go_parity': 'false', 'frontend_parity': 'false', 'docs_inventory': 'false'}
        needs.update({name: {'result': 'skipped'} for name in PARITY})
        needs['release-rehearsal'] = {'result': 'skipped'}
        needs.update({name: {'result': 'success' if code == 'true' else 'skipped'} for name in CRAFT})
        for n in CODE if code == 'false' else []:
            needs[n]['result'] = 'skipped'
        for n in GO if go == 'false' else []:
            needs[n]['result'] = 'skipped'
        return needs

    def test_release_rehearsal_is_required_when_planned(self):
        self.assertTrue(release_changed(['.goreleaser.yml']))
        self.assertTrue(release_changed(['packaging/crewship.service']))
        self.assertFalse(release_changed(['docs/guide.md']))
        needs = self.results()
        needs['changes']['outputs']['release'] = 'true'
        self.assertTrue(failures(needs))
        needs['release-rehearsal']['result'] = 'success'
        self.assertEqual(failures(needs), [])

    # Paths the gate drives through (#5362, #5365 review) and the inputs of the
    # retired craft-components-candidate.yml: each is code, so the gate runs.
    GATE_PATHS = [
        'components/features/pages/native-craft-editor-host.tsx', 'components/features/pages/native-craft-lease-bar.tsx',
        'components/features/pages/page-workbook.tsx', 'app/(dashboard)/pages/craft/editor/page.tsx',
        'lib/mooring/channel.ts', 'lib/workbooks/bridge.ts', 'lib/editor-lifecycle/use-leave-guard.ts', 'lib/api-fetch.ts',
        'hooks/use-realtime.ts', 'internal/server/security_headers.go', 'internal/server/craft_native.go',
        'internal/server/editor_origin.go', 'internal/server/editor_selfcheck.go', 'internal/api/craft_native_sessions.go',
        'internal/config/craft_native.go', 'cmd/crewship/cmd_admin_instance_applications.go',
        '.github/actions/setup-playwright-browser/action.yml', 'package.json', 'pnpm-lock.yaml',
        'e2e/acceptance/prd/env.ts', 'e2e/craft-packages/craft-package-gate.spec.ts', 'scripts/ci/craft-package-gate.sh',
        'playwright.craft-packages.config.ts',
        # craft-components-candidate.yml on.pull_request.paths
        '.github/workflows/release.yml', 'tools/craft-components/components.json', 'scripts/build-craft-components.py',
        'scripts/test_build_craft_components.py', 'scripts/build-mooring-runtime.py', 'mooring/build-tools.json',
        'scripts/check-craft-packages.sh', '.goreleaser.yml', 'Dockerfile', 'internal/craftassets/catalog.go',
        'internal/craftapps/policy.go', 'internal/craftengine/wasm.go', 'internal/editororigin/editororigin.go',
        'internal/server/craft_apps.go', 'internal/server/craft_apps_release_packages_test.go',
    ]

    def test_craft_gate_runs_with_every_code_change(self):
        self.assertEqual(CRAFT, {'craft-packages-build', 'craft-packages'})
        for path in self.GATE_PATHS:
            self.assertTrue(classify([path])['code'], path)
        # A host-only change is code: both jobs must succeed although nothing is rebuilt.
        host = ['components/features/pages/native-craft-editor-host.tsx']
        self.assertFalse(craft_changed(host))
        needs = self.results()
        self.assertEqual(failures(needs), [])
        for name in CRAFT:
            for result in ['failure', 'cancelled', 'skipped', None]:
                bad = {k: dict(v) for k, v in needs.items()}
                bad[name]['result'] = result
                self.assertTrue(failures(bad), (name, result))
            bad = self.results()
            del bad[name]
            self.assertTrue(failures(bad), name)
        # Skipped only without code; a plan without the craft key fails closed.
        self.assertEqual(failures(self.results('false', 'false')), [])
        del needs['changes']['outputs']['craft']
        self.assertTrue(failures(needs))

    def test_craft_rebuild_inputs(self):
        for path in ['tools/craft-components/catalog.txt', 'tools/craft-components/patches/gridcraft-wasi.patch',
                     'scripts/build-craft-components.py', 'scripts/build-mooring-runtime.py', 'mooring/build-tools.json',
                     'scripts/check-craft-packages.sh', '.github/workflows/ci.yml', 'scripts/ci/plan.py', 'scripts/ci/verdict.py']:
            self.assertTrue(craft_changed([path]), path)
        self.assertFalse(craft_changed(['components/button.tsx', 'docs/guide.mdx', 'internal/api/foo.go']))

    def craft_repo(self, root):
        def git(*args):
            return subprocess.check_output(['git', *args], cwd=root, text=True).strip()
        git('init', '-q')
        git('config', 'user.email', 'ci@example.invalid')
        git('config', 'user.name', 'CI')
        commits = {}
        for name, path, text in [('base', 'README.md', 'a\n'), ('docs', 'README.md', 'b\n'),
                                 ('pins', 'tools/craft-components/catalog.txt', 'pins\n'),
                                 ('host', 'components/features/pages/native-craft-editor-host.tsx', 'host\n')]:
            Path(root, path).parent.mkdir(parents=True, exist_ok=True)
            Path(root, path).write_text(text)
            git('add', '.')
            git('commit', '-qm', name)
            commits[name] = git('rev-parse', 'HEAD')
        return commits

    def run_plan(self, root, name, event):
        event_path, output = Path(root, 'event.json'), Path(root, 'out')
        event_path.write_text(json.dumps(event))
        return json.loads(subprocess.check_output(
            ['python3', str(Path(__file__).with_name('plan.py'))], cwd=root, text=True,
            env={**os.environ, 'GITHUB_EVENT_NAME': name, 'GITHUB_EVENT_PATH': str(event_path), 'GITHUB_OUTPUT': str(output)}))

    def test_pull_request_and_merge_group_plan_craft_from_their_diff(self):
        with tempfile.TemporaryDirectory() as root:
            c = self.craft_repo(root)
            for name, wrap in [('pull_request', lambda b, h: {'pull_request': {'base': {'sha': b}, 'head': {'sha': h}, 'labels': []}}),
                               ('merge_group', lambda b, h: {'merge_group': {'base_sha': b, 'head_sha': h}})]:
                pins = self.run_plan(root, name, wrap(c['docs'], c['pins']))
                self.assertTrue(pins['craft'], name)
                self.assertTrue(pins['code'], name)
                host = self.run_plan(root, name, wrap(c['pins'], c['host']))
                # Nothing to rebuild, but the host changed: the gate jobs are planned with code.
                self.assertFalse(host['craft'], name)
                self.assertTrue(host['code'], name)
                docs = self.run_plan(root, name, wrap(c['base'], c['docs']))
                self.assertFalse(docs['craft'] or docs['code'], name)

    def test_push_plans_craft_from_its_diff(self):
        with tempfile.TemporaryDirectory() as root:
            c = self.craft_repo(root)
            plan = lambda name, event: self.run_plan(root, name, event)
            # A computable push diff decides; the other suites of a push stay complete.
            quiet = plan('push', {'before': c['base'], 'after': c['docs']})
            self.assertFalse(quiet['craft'])
            self.assertTrue(quiet['code'] and quiet['go'] and quiet['full'])
            self.assertTrue(plan('push', {'before': c['docs'], 'after': c['pins']})['craft'])
            self.assertTrue(plan('push', {'before': c['base'], 'after': c['pins']})['craft'])
            self.assertFalse(plan('push', {'before': c['pins'], 'after': c['host']})['craft'])
            # No computable diff: a new branch, a forced push, an unknown commit, a dispatch.
            for name, event in [('push', {'before': '0' * 40, 'after': c['docs']}),
                                ('push', {'before': c['base'], 'after': c['docs'], 'forced': True}),
                                ('push', {'before': 'f' * 40, 'after': c['docs']}),
                                ('push', {'after': c['docs']}),
                                ('push', {}),
                                ('workflow_dispatch', {'before': c['base'], 'after': c['docs']})]:
                self.assertTrue(plan(name, event)['craft'], (name, event))

    def test_all_actual_mooring_lane_cannot_be_missing_or_not_executed(self):
        self.assertIn('mooring-actual', CODE)
        for result in ['failure', 'cancelled', 'skipped', None]:
            needs = self.results()
            needs['mooring-actual']['result'] = result
            self.assertTrue(failures(needs), result)
        needs = self.results()
        del needs['mooring-actual']
        self.assertTrue(failures(needs))

    def test_mooring_requires_actual_success_for_code_changes(self):
        self.assertIn('mooring', CODE)
        for result in ['failure', 'cancelled', 'skipped', None]:
            needs = self.results()
            needs['mooring']['result'] = result
            self.assertTrue(failures(needs), result)
        needs = self.results()
        del needs['mooring']
        self.assertTrue(failures(needs))

    def test_valid_plans(self):
        for code, go in [('true', 'true'), ('true', 'false'), ('false', 'false')]:
            self.assertEqual(failures(self.results(code, go)), [])

    def test_fail_closed(self):
        for name in ALWAYS | CODE | GO:
            for result in ['failure', 'cancelled', 'skipped', None]:
                needs = self.results()
                needs[name]['result'] = result
                self.assertTrue(failures(needs), (name, result))
            needs = self.results()
            del needs[name]
            self.assertTrue(failures(needs))
        self.assertTrue(failures({}))

    # ── #4752: expensive lanes run in the merge queue, not twice per PR ──
    EXPENSIVE = {'go-platforms', 'go-race', 'go-race-cli-shards', 'go-race-cli',
                 'go-race-api-template', 'go-race-api-shards', 'go-race-api',
                 'cli-subprocess-race', 'go-shuffle', 'mooring-actual'}

    def test_reduced_matrix_is_exactly_the_expensive_lanes(self):
        self.assertEqual(FULL, self.EXPENSIVE)
        self.assertLessEqual(FULL, GO | CODE)
        # Representative evidence stays on every pull request.
        for name in ['go', 'go-test', 'go-lint', 'frontend', 'frontend-test', 'frontend-test-shards',
                     'mooring', 'binary', 'image', 'harness-pr', 'playwright-pr', 'managed-environments']:
            self.assertNotIn(name, FULL)

    def test_full_matrix_routing(self):
        for event in ['merge_group', 'push', 'workflow_dispatch', None, 'pull_request_target']:
            with self.subTest(event=event):
                self.assertTrue(full_matrix(event, ['internal/api/foo.go'], []))
        self.assertFalse(full_matrix('pull_request', ['internal/api/foo.go'], []))
        self.assertFalse(full_matrix('pull_request', ['components/a.tsx'], ['skip-changelog']))
        self.assertTrue(full_matrix('pull_request', ['internal/api/foo.go'], ['full-ci']))
        for path in ['.github/workflows/ci.yml', 'scripts/ci/verdict.py', '.github/actions/setup-go-embed/action.yml',
                     'go.mod', 'go.sum', 'pnpm-lock.yaml', 'Dockerfile']:
            with self.subTest(path=path):
                self.assertTrue(full_matrix('pull_request', [path], []))

    def test_plan_reads_pull_request_labels(self):
        with tempfile.TemporaryDirectory() as root:
            def git(*args):
                return subprocess.check_output(['git', '-C', root, *args], text=True).strip()
            git('init', '-q')
            git('config', 'user.name', 'CI routing test')
            git('config', 'user.email', 'ci-routing@example.test')
            Path(root, 'internal').mkdir()
            Path(root, 'internal/a.go').write_text('package a\n')
            git('add', '.')
            git('commit', '-qm', 'base')
            base = git('rev-parse', 'HEAD')
            Path(root, 'internal/a.go').write_text('package a\n// change\n')
            git('commit', '-qam', 'head')
            head = git('rev-parse', 'HEAD')
            for labels, expected in [([], False), ([{'name': 'ci/cd'}], False), ([{'name': 'full-ci'}], True)]:
                event = Path(root, 'event.json')
                event.write_text(json.dumps({'pull_request': {'base': {'sha': base}, 'head': {'sha': head}, 'labels': labels}}))
                output = Path(root, 'out')
                result = json.loads(subprocess.check_output(
                    ['python3', str(Path(__file__).with_name('plan.py'))], cwd=root, text=True,
                    env={**os.environ, 'GITHUB_EVENT_NAME': 'pull_request',
                         'GITHUB_EVENT_PATH': str(event), 'GITHUB_OUTPUT': str(output)}))
                self.assertIs(result['full'], expected, labels)
                self.assertIn(f'full={str(expected).lower()}', output.read_text())

    def test_pull_request_may_skip_only_the_expensive_lanes(self):
        needs = self.results()
        needs['changes']['outputs']['full'] = 'false'
        for name in FULL:
            needs[name]['result'] = 'skipped'
        self.assertEqual(failures(needs, 'pull_request'), [])
        # Everything outside the expensive set is still required on the PR.
        for name in (ALWAYS | CODE | GO) - FULL:
            for result in ['failure', 'cancelled', 'skipped', None]:
                bad = {k: dict(v) for k, v in needs.items()}
                bad[name]['result'] = result
                self.assertTrue(failures(bad, 'pull_request'), (name, result))
        # A reduced plan does not tolerate a failed expensive lane that ran.
        for name in FULL:
            bad = {k: dict(v) for k, v in needs.items()}
            bad[name]['result'] = 'failure'
            self.assertTrue(failures(bad, 'pull_request'), name)

    def test_merge_queue_fails_closed_on_reduced_or_skipped_matrix(self):
        for event in ['merge_group', 'push', 'workflow_dispatch', None]:
            reduced = self.results()
            reduced['changes']['outputs']['full'] = 'false'
            for name in FULL:
                reduced[name]['result'] = 'skipped'
            self.assertTrue(failures(reduced, event), event)
            for name in FULL:
                for result in ['failure', 'cancelled', 'skipped', None]:
                    needs = self.results()
                    needs[name]['result'] = result
                    self.assertTrue(failures(needs, event), (event, name, result))
                needs = self.results()
                del needs[name]
                self.assertTrue(failures(needs, event), (event, name))
            self.assertEqual(failures(self.results(), event), [])
        for value in [None, '', 'yes']:
            needs = self.results()
            if value is None:
                del needs['changes']['outputs']['full']
            else:
                needs['changes']['outputs']['full'] = value
            self.assertTrue(failures(needs, 'pull_request'), value)
            self.assertTrue(failures(needs, 'merge_group'), value)

    def test_verdict_entrypoint_without_event_name_fails_closed(self):
        # The script must not assume a pull_request when the runner omits
        # GITHUB_EVENT_NAME: a reduced plan then fails the CI Result.
        reduced = self.results()
        reduced['changes']['outputs']['full'] = 'false'
        for name in FULL:
            reduced[name]['result'] = 'skipped'
        script = Path(__file__).with_name('verdict.py')
        env = {k: v for k, v in os.environ.items() if k != 'GITHUB_EVENT_NAME'}
        for event, needs, code in [(None, reduced, 1), ('merge_group', reduced, 1),
                                   ('pull_request', reduced, 0), (None, self.results(), 0)]:
            run_env = dict(env, CI_NEEDS=json.dumps(needs))
            if event:
                run_env['GITHUB_EVENT_NAME'] = event
            result = subprocess.run(['python3', str(script)], env=run_env, capture_output=True, text=True)
            self.assertEqual(result.returncode, code, (event, result.stdout))


if __name__ == '__main__':
    unittest.main()
