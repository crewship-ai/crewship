import importlib.util
from pathlib import Path
import unittest

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


if __name__ == '__main__':
    unittest.main()
