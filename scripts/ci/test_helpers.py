import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('prune', ROOT / 'scripts/ci/prune-nightlies.py')
prune = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prune)


class HelperTests(unittest.TestCase):
    def test_pruning_preserves_stable_legacy_drafts_and_recent_runs(self):
        def release(tag, **kw):
            return dict(tag_name=tag, prerelease=True, draft=False, published_at='2026-08-01T00:00:00Z', **kw)
        releases = [release(f'nightly-aaaaaaaaaaaa-r{i}-a1') for i in range(25)]
        protected = [release('v1.0.0'), release('nightly-20260801-r1')]
        protected += [dict(releases[-1], tag_name='nightly-bbbbbbbbbbbb-r1-a1', draft=True)]
        protected += [dict(releases[-1], tag_name='nightly-cccccccccccc-r1-a1', published_at='2026-09-10T00:00:00Z')]
        expired = prune.expired(releases + protected, datetime(2026, 9, 11, tzinfo=timezone.utc))
        self.assertEqual(len(expired), 6)
        self.assertFalse(set(expired) & {r['tag_name'] for r in protected})
        self.assertEqual(prune.expired(releases[:20], datetime(2026, 9, 11, tzinfo=timezone.utc)), [])

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
