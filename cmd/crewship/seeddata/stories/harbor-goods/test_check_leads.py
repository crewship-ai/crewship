import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("check_leads", HERE / "check_leads.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class CheckLeadsTest(unittest.TestCase):
    def test_one_unanswered_inquiry_is_flagged(self):
        result = MODULE.check(HERE / "leads.json", 24)
        self.assertEqual(result["state"], "warning")
        self.assertEqual(result["lead_id"], "L-103")
        self.assertEqual(result["age"], "26 hours")

    def test_replied_inquiry_is_not_flagged(self):
        fixture = json.loads((HERE / "leads.json").read_text())
        fixture["leads"][2]["replied"] = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "leads.json"
            path.write_text(json.dumps(fixture))
            result = MODULE.check(path, 24)
        self.assertEqual(result["state"], "ok")
        self.assertEqual(result["lead_id"], "none")

    def test_missing_fixture_reports_failure(self):
        result = MODULE.check(Path("/definitely/missing/demo-leads.json"), 24)
        self.assertEqual(result["state"], "critical")
        self.assertIn("could not be checked", result["headline"])


if __name__ == "__main__":
    unittest.main()
