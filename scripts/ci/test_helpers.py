import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('prune', ROOT / 'scripts/ci/prune-nightlies.py')
prune = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prune)

homebrew_spec = importlib.util.spec_from_file_location('homebrew', ROOT / 'scripts/ci/publish-homebrew.py')
homebrew = importlib.util.module_from_spec(homebrew_spec)
homebrew_spec.loader.exec_module(homebrew)

version_spec = importlib.util.spec_from_file_location('nightly_version', ROOT / 'scripts/ci/nightly-version.py')
nightly_version = importlib.util.module_from_spec(version_spec)
version_spec.loader.exec_module(nightly_version)


class HelperTests(unittest.TestCase):
    def test_nightly_versions_keep_old_client_grammar_and_order(self):
        first = nightly_version.version(1234, 1, '20260911')
        self.assertEqual(first, 'nightly-20260911-r123401')
        self.assertRegex(first, r'^nightly-[0-9]{8}-r[0-9]+$')
        self.assertLess(int(first.rsplit('r', 1)[1]), int(nightly_version.version(1234, 2, '20260911').rsplit('r', 1)[1]))
        self.assertLess(123499, int(nightly_version.version(1235, 1, '20260911').rsplit('r', 1)[1]))
        for run, attempt in [(0, 1), (1, 0), (1, 100), (21474836, 1)]:
            with self.assertRaises(ValueError): nightly_version.version(run, attempt, '20260911')

    def test_pruning_preserves_stable_rolling_drafts_and_recent_runs(self):
        def release(tag, **kw):
            return dict(tag_name=tag, prerelease=True, draft=False, published_at='2026-08-01T00:00:00Z', **kw)
        releases = [release(f'nightly-20260801-r{i}') for i in range(25)]
        protected = [release('v1.0.0'), release('nightly')]
        protected += [dict(releases[-1], tag_name='nightly-20260802-r1', draft=True)]
        protected += [dict(releases[-1], tag_name='nightly-20260910-r1', published_at='2026-09-10T00:00:00Z')]
        expired = prune.expired(releases + protected, datetime(2026, 9, 11, tzinfo=timezone.utc))
        self.assertEqual(len(expired), 6)
        self.assertFalse(set(expired) & {r['tag_name'] for r in protected})
        self.assertEqual(prune.expired(releases[:20], datetime(2026, 9, 11, tzinfo=timezone.utc)), [])

    def test_homebrew_commits_both_formulas_without_force(self):
        responses = [{'default_branch': 'main'}, {'object': {'sha': 'old'}},
                     {'tree': {'sha': 'base'}}, {'sha': 'tree'}, {'sha': 'commit'}]
        with patch.object(homebrew, 'api', side_effect=responses) as api, \
             patch.object(homebrew.Path, 'read_text', return_value='url "https://github.com/example/releases/download/v1.2.3/archive"'), \
             patch.object(homebrew.subprocess, 'run') as update, \
             patch('sys.argv', ['publish-homebrew.py', 'v1.2.3']):
            homebrew.main()
        tree = api.call_args_list[3].args[1]
        self.assertEqual({e['path'] for e in tree['tree']}, {'Formula/crewship.rb', 'Formula/crewship-cli.rb'})
        self.assertEqual(api.call_args_list[4].args[1]['parents'], ['old'])
        self.assertEqual(json.loads(update.call_args.kwargs['input']), {'sha': 'commit', 'force': False})
        self.assertIn('PATCH', update.call_args.args[0])

    def test_homebrew_rejects_wrong_artifact_before_any_write(self):
        with patch.object(homebrew, 'api', side_effect=[{'default_branch': 'main'}, {'object': {'sha': 'old'}}, {'tree': {'sha': 'base'}}]) as api, \
             patch.object(homebrew.Path, 'read_text', return_value='/download/v9.9.9/archive'), \
             patch('sys.argv', ['publish-homebrew.py', 'v1.2.3']), self.assertRaises(SystemExit):
            homebrew.main()
        self.assertTrue(all(len(call.args) == 1 for call in api.call_args_list))

    def test_reporter_does_not_turn_failing_go_into_success(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fake_go = root / 'go'
            fake_go.write_text('#!/bin/sh\necho \'{"Action":"fail","Package":"example","Elapsed":1.5}\'\nexit 7\n')
            fake_go.chmod(0o755)
            env = dict(os.environ, PATH=f'{root}:' + os.environ['PATH'], CI_RESULTS_DIR=str(root / 'results'), GITHUB_STEP_SUMMARY=str(root / 'summary'))
            result = subprocess.run(['bash', str(ROOT / 'scripts/ci/go-test.sh'), './...'], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 7, result.stderr)
            timings = json.loads((root / 'results/timings.json').read_text())
            self.assertEqual(timings['timings'][0]['Action'], 'fail')


if __name__ == '__main__':
    unittest.main()
