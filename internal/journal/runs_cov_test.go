package journal

// Coverage tests for runs.go — status filters the base suite skips
// (CANCELLED / TIMEOUT / RUNNING), the tag filter, limit clamping,
// terminal-payload field extraction, and RunStats counters.

import (
	"context"
	"testing"
	"time"
)

func TestListRuns_CancelledTimeoutRunningFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()

	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	emitRun(t, w, "ws_test", "ag_1", "run_cancelled", "CANCELLED", "manual", base)
	emitRun(t, w, "ws_test", "ag_1", "run_timeout", "TIMEOUT", "manual", base.Add(time.Hour))
	emitRun(t, w, "ws_test", "ag_1", "run_live", "", "manual", base.Add(2*time.Hour))
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	cases := []struct {
		status RunStatus
		wantID string
	}{
		{RunStatusCancelled, "run_cancelled"},
		{RunStatusTimeout, "run_timeout"},
		{RunStatusRunning, "run_live"},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			runs, total, err := ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test", Status: c.status})
			if err != nil {
				t.Fatalf("ListRuns(%s): %v", c.status, err)
			}
			if total != 1 || len(runs) != 1 {
				t.Fatalf("status %s: got %d runs (total %d), want 1", c.status, len(runs), total)
			}
			if runs[0].ID != c.wantID {
				t.Errorf("status %s: run = %s, want %s", c.status, runs[0].ID, c.wantID)
			}
			if runs[0].Status != c.status {
				t.Errorf("mapped status = %s, want %s", runs[0].Status, c.status)
			}
		})
	}

	// Terminal runs must carry FinishedAt; running ones must not.
	runs, _, err := ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("ListRuns all: %v", err)
	}
	for _, r := range runs {
		if r.Status == RunStatusRunning && r.FinishedAt != nil {
			t.Errorf("running run %s has FinishedAt", r.ID)
		}
		if r.Status != RunStatusRunning && r.FinishedAt == nil {
			t.Errorf("terminal run %s missing FinishedAt", r.ID)
		}
	}
}

func TestListRuns_TagFilterAndPayloadExtraction(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

	// Run with tags + chat + metadata in run.started, error details in
	// the terminal entry.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test", AgentID: "ag_1", Type: EntryRunStarted,
		ActorType: ActorSidecar, ActorID: "user_7", Summary: "started",
		TraceID: "run_tagged", TS: base,
		Payload: map[string]any{
			"trigger_type": "schedule",
			"chat_id":      "chat_42",
			"metadata":     map[string]any{"tags": []any{"nightly", "smoke"}},
		},
	}); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test", AgentID: "ag_1", Type: EntryRunFailed,
		ActorType: ActorSidecar, Summary: "failed",
		TraceID: "run_tagged", TS: base.Add(time.Minute),
		Payload: map[string]any{"error_message": "boom", "exit_code": float64(3)},
	}); err != nil {
		t.Fatalf("emit terminal: %v", err)
	}
	// A second, untagged run that the tag filter must exclude.
	emitRun(t, w, "ws_test", "ag_1", "run_plain", "COMPLETED", "manual", base.Add(2*time.Hour))
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	runs, total, err := ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test", Tag: "nightly"})
	if err != nil {
		t.Fatalf("ListRuns tag: %v", err)
	}
	if total != 1 || len(runs) != 1 {
		t.Fatalf("tag filter: got %d (total %d), want 1", len(runs), total)
	}
	r := runs[0]
	if r.ID != "run_tagged" {
		t.Fatalf("tag filter matched %s, want run_tagged", r.ID)
	}
	if r.TriggerType != "schedule" {
		t.Errorf("TriggerType = %q, want schedule", r.TriggerType)
	}
	if r.ChatID != "chat_42" {
		t.Errorf("ChatID = %q, want chat_42", r.ChatID)
	}
	if r.TriggeredBy != "user_7" {
		t.Errorf("TriggeredBy = %q, want user_7", r.TriggeredBy)
	}
	if r.Metadata == nil {
		t.Fatal("Metadata not extracted")
	}
	if r.ErrorMessage != "boom" {
		t.Errorf("ErrorMessage = %q, want boom", r.ErrorMessage)
	}
	if r.ExitCode == nil || *r.ExitCode != 3 {
		t.Errorf("ExitCode = %v, want 3", r.ExitCode)
	}
	if r.Status != RunStatusFailed {
		t.Errorf("Status = %s, want FAILED", r.Status)
	}

	// Non-matching tag.
	_, total, err = ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test", Tag: "missing"})
	if err != nil {
		t.Fatalf("ListRuns missing tag: %v", err)
	}
	if total != 0 {
		t.Errorf("missing tag matched %d runs", total)
	}
}

func TestListRuns_LimitClampAndOffset(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		emitRun(t, w, "ws_test", "ag_1", "run_"+string(rune('a'+i)), "COMPLETED", "manual",
			base.Add(time.Duration(i)*time.Hour))
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Limit above the 100 cap must not error and must return everything.
	runs, total, err := ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test", Limit: 5000})
	if err != nil {
		t.Fatalf("ListRuns big limit: %v", err)
	}
	if total != 3 || len(runs) != 3 {
		t.Fatalf("big limit: got %d (total %d), want 3", len(runs), total)
	}

	// Offset pagination: skip the newest run.
	runs, total, err = ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListRuns offset: %v", err)
	}
	if total != 3 {
		t.Errorf("total with offset = %d, want 3 (unbounded by paging)", total)
	}
	if len(runs) != 1 || runs[0].ID != "run_b" {
		t.Errorf("offset page = %v, want [run_b]", runs)
	}
}

func TestListRuns_RequiresWorkspaceAndClosedDB(t *testing.T) {
	db := openTestDB(t)
	if _, _, err := ListRuns(context.Background(), db, RunsQuery{}); err == nil {
		t.Error("ListRuns without workspace should error")
	}
	db.Close()
	if _, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"}); err == nil {
		t.Error("ListRuns on closed DB should error")
	}
}

func TestRunStats_CountsAllBuckets(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()

	// Anchor "today" at 00:01 UTC so the +2min terminal stamp emitRun
	// adds can never roll over into another date — keeps the date('now')
	// comparison in RunStats deterministic at any wall-clock time.
	today := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Minute)
	// One still running (started today), one completed today, one failed
	// today, one failed ten days ago (must not count toward FailedToday).
	emitRun(t, w, "ws_test", "ag_1", "rs_live", "", "manual", today)
	emitRun(t, w, "ws_test", "ag_1", "rs_done", "COMPLETED", "manual", today.Add(10*time.Minute))
	emitRun(t, w, "ws_test", "ag_1", "rs_fail", "FAILED", "manual", today.Add(20*time.Minute))
	emitRun(t, w, "ws_test", "ag_1", "rs_old_fail", "FAILED", "manual", today.AddDate(0, 0, -10))
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	res, err := RunStats(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("RunStats: %v", err)
	}
	if res.Running != 1 {
		t.Errorf("Running = %d, want 1", res.Running)
	}
	if res.Today != 3 {
		t.Errorf("Today = %d, want 3", res.Today)
	}
	if res.FailedToday != 1 {
		t.Errorf("FailedToday = %d, want 1", res.FailedToday)
	}
}

func TestRunStats_RequiresWorkspaceAndClosedDB(t *testing.T) {
	db := openTestDB(t)
	if _, err := RunStats(context.Background(), db, ""); err == nil {
		t.Error("RunStats without workspace should error")
	}
	db.Close()
	if _, err := RunStats(context.Background(), db, "ws_test"); err == nil {
		t.Error("RunStats on closed DB should error")
	}
}
