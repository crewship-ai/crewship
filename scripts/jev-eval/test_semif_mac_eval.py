"""Check the local scorer contract and conservative routing calculation."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("semif-mac-eval.py")
SPEC = importlib.util.spec_from_file_location("semif_mac_eval", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class SemIfMacEvalTests(unittest.TestCase):
    def test_prepared_rows_and_review_fallback(self):
        cases = MODULE.load_cases(Path(__file__).with_name("semif-mac-cases.jsonl"))
        self.assertEqual(len(cases), 22)
        prepared = MODULE.prepare_rows(cases)
        self.assertEqual(len({row["id"] for row in prepared}), len(prepared))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "output.jsonl"
            lines = []
            for row in prepared:
                probabilities = [.02] * len(row["options"])
                probabilities[0] = 1 - .02 * (len(probabilities) - 1)
                lines.append(json.dumps({
                    "id": row["id"], "option_ids": [option["id"] for option in row["options"]],
                    "probabilities": probabilities, "total_seconds": .012,
                    "model": {"source": MODULE.MODEL, "revision": MODULE.REVISION,
                              "backend": "mlx", "quantization": {"bits": 4}},
                }))
            path.write_text("\n".join(lines) + "\n")
            result = MODULE.read_results(path, cases, .99, "4")
            self.assertTrue(all(row["chosen"] == "review" for row in result))
            self.assertEqual(MODULE.metrics(result)["action_coverage"], 0)
            first = json.loads(lines[0])
            first["option_ids"][0] = "unknown"
            path.write_text(json.dumps(first) + "\n" + "\n".join(lines[1:]) + "\n")
            with self.assertRaisesRegex(ValueError, "incompatible options"):
                MODULE.read_results(path, cases, .9, "4")


if __name__ == "__main__":
    unittest.main()
