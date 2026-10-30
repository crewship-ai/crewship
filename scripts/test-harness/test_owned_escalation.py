"""The harness must never decide another user's pending request."""
import json
from pathlib import Path
import subprocess
import unittest

FILTER = Path(__file__).with_name("owned-escalation.jq")

class OwnedEscalationTests(unittest.TestCase):
    def row(self, **overrides):
        return dict(id="own", type="CREDENTIAL", status="PENDING", from_slug="morgan", metadata=json.dumps({"name": "HARNESS_OWN"}), **overrides)

    def select(self, rows):
        return subprocess.run(["jq", "-er", "--arg", "agent", "morgan", "--arg", "name", "HARNESS_OWN", "-f", str(FILTER)], input=json.dumps(rows), capture_output=True, text=True)

    def test_foreign_first_does_not_win(self):
        foreign = self.row(); foreign.update(id="foreign", metadata=json.dumps({"name": "OTHER"}))
        result = self.select([foreign, self.row()])
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout.strip(), "own")

    def test_generic_credential_reason_is_not_ownership(self):
        row = self.row(); row.update(metadata=None, reason="credential HARNESS_OWN")
        self.assertNotEqual(self.select([row]).returncode, 0)

    def test_duplicate_owned_rows_are_ambiguous(self):
        self.assertNotEqual(self.select([self.row(), self.row()]).returncode, 0)

    def test_other_agent_is_not_ownership(self):
        row = self.row(); row["from_slug"] = "sam"
        self.assertNotEqual(self.select([row]).returncode, 0)

    def test_closed_request_is_not_selected(self):
        row = self.row(); row["status"] = "RESOLVED"
        self.assertNotEqual(self.select([row]).returncode, 0)

    def test_wrong_type_is_not_selected(self):
        row = self.row(); row["type"] = "QUESTION"
        self.assertNotEqual(self.select([row]).returncode, 0)

    def test_malformed_metadata_is_not_selected(self):
        row = self.row(); row["metadata"] = "{broken"
        self.assertNotEqual(self.select([row]).returncode, 0)

    def test_missing_id_is_not_selected(self):
        row = self.row(); row["id"] = None
        self.assertNotEqual(self.select([row]).returncode, 0)

if __name__ == "__main__":
    unittest.main()
