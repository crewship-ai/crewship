"""Exercise replay IDs, scorer paths, and the oversized probe boundary."""

import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


HERE = Path(__file__).resolve().parent


def load_script(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), HERE / name)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ScriptBoundaryTests(unittest.TestCase):
    def test_scorer_paths_stay_absolute_when_cwd_changes(self):
        scripts = (
            ("semif-mac-eval.py", ["--cases", str(HERE / "semif-mac-cases.jsonl")]),
            ("semif-mac-length.py", []),
            ("semif-mac-multifacet.py", []),
            ("semif-mac-stress.py", []),
        )
        original_cwd = Path.cwd()
        with tempfile.TemporaryDirectory() as directory:
            os.chdir(directory)
            try:
                scorer = Path("semif-root/.venv/bin/semif-score")
                scorer.parent.mkdir(parents=True)
                scorer.touch()
                for index, (filename, extra) in enumerate(scripts):
                    with self.subTest(filename=filename):
                        module = load_script(filename)
                        argv = [filename, "--semif-root", "semif-root", "--output", f"output-{index}", *extra]

                        def check_command(command, **kwargs):
                            self.assertTrue(Path(command[0]).is_absolute())
                            self.assertTrue(Path(command[command.index("--input") + 1]).is_absolute())
                            self.assertTrue(Path(command[command.index("--output") + 1]).is_absolute())
                            self.assertTrue(Path(kwargs["cwd"]).is_absolute())
                            raise StopIteration("paths checked before model execution")

                        with mock.patch.object(sys, "argv", argv), mock.patch.object(module.subprocess, "run", side_effect=check_command):
                            with self.assertRaisesRegex(StopIteration, "paths checked"):
                                module.main()
            finally:
                os.chdir(original_cwd)

    def test_webhook_reuses_source_event_id(self):
        bridge = load_script("local-mlx-webhook-bridge.py")
        event = {"event_id": "outage-42", "simulation": True, "type": "outage",
                 "service": "payments", "summary": "unavailable"}
        hook = {"public_url": "http://127.0.0.1:8081/hook", "signing_secret": "test-secret"}
        sent_ids = []

        class Response(io.BytesIO):
            status = 202

        def respond(request, timeout):
            sent_ids.append(request.get_header("X-crewship-event-id"))
            return Response(json.dumps({"deduped": len(sent_ids) > 1}).encode())

        with mock.patch.object(bridge.urllib.request, "urlopen", side_effect=respond):
            bridge.fire(hook, event)
            bridge.fire(hook, event)
        self.assertEqual(sent_ids, ["outage-42", "outage-42"])

    def test_webhook_requires_stable_event_id_before_scoring(self):
        bridge = load_script("local-mlx-webhook-bridge.py")
        with tempfile.TemporaryDirectory() as directory:
            event_path = Path(directory) / "event.json"
            event_path.write_text(json.dumps({"simulation": True, "type": "outage",
                                              "service": "payments", "summary": "unavailable"}))
            with mock.patch.object(sys, "argv", ["bridge", "--event", str(event_path)]), \
                    mock.patch.object(bridge, "score") as score, \
                    mock.patch.object(sys, "stderr", io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    bridge.main()
            self.assertEqual(error.exception.code, 2)
            score.assert_not_called()

    def test_oversized_probe_rejects_only_the_token_limit(self):
        length = load_script("semif-mac-length.py")
        length.confirm_token_limit_rejection(1, "7338 tokens exceed max_tokens: no truncation allowed", 0)
        for code, error, rows in ((1, "model crashed", 0), (0, "", 0),
                                  (1, "token limit: no truncation allowed", 1)):
            with self.subTest(code=code, error=error, rows=rows):
                with self.assertRaisesRegex(RuntimeError, "did not confirm"):
                    length.confirm_token_limit_rejection(code, error, rows)


if __name__ == "__main__":
    unittest.main()
