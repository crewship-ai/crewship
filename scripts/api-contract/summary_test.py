#!/usr/bin/env python3
import tempfile
import unittest
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).parent))
from summary import classify_failure, junit_failures  # noqa: E402


class SummaryTest(unittest.TestCase):
    def test_runner_keeps_safe_scope_explicit_and_bounded(self):
        runner = (Path(__file__).parent / "run.sh").read_text()
        for method in ("POST", "PUT", "PATCH", "DELETE"):
            self.assertIn(f"--exclude-method {method}", runner)
        for route in ("admin/backups/download", "files/download", "avatar", "pipelines/[^/]+/export", "memory/", "journal/stream"):
            self.assertIn(route, runner)
        self.assertIn("AUTH_UI_PATH_REGEX='^/api/auth(/|$)'", runner)
        self.assertIn("--max-examples 10", runner)

    def test_response_schema_failure_is_schema(self):
        self.assertEqual(classify_failure("response schema conformance failed", 1), "schema")

    def test_executed_server_failure_is_runtime(self):
        self.assertEqual(classify_failure("server error: status code 500", 1), "runtime")

    def test_abort_is_schema(self):
        self.assertEqual(classify_failure("invalid OpenAPI document", 2), "schema")

    def test_junit_is_concise_and_classified(self):
        xml = """<testsuite><testcase name=\"GET /api/v1/crews\"><failure message=\"response schema mismatch\" /></testcase><testcase name=\"GET /api/v1/health\"><failure message=\"server error 500\" /></testcase></testsuite>"""
        with tempfile.NamedTemporaryFile("w", suffix=".xml", delete=False) as report:
            report.write(xml)
            report_path = report.name
        try:
            self.assertEqual(
                junit_failures(report_path, 1),
                [
                    {"name": "GET /api/v1/crews", "class": "schema"},
                    {"name": "GET /api/v1/health", "class": "runtime"},
                ],
            )
        finally:
            Path(report_path).unlink()


if __name__ == "__main__":
    unittest.main()
