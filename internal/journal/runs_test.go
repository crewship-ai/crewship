package journal

import (
	"context"
	"testing"
	"time"
)

// emitRun is a tiny helper that writes a run.started + a single
// terminal entry for trace_id=runID. status="" means "leave running"
// (only emit run.started).
func emitRun(t *testing.T, w *Writer, ws, agentID, runID, status, trigger string, when time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        EntryRunStarted,
		ActorType:   ActorSidecar,
		Summary:     "started",
		Payload:     map[string]any{"trigger_type": trigger},
		TraceID:     runID,
		TS:          when,
	})
	if err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if status == "" {
		return
	}
	var et EntryType
	switch status {
	case "COMPLETED":
		et = EntryRunCompleted
	case "FAILED":
		et = EntryRunFailed
	case "CANCELLED":
		et = EntryRunCancelled
	case "TIMEOUT":
		et = EntryRunTimeout
	default:
		t.Fatalf("unknown status %q", status)
	}
	_, err = w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        et,
		ActorType:   ActorSidecar,
		Summary:     status,
		Payload:     map[string]any{"exit_code": float64(0)},
		TraceID:     runID,
		TS:          when.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("emit terminal: %v", err)
	}
}

func TestListRuns_GroupsByTrace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_1", "COMPLETED", "USER", now.Add(-10*time.Minute))
	emitRun(t, w, "ws_test", "agent_b", "run_2", "FAILED", "WEBHOOK", now.Add(-5*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_3", "", "USER", now.Add(-1*time.Minute)) // still running
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 {
		t.Errorf("total: got %d want 3", total)
	}
	if len(runs) != 3 {
		t.Fatalf("rows: got %d want 3", len(runs))
	}
	// ORDER BY started_at DESC → run_3 first
	if runs[0].ID != "run_3" || runs[0].Status != RunStatusRunning {
		t.Errorf("first row: %+v", runs[0])
	}
	if runs[1].ID != "run_2" || runs[1].Status != RunStatusFailed {
		t.Errorf("second row: %+v", runs[1])
	}
	if runs[2].ID != "run_1" || runs[2].Status != RunStatusCompleted {
		t.Errorf("third row: %+v", runs[2])
	}
}

func TestListRuns_StatusFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_c", "COMPLETED", "USER", now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_f", "FAILED", "USER", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_r", "", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	for _, tc := range []struct {
		status RunStatus
		want   string
	}{
		{RunStatusRunning, "run_r"},
		{RunStatusCompleted, "run_c"},
		{RunStatusFailed, "run_f"},
	} {
		runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", Status: tc.status})
		if err != nil {
			t.Fatalf("list %s: %v", tc.status, err)
		}
		if len(runs) != 1 || runs[0].ID != tc.want {
			t.Errorf("status=%s: got %d rows (first=%v) want exactly run id %s",
				tc.status, len(runs), runFirstID(runs), tc.want)
		}
	}
}

func TestListRuns_AgentFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_a1", "COMPLETED", "USER", now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_b", "run_b1", "COMPLETED", "USER", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_a2", "FAILED", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", AgentID: "agent_a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(runs) != 2 {
		t.Errorf("rows: got %d / total %d, want 2/2", len(runs), total)
	}
}

func TestListRuns_TriggerFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_u", "COMPLETED", "USER", now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_w", "COMPLETED", "WEBHOOK", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_c", "COMPLETED", "CRON", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", TriggerType: "WEBHOOK"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run_w" {
		t.Errorf("trigger filter: %+v", runs)
	}
}

func TestRunStats(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "r1", "COMPLETED", "USER", now.Add(-30*time.Minute)) // today
	emitRun(t, w, "ws_test", "agent_a", "r2", "FAILED", "USER", now.Add(-10*time.Minute))    // today, failed
	emitRun(t, w, "ws_test", "agent_a", "r3", "TIMEOUT", "USER", now.Add(-5*time.Minute))    // today, timeout (counts as failed)
	emitRun(t, w, "ws_test", "agent_a", "r4", "", "USER", now.Add(-1*time.Minute))           // running
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	stats, err := RunStats(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Running != 1 {
		t.Errorf("running: got %d want 1", stats.Running)
	}
	if stats.Today != 4 {
		t.Errorf("today: got %d want 4", stats.Today)
	}
	if stats.FailedToday != 2 {
		t.Errorf("failed today: got %d want 2 (FAILED + TIMEOUT)", stats.FailedToday)
	}
}

func TestListRuns_WorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "mine_1", "COMPLETED", "USER", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_other", "agent_a", "theirs_1", "COMPLETED", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "mine_1" {
		t.Errorf("workspace leak: %+v", runs)
	}
}

func runFirstID(runs []RunAggregated) string {
	if len(runs) == 0 {
		return ""
	}
	return runs[0].ID
}
