import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch
import contextlib
import io
import os
import tempfile

spec = importlib.util.spec_from_file_location('cli_race', Path(__file__).with_name('cli-subprocess-race.py'))
lane = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lane)


class SubprocessRaceEvidence(unittest.TestCase):
    def events(self):
        return [dict(Package=lane.PACKAGE, Test=name, Action='pass') for name in lane.TESTS]

    def test_every_named_boundary_must_pass(self):
        self.assertEqual(lane.missing_tests(self.events()), [])
        for name in lane.TESTS:
            for outcome in ('skip', 'fail', 'run'):
                with self.subTest(test=name, outcome=outcome):
                    events = self.events()
                    next(e for e in events if e['Test'] == name)['Action'] = outcome
                    self.assertEqual(lane.missing_tests(events), [name])

    def test_no_tests_and_wrong_package_fail_closed(self):
        self.assertEqual(lane.missing_tests([]), sorted(lane.TESTS))
        events = self.events()
        for event in events:
            event['Package'] = 'another/package'
        self.assertEqual(lane.missing_tests(events), sorted(lane.TESTS))

    def test_child_race_reddens_successful_outer_test_events(self):
        for fresh_race in (False, True):
            with self.subTest(fresh_race=fresh_race), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                logs = root / '.ci-results' / 'cli-subprocess-race'
                logs.mkdir(parents=True)
                (logs / 'race.old').write_text('stale prior run')
                class Process:
                    stdout = [__import__('json').dumps(event) + '\n' for event in self.events()]
                    def wait(self):
                        return 0  # Negative acceptance tests swallowed the child failure.
                def start(*args, **kwargs):
                    self.assertIn('log_path=' + str(logs / 'race'), kwargs['env']['GORACE'])
                    self.assertFalse((logs / 'race.old').exists())
                    if fresh_race:
                        (logs / 'race.123').write_text('WARNING: DATA RACE')
                    return Process()
                previous = Path.cwd()
                try:
                    os.chdir(root)
                    with patch.object(lane.subprocess, 'Popen', side_effect=start), contextlib.redirect_stdout(io.StringIO()):
                        self.assertEqual(bool(lane.main()), fresh_race)
                finally:
                    os.chdir(previous)


if __name__ == '__main__':
    unittest.main()
