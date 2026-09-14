#!/usr/bin/env python3
import unittest
from plan import classify, release_changed
from verdict import ALWAYS, CODE, GO, failures


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
                self.assertEqual(classify(paths), expected)

    def results(self, code='true', go='true'):
        needs = {n: {'result': 'success'} for n in ALWAYS | CODE | GO}
        needs['changes']['outputs'] = {'code': code, 'go': go, 'release': 'false'}
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
