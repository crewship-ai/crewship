"""The helper fixtures must not consume the complete-project analysis deadline."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
FIXTURE_COMMAND = "node --test scripts/typecheck-tests-node-test.mjs"
ANALYSIS_COMMAND = "timeout --kill-after=30s 8m pnpm test:types"


def job(text, name):
    return re.search(r"^  " + re.escape(name) + r":\n(.*?)(?=^  [a-z][\w-]*:\n|\Z)",
                     text, re.M | re.S).group(1)


def step(lane, name):
    match = re.search(r"^      - name: " + re.escape(name) + r"\n(.*?)(?=^      - |\Z)",
                      lane, re.M | re.S)
    if match is None:
        raise AssertionError("Missing required workflow step: " + name)
    return match.group(1)


def script(block):
    match = re.search(r"^        run: (.*)$", block, re.M)
    if match.group(1) == "|":
        return textwrap.dedent(block[match.end() + 1:])
    return match.group(1)


class TypecheckWorkflowBoundary(unittest.TestCase):
    def setUp(self):
        self.ci = (ROOT / ".github/workflows/ci.yml").read_text()
        self.lane = job(self.ci, "frontend-test-types")

    def test_fixtures_have_their_own_bound_without_expanding_analysis_or_job(self):
        fixture = step(self.lane, "Typecheck helper regressions")
        analysis = step(self.lane, "Type-check tests once")
        self.assertEqual(self.lane.count(FIXTURE_COMMAND), 1)
        self.assertEqual(self.lane.count(ANALYSIS_COMMAND), 1)
        self.assertLess(self.lane.index(FIXTURE_COMMAND), self.lane.index(ANALYSIS_COMMAND))
        self.assertRegex(fixture, r"(?m)^        timeout-minutes: 3$")
        self.assertRegex(analysis, r"(?m)^        timeout-minutes: 9$")
        self.assertRegex(self.lane, r"(?m)^    timeout-minutes: 20$")
        self.assertEqual(script(fixture), FIXTURE_COMMAND)
        self.assertEqual(script(analysis).strip(), ANALYSIS_COMMAND)
        self.assertNotIn(FIXTURE_COMMAND, analysis)
        self.assertNotIn("continue-on-error", self.lane)
        # Default step success semantics propagate either failure to the
        # required job; neither step may add an always()/failure() bypass.
        for block in (fixture, analysis):
            self.assertNotRegex(block, r"(?m)^        if:")
        aggregate = job(self.ci, "frontend-test")
        self.assertIn("frontend-test-types", aggregate)
        self.assertIn('test "$SHARD_RESULT" = success && test "$TYPE_RESULT" = success', aggregate)
        self.assertIn("frontend-test", job(self.ci, "result"))

    def test_actual_step_commands_preserve_success_and_failure_exit_status(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            trace = root / "trace"
            for program in ("node", "pnpm"):
                executable = root / program
                executable.write_text(
                    "#!/bin/sh\n"
                    f'echo {program} >> "$TYPECHECK_TRACE"\n'
                    'exit "$TYPECHECK_STATUS"\n'
                )
                executable.chmod(0o755)
            for name, program in (("Typecheck helper regressions", "node"),
                                  ("Type-check tests once", "pnpm")):
                for status in (0, 17):
                    with self.subTest(step=name, status=status):
                        trace.write_text("")
                        env = dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"],
                                   TYPECHECK_TRACE=str(trace), TYPECHECK_STATUS=str(status))
                        result = subprocess.run(["bash", "-e", "-c", script(step(self.lane, name))],
                                                env=env, capture_output=True, text=True, timeout=5)
                        self.assertEqual(result.returncode, status, result.stderr)
                        self.assertEqual(trace.read_text().splitlines(), [program])


if __name__ == "__main__":
    unittest.main()
