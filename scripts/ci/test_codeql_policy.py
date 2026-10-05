"""Required-tool evidence must exist even when a PR changes no scanned sources."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


class CodeQLPolicyTests(unittest.TestCase):
    def test_actual_plan_runs_both_languages_for_docs_and_single_language_prs(self):
        workflow = (ROOT / '.github/workflows/codeql.yml').read_text()
        job = workflow.split('  changes:\n', 1)[1].split('\n  analyze:', 1)[0]
        block = re.search(r'        run: \|\n((?:          .*\n)+)', job)
        self.assertIsNotNone(block)
        script = textwrap.dedent(block.group(1))
        for event, go_changed, js_changed in [('pull_request', 'false', 'false'),
                                              ('pull_request', 'true', 'false'),
                                              ('pull_request', 'false', 'true'),
                                              ('merge_group', 'false', 'false'),
                                              ('push', 'false', 'false')]:
            with self.subTest(event=event, go=go_changed, js=js_changed), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / 'output'
                env = {**os.environ, 'GITHUB_OUTPUT': str(output), 'GITHUB_EVENT_NAME': event,
                       'IS_PR': str(event == 'pull_request').lower(),
                       'GO_CHANGED': go_changed, 'JS_CHANGED': js_changed}
                subprocess.run(['bash', '-c', script], env=env, check=True, capture_output=True)
                matrix = json.loads(output.read_text().strip().split('=', 1)[1])
                self.assertEqual({e['language']: e['build-mode'] for e in matrix},
                                 {'go': 'manual', 'javascript-typescript': 'none'})

    def test_workflow_cannot_path_skip_the_required_tool(self):
        workflow = (ROOT / '.github/workflows/codeql.yml').read_text()
        self.assertNotRegex(workflow, r'(?m)^\s+paths(?:-ignore)?:', 'required tool cannot skip docs-only PRs')
        self.assertIn('  merge_group:', workflow)
        self.assertIn('  pull_request:', workflow)
