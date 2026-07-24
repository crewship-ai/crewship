package main

import (
	"testing"
	"time"
)

// Tests for the pure helpers in cmd_routine_backtest.go. Like bench's
// test file, the cobra command itself does N HTTP round-trips (corpus
// fetch + N pinned replays) so it's exercised at the integration layer;
// here we pin down corpus selection, per-run grading, and the
// aggregate-verdict thresholds that are easy to regress.

func TestBacktestVerdict_MatchDivergeRegress(t *testing.T) {
	cases := []struct {
		name            string
		sourceOutput    string
		candidateStatus string
		candidateOutput string
		want            string
	}{
		{"identical output, completed", "hello world", "COMPLETED", "hello world", "MATCH"},
		{"deduped counts as pass, identical output", "hello world", "DEDUPED", "hello world", "MATCH"},
		{"passing but output text changed", "hello world", "COMPLETED", "hello there", "DIVERGED"},
		{"candidate failed", "hello world", "FAILED", "", "REGRESSED"},
		{"candidate cancelled", "hello world", "CANCELLED", "hello world", "REGRESSED"},
		{"candidate waiting (non-terminal) treated as regression", "hello world", "WAITING", "", "REGRESSED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := backtestVerdict(c.sourceOutput, c.candidateStatus, c.candidateOutput)
			if got != c.want {
				t.Errorf("backtestVerdict(%q, %q, %q) = %q, want %q",
					c.sourceOutput, c.candidateStatus, c.candidateOutput, got, c.want)
			}
		})
	}
}

func TestFilterRunsSince_WindowAndLimit(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	mk := func(id string, ageHours int) backtestSourceRun {
		return backtestSourceRun{
			RunID:     id,
			Status:    "COMPLETED",
			StartedAt: now.Add(-time.Duration(ageHours) * time.Hour).Format(time.RFC3339Nano),
		}
	}
	// Newest-first, as the run-records API returns them.
	records := []backtestSourceRun{
		mk("run_1h", 1),
		mk("run_2h", 2),
		mk("run_8d", 8*24), // outside a 7d window
		mk("run_9d", 9*24), // outside a 7d window
		mk("run_3h", 3),
	}
	since := now.Add(-7 * 24 * time.Hour)

	got := filterRunsSince(records, since, 10)
	if len(got) != 3 {
		t.Fatalf("filterRunsSince window: got %d runs, want 3 (run_1h, run_2h, run_3h); rows=%v", len(got), got)
	}
	wantOrder := []string{"run_1h", "run_2h", "run_3h"}
	for i, w := range wantOrder {
		if got[i].RunID != w {
			t.Errorf("row %d: got %q, want %q (order must stay newest-first)", i, got[i].RunID, w)
		}
	}

	// Limit caps the corpus even when more runs fall inside the window.
	limited := filterRunsSince(records, since, 2)
	if len(limited) != 2 {
		t.Fatalf("filterRunsSince limit: got %d runs, want 2", len(limited))
	}
	if limited[0].RunID != "run_1h" || limited[1].RunID != "run_2h" {
		t.Errorf("filterRunsSince limit: got %v, want [run_1h run_2h]", limited)
	}
}

func TestFilterRunsSince_UnparsableTimestampSkipped(t *testing.T) {
	records := []backtestSourceRun{
		{RunID: "bad", StartedAt: "not-a-timestamp"},
		{RunID: "good", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	got := filterRunsSince(records, time.Now().Add(-time.Hour), 10)
	if len(got) != 1 || got[0].RunID != "good" {
		t.Errorf("expected only the parsable row to survive, got %v", got)
	}
}

func TestSummariseBacktest_AggregatesAndThresholds(t *testing.T) {
	t.Run("no corpus", func(t *testing.T) {
		s := summariseBacktest("r", 9, time.Now(), nil)
		if s.Verdict != "NO_CORPUS" {
			t.Errorf("Verdict = %q, want NO_CORPUS", s.Verdict)
		}
	})

	t.Run("all matched -> clean", func(t *testing.T) {
		rows := []backtestRunRow{
			{Verdict: "MATCH"}, {Verdict: "MATCH"}, {Verdict: "MATCH"},
		}
		s := summariseBacktest("r", 9, time.Now(), rows)
		if s.Runs != 3 || s.Matched != 3 {
			t.Errorf("Runs/Matched = %d/%d, want 3/3", s.Runs, s.Matched)
		}
		if s.Verdict != "CLEAN" {
			t.Errorf("Verdict = %q, want CLEAN", s.Verdict)
		}
	})

	t.Run("diverged but no regressions -> diverged_outputs", func(t *testing.T) {
		rows := []backtestRunRow{
			{Verdict: "MATCH"}, {Verdict: "DIVERGED"},
		}
		s := summariseBacktest("r", 9, time.Now(), rows)
		if s.Diverged != 1 {
			t.Errorf("Diverged = %d, want 1", s.Diverged)
		}
		if s.Verdict != "DIVERGED_OUTPUTS" {
			t.Errorf("Verdict = %q, want DIVERGED_OUTPUTS", s.Verdict)
		}
	})

	t.Run("any regression -> regression_detected even with matches", func(t *testing.T) {
		rows := []backtestRunRow{
			{Verdict: "MATCH"}, {Verdict: "MATCH"}, {Verdict: "REGRESSED"},
		}
		s := summariseBacktest("r", 9, time.Now(), rows)
		if s.Regressed != 1 {
			t.Errorf("Regressed = %d, want 1", s.Regressed)
		}
		if s.Verdict != "REGRESSION_DETECTED" {
			t.Errorf("Verdict = %q, want REGRESSION_DETECTED", s.Verdict)
		}
	})

	t.Run("transport errors also block a clean verdict", func(t *testing.T) {
		rows := []backtestRunRow{
			{Verdict: "MATCH"}, {Verdict: "ERROR"},
		}
		s := summariseBacktest("r", 9, time.Now(), rows)
		if s.Errored != 1 {
			t.Errorf("Errored = %d, want 1", s.Errored)
		}
		if s.Verdict != "REGRESSION_DETECTED" {
			t.Errorf("Verdict = %q, want REGRESSION_DETECTED (a replay error is not a clean pass)", s.Verdict)
		}
	})
}
