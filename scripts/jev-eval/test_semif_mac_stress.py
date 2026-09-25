"""Check that the stress report exposes unsafe order-dependent routes."""

import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location("semif_mac_stress", Path(__file__).with_name("semif-mac-stress.py"))
STRESS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(STRESS)


class StressTests(unittest.TestCase):
    def test_order_change_can_become_wrong_automatic_route(self):
        cases = [{"id": "case", "area": "dryrun", "state": "Ansible check failed",
                  "expected": "fix", "language": "en"}]
        rows = STRESS.variants(cases)
        self.assertEqual(len(rows), 5)
        outputs = []
        for row in rows:
            ids = [option["id"] for option in row["options"]]
            winner = "proceed" if row["id"].endswith("::reversed") else "fix"
            outputs.append({"id": row["id"], "option_ids": ids,
                            "probabilities": [.99 if key == winner else .005 for key in ids],
                            "total_seconds": .01})
        _, summary = STRESS.analyze(cases, rows, outputs, .9)
        self.assertEqual(summary["repeat_probability_differences"], [])
        self.assertEqual(summary["wrong_automatic_routes"][0]["variant"], "reversed")
        self.assertEqual(summary["wrong_automatic_routes"][0]["route"], "proceed")


if __name__ == "__main__":
    unittest.main()
