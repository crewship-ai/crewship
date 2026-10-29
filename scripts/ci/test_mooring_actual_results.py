"""No compiler/child mocking here: qualify the runner's result evidence only."""

import importlib.util
import json
import subprocess
import tempfile
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    "mooring_actual_results", Path(__file__).with_name("mooring-actual-results.py")
)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ActualResultsTests(unittest.TestCase):
    package = "github.com/crewship-ai/crewship/internal/api"

    def events(self, *pairs):
        return [json.dumps(dict(Package=self.package, Action=action,
                                **({"Test": name} if name else {})))
                for action, name in pairs]

    def valid(self):
        return self.events(("run", "TestActual"), ("run", "TestActual/reader"),
                           ("pass", "TestActual/reader"), ("pass", "TestActual"),
                           ("pass", None))

    def test_real_go_json_compile_and_test_events(self):
        # This is a tiny evidence-parser fixture, not a Mooring integration pass.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text("module ci.example/actual\n\ngo 1.27.3\n")
            source = root / "actual_test.go"
            source.write_text('package actual\nimport "testing"\nfunc TestActual(t *testing.T) {}\n')
            command = ["go", "test", "-json", "-count=1", "-timeout=1m", "."]
            passed = subprocess.run(command, cwd=root, text=True, capture_output=True, check=False)
            self.assertEqual(passed.returncode, 0, passed.stderr + passed.stdout)
            evidence = module.verify(passed.stdout.splitlines(), "ci.example/actual", ["TestActual"])
            self.assertEqual(evidence["executed"], ["TestActual"])
            source.write_text('package actual\nimport "testing"\nfunc TestActual(t *testing.T) { undefinedFixtureCall() }\n')
            failed = subprocess.run(command, cwd=root, text=True, capture_output=True, check=False)
            self.assertNotEqual(failed.returncode, 0)
            with self.assertRaises(ValueError):
                module.verify(failed.stdout.splitlines(), "ci.example/actual", ["TestActual"])

    def test_actual_completion_including_subtest(self):
        result = module.verify(self.valid(), self.package, ["TestActual"])
        self.assertEqual(result["executed"], ["TestActual"])
        self.assertEqual(result["skipped"], 0)

    def test_failures_cannot_become_green(self):
        cases = {
            "zero": self.events(("pass", None)),
            "skip": self.events(("run", "TestActual"), ("skip", "TestActual"),
                                ("pass", None)),
            "subtest_skip": self.valid() + self.events(("skip", "TestActual/reader")),
            "failure": self.valid() + self.events(("fail", "TestActual")),
            "truncated": self.valid()[:-1],
            "duplicate": self.valid() + self.valid(),
            "unexpected": self.valid() + self.events(("run", "TestOther")),
            "wrong_package": [json.dumps({"Package": "other", "Action": "pass"})],
            "malformed": ["not JSON"],
        }
        for reason, events in cases.items():
            with self.subTest(reason=reason), self.assertRaises(ValueError):
                module.verify(events, self.package, ["TestActual"])

    def test_empty_or_duplicate_expectations_fail(self):
        for expected in ([], ["TestActual", "TestActual"]):
            with self.assertRaises(ValueError):
                module.verify(self.valid(), self.package, expected)


if __name__ == "__main__":
    unittest.main()
