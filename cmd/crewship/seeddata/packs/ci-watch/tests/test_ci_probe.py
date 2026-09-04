#!/usr/bin/env python3
"""
Tests for the nightly CI probe (scripts/ci_probe.py).

The probe is AGENTLESS — it runs as the wake gate in front of the expensive
triage routine, so it has to be deterministic and cheap. Two things it watches:

  1. RED    — a workflow finished and failed.
  2. STALE  — a workflow has not run for a while. This is the silent failure:
              a dropped cron, a workflow disabled after 60 idle days, broken
              YAML — nobody notices, because no red ever appears.

Run:
    python3 -m unittest discover -s tests -v
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
PROBE = os.path.join(HERE, "..", "scripts", "ci_probe.py")

NOW = "2026-08-06T07:00:00Z"


def run(workflows, runs, now=NOW, max_stale_hours=None, exit_code=False):
    """Calls the probe with injected data instead of the GitHub API."""
    with tempfile.TemporaryDirectory() as d:
        wf = os.path.join(d, "workflows.json")
        rn = os.path.join(d, "runs.json")
        with open(wf, "w", encoding="utf-8") as f:
            json.dump(workflows, f)
        with open(rn, "w", encoding="utf-8") as f:
            json.dump(runs, f)
        cmd = [sys.executable, PROBE, "--workflows-file", wf,
               "--runs-file", rn, "--now", now]
        if max_stale_hours is not None:
            cmd += ["--max-stale-hours", str(max_stale_hours)]
        if exit_code:
            cmd += ["--exit-code"]
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode not in (0, 1):
            raise AssertionError(f"probe failed ({r.returncode}): {r.stderr}")
        return json.loads(r.stdout), r.returncode


def wf(name, cron="0 3 * * *"):
    return {"name": name, "cron": cron}


def run_row(name, conclusion, created_at, run_id=1):
    return {"workflow": name, "conclusion": conclusion,
            "created_at": created_at, "id": run_id,
            "html_url": f"https://github.com/x/y/actions/runs/{run_id}"}


class TestRed(unittest.TestCase):
    def test_all_green_does_not_wake(self):
        out, code = run([wf("Nightly E2E")],
                        [run_row("Nightly E2E", "success", "2026-08-06T03:50:00Z")])
        self.assertFalse(out["wake"])
        self.assertEqual(out["red"], 0)
        self.assertEqual(code, 0)

    def test_failed_workflow_wakes(self):
        out, code = run([wf("E2E Devcontainer")],
                        [run_row("E2E Devcontainer", "failure", "2026-08-06T02:35:00Z")])
        self.assertTrue(out["wake"])
        self.assertEqual(out["red"], 1)
        self.assertEqual(out["detail"][0]["status"], "RED")
        self.assertEqual(code, 0)  # see test_default_exit_is_always_zero

    def test_every_failure_conclusion_counts_as_red(self):
        for conclusion in ("failure", "timed_out", "cancelled", "startup_failure",
                           "action_required", "stale"):
            with self.subTest(conclusion=conclusion):
                out, _ = run([wf("Nightly Harness")],
                             [run_row("Nightly Harness", conclusion, "2026-08-06T03:10:00Z")])
                self.assertEqual(out["red"], 1, out)

    def test_running_run_is_not_red(self):
        """conclusion=null means 'still running' — not a failure."""
        out, _ = run([wf("Security")],
                     [run_row("Security", None, "2026-08-06T05:23:00Z")])
        self.assertEqual(out["red"], 0)
        self.assertEqual(out["detail"][0]["status"], "RUNNING")

    def test_latest_run_wins_not_first_in_list(self):
        """Yesterday's failure must not override today's success."""
        out, _ = run([wf("Nightly E2E")], [
            run_row("Nightly E2E", "failure", "2026-08-05T03:50:00Z", 1),
            run_row("Nightly E2E", "success", "2026-08-06T03:50:00Z", 2),
        ])
        self.assertEqual(out["red"], 0, out)
        self.assertEqual(out["detail"][0]["run_id"], 2)


class TestStale(unittest.TestCase):
    """The silent failure: a workflow stopped running and nobody noticed."""

    def test_workflow_without_a_run_for_three_days_is_stale(self):
        out, _ = run([wf("E2E Devcontainer")],
                     [run_row("E2E Devcontainer", "success", "2026-08-03T02:30:00Z")])
        self.assertTrue(out["wake"])
        self.assertEqual(out["stale"], 1)
        self.assertEqual(out["detail"][0]["status"], "STALE")

    def test_workflow_that_never_ran_is_stale(self):
        out, _ = run([wf("Runtime Conformance")], [])
        self.assertEqual(out["stale"], 1)
        self.assertIn("no run", out["detail"][0]["reason"])

    def test_fresh_run_is_not_stale(self):
        out, _ = run([wf("Nightly Harness")],
                     [run_row("Nightly Harness", "success", "2026-08-06T03:10:00Z")])
        self.assertEqual(out["stale"], 0)
        self.assertFalse(out["wake"])

    def test_stale_threshold_is_configurable(self):
        """30 h passes at the default 48 h, but not at 24 h."""
        runs = [run_row("Security", "success", "2026-08-05T01:00:00Z")]
        out, _ = run([wf("Security")], runs)
        self.assertEqual(out["stale"], 0, out)
        out2, _ = run([wf("Security")], runs, max_stale_hours=24)
        self.assertEqual(out2["stale"], 1, out2)

    def test_red_takes_precedence_over_stale(self):
        """An old AND failed run is reported red, not twice."""
        out, _ = run([wf("Nightly E2E")],
                     [run_row("Nightly E2E", "failure", "2026-08-01T03:50:00Z")])
        self.assertEqual(out["red"] + out["stale"], 1)
        self.assertEqual(out["detail"][0]["status"], "RED")


class TestOutput(unittest.TestCase):
    def test_counts_match_detail(self):
        out, _ = run(
            [wf("A"), wf("B"), wf("C"), wf("D")],
            [
                run_row("A", "success", "2026-08-06T03:00:00Z", 1),
                run_row("B", "failure", "2026-08-06T03:00:00Z", 2),
                run_row("C", "success", "2026-08-01T03:00:00Z", 3),
                run_row("D", None, "2026-08-06T06:00:00Z", 4),
            ])
        self.assertEqual(out["checked"], 4)
        self.assertEqual(len(out["detail"]), 4)
        self.assertEqual(out["red"], 1)
        self.assertEqual(out["stale"], 1)
        self.assertEqual(out["ok"], 1)
        self.assertEqual(out["running"], 1)

    def test_detail_is_sorted_problems_first(self):
        out, _ = run(
            [wf("Green"), wf("Red")],
            [
                run_row("Green", "success", "2026-08-06T03:00:00Z", 1),
                run_row("Red", "failure", "2026-08-06T03:00:00Z", 2),
            ])
        self.assertEqual(out["detail"][0]["workflow"], "Red")

    def test_panel_states_follow_the_counts(self):
        """The Page panel is computed HERE, because a transform step can only
        project a field — it cannot decide what 'critical' means."""
        calm, _ = run([wf("A")], [run_row("A", "success", "2026-08-06T03:00:00Z")])
        self.assertEqual(calm["panel"]["red_state"], "ok")
        self.assertEqual(calm["panel"]["stale_state"], "ok")
        loud, _ = run([wf("A"), wf("B")], [
            run_row("A", "failure", "2026-08-06T03:00:00Z", 1),
            run_row("B", "success", "2026-08-01T03:00:00Z", 2),
        ])
        self.assertEqual(loud["panel"]["red_state"], "critical")
        self.assertIn("A", loud["panel"]["red_label"])
        self.assertEqual(loud["panel"]["stale_state"], "warning")
        self.assertIn("B", loud["panel"]["stale_label"])
        self.assertIn("2 scheduled workflows", loud["panel"]["checked_label"])

    def test_default_exit_is_always_zero(self):
        """
        In a routine `script` step a non-zero exit means the step failed. A
        probe that finds a red workflow MUST exit zero — otherwise it aborts
        the run instead of waking it.
        """
        _, code = run([wf("A")], [run_row("A", "failure", "2026-08-06T03:00:00Z")])
        self.assertEqual(code, 0)

    def test_exit_code_1_only_with_the_flag(self):
        """For shell/CI the state is available in the exit code too."""
        _, code_ok = run([wf("A")], [run_row("A", "success", "2026-08-06T03:00:00Z")],
                         exit_code=True)
        _, code_bad = run([wf("A")], [run_row("A", "failure", "2026-08-06T03:00:00Z")],
                          exit_code=True)
        self.assertEqual(code_ok, 0)
        self.assertEqual(code_bad, 1)


if __name__ == "__main__":
    unittest.main()
