#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from plan import classify, changed_paths, release_changed
from verdict import ALWAYS, CODE, GO, PARITY, failures
from parity import TESTS, missing_tests


class GateTests(unittest.TestCase):
    def test_routing(self):
        for paths, expected in [
            (['docs/guide.mdx'], {'code': False, 'go': False}),
            (['components/button.tsx', 'docs/guide.mdx'], {'code': True, 'go': False}),
            (['internal/api/foo.go'], {'code': True, 'go': True}),
            (['internal/skills/bundled/tool/SKILL.md'], {'code': True, 'go': True}),
            (['pnpm-lock.yaml'], {'code': True, 'go': True}),
            (['.github/workflows/ci.yml'], {'code': True, 'go': True}),
            (['new-runtime/config.toml'], {'code': True, 'go': True}),
        ]:
            with self.subTest(paths=paths):
                self.assertEqual(classify(paths), {**expected, 'go_parity': False, 'frontend_parity': False})

    def test_documentation_allowlist_and_unknown_inputs(self):
        for path in ['docs/guide.md', 'docs/guide.mdx', 'docs/llms.txt',
                     'docs/images/example.png', 'docs/images/icon.svg',
                     'docs/images/example.webp', 'docs/images/example.JPG']:
            with self.subTest(path=path):
                self.assertEqual(classify([path]), {
                    'code': False, 'go': False, 'go_parity': False, 'frontend_parity': False,
                })
        for path in ['docs/config.json', 'docs/task.go', 'docs/example.ts',
                     'docs/workflow.yaml', 'docs/check.sh', 'docs/extensionless',
                     'docs/new.unknown', 'docs/guide.mdx.exe']:
            with self.subTest(path=path):
                self.assertTrue(classify([path])['go'])
                self.assertTrue(classify([path])['code'])

    def test_cross_language_parity_routing(self):
        for path in ['docs/configuration/providers.mdx', 'lib/crew-icons.ts',
                     'lib/colors.ts', 'lib/notification-categories.ts',
                     'components/features/admin/backups/backups-model.ts']:
            with self.subTest(path=path):
                plan = classify([path])
                self.assertFalse(plan['go'])
                self.assertTrue(plan['go_parity'])
        self.assertEqual(classify(['docs/api-reference/websocket.mdx']), {
            'code': False, 'go': False, 'go_parity': False, 'frontend_parity': True,
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
                self.assertFalse(result['go_parity'])
                self.assertFalse(result['frontend_parity'])

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
        needs['changes']['outputs'] = {'code': code, 'go': go, 'release': 'false', 'go_parity': 'false', 'frontend_parity': 'false'}
        needs.update({name: {'result': 'skipped'} for name in PARITY})
        needs['release-rehearsal'] = {'result': 'skipped'}
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


if __name__ == '__main__':
    unittest.main()
