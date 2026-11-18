import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('windows_custody', Path(__file__).with_name('windows-sdk-custody.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class WindowsCustodyEvidenceTests(unittest.TestCase):
    def passing(self):
        return [{'Package': module.PACKAGE, 'Test': name, 'Action': action}
                for name in module.REQUIRED for action in ('run', 'pass')]

    def test_requires_every_parent_and_named_case(self):
        self.assertEqual(module.failures(self.passing(), 0), [])
        for name in module.REQUIRED:
            with self.subTest(name=name):
                events = [event for event in self.passing() if event['Test'] != name]
                self.assertTrue(module.failures(events, 0))

    def test_skip_or_failure_cannot_be_overridden_by_parent_pass(self):
        name = 'TestAgentCLIWindowsRejectsCustodyChanges/source-hardlink'
        for action in ('skip', 'fail'):
            self.assertTrue(module.failures(self.passing() + [
                {'Package': module.PACKAGE, 'Test': name, 'Action': action}], 0))

    def test_another_package_and_nonzero_exit_cannot_qualify(self):
        self.assertTrue(module.failures([dict(event, Package='wrong') for event in self.passing()], 0))
        self.assertTrue(module.failures(self.passing(), 1))


if __name__ == '__main__':
    unittest.main()
