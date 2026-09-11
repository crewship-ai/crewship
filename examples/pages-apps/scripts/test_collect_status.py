import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("collector", Path(__file__).with_name("collect_status.py"))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)

class CollectorTests(unittest.TestCase):
    def test_missing_configuration_never_reports_success(self):
        with patch.dict(os.environ, {}, clear=True):
            for kind in ["mysql", "ansible"]:
                payload, failed = collector.collect(kind)
                self.assertTrue(failed)
                self.assertEqual(payload["items"][0]["state"], "warning")

    def test_authenticated_query_and_no_diagnostic_leak(self):
        with patch.dict(os.environ, {"MYSQL_DEFAULTS_FILE": "/run/credentials/mysql.cnf"}, clear=True):
            with patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "password=secret")) as run:
                payload, failed = collector.collect("mysql")
                self.assertTrue(failed)
                self.assertNotIn("secret", json.dumps(payload))
                self.assertIn("--execute=SELECT 1", run.call_args.args[0])
                self.assertEqual(run.call_args.kwargs["stdout"], subprocess.DEVNULL)

    def test_timeout_is_unknown_and_ansible_is_check_mode(self):
        with patch.dict(os.environ, {"ANSIBLE_INVENTORY_FILE": "/crew/inventory", "ANSIBLE_PLAYBOOK_FILE": "/crew/playbook"}, clear=True):
            with patch.object(subprocess, "run", side_effect=subprocess.TimeoutExpired("ansible-playbook", 120)) as run:
                payload, failed = collector.collect("ansible")
                self.assertTrue(failed)
                self.assertIn("unknown", payload["items"][0]["label"])
                self.assertIn("--check", run.call_args.args[0])

    def test_routine_output_preserves_failure_for_following_page_write(self):
        output = io.StringIO()
        with patch.dict(os.environ, {}, clear=True), patch("sys.argv", ["collect_status.py", "mysql", "--routine-output"]), contextlib.redirect_stdout(output):
            self.assertEqual(collector.main(), 0)
        self.assertEqual(json.loads(output.getvalue())["producer_state"], "failed")

if __name__ == "__main__":
    unittest.main()
