"""Harness fault-injection tests; no real server execution is claimed here."""
import importlib.util
from pathlib import Path
from types import SimpleNamespace
import unittest

spec = importlib.util.spec_from_file_location("routines_live_contracts", Path(__file__).with_name("routines-live-contracts.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CleanupFailureTests(unittest.TestCase):
    def test_cleanup_attempts_all_resources_after_cancel_failure(self):
        self.check_cleanup("cancel")

    def test_cleanup_removes_credentials_after_logout_failure(self):
        self.check_cleanup("logout")

    def check_cleanup(self, failure):
        suite = module.Suite.__new__(module.Suite)
        suite.workspace = "fixture-workspace"
        suite.prefix = "fixture-prefix"
        suite.deferred = ["fixture-pending"]
        suite.pending = ["fixture-run"]
        suite.member_config = Path("unused-fixture-config")
        suite.extra_workspaces = [("fixture-foreign", "fixture-foreign-prefix")]
        attempts = []
        suite.member_tmp = SimpleNamespace(cleanup=lambda: attempts.append("credentials removed"))

        def api(method, path, *args):
            attempts.append(path)
            if failure == "cancel" and "/pending/" in path:
                raise AssertionError("injected cancellation failure")

        def cli(*args, **kwargs):
            attempts.append(args)
            return SimpleNamespace(returncode=int(failure == "logout" and args[0] == "logout"))

        suite.api, suite.cli = api, cli
        with self.assertRaises(AssertionError):
            suite.cleanup()
        self.assertIn(("workspace", "delete", "fixture-foreign", "--confirm", "fixture-foreign-prefix", "--yes"), attempts)
        self.assertIn(("workspace", "delete", "fixture-workspace", "--confirm", "fixture-prefix", "--yes"), attempts)
        self.assertIn("credentials removed", attempts)
        self.assertTrue(any(isinstance(x, str) and "/runs/fixture-run/cancel" in x for x in attempts))


if __name__ == "__main__":
    unittest.main()
