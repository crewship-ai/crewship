package journal

import (
	"context"
	"testing"
	"time"
)

// TestListRuns_SurfacesResolvedModel proves the model recorded on the
// terminal run.* entry's metadata is surfaced on RunAggregated.Model — the
// queryable run record an operator reads to confirm Opus-vs-Sonnet.
func TestListRuns_SurfacesResolvedModel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// run.started — no model yet (known only after session-init).
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryRunStarted,
		ActorType:   ActorSidecar,
		Summary:     "started",
		Payload:     map[string]any{"trigger_type": "USER"},
		TraceID:     "run_model",
		TS:          now,
	}); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	// run.completed — the driver stamps the actually-resolved model into the
	// terminal entry's metadata.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryRunCompleted,
		ActorType:   ActorSidecar,
		Summary:     "completed",
		Payload: map[string]any{
			"exit_code": float64(0),
			"metadata":  map[string]any{"model": "claude-sonnet-4-5"},
		},
		TraceID: "run_model",
		TS:      now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("emit completed: %v", err)
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("rows: got %d want 1", len(runs))
	}
	if runs[0].Model != "claude-sonnet-4-5" {
		t.Errorf("RunAggregated.Model = %q, want claude-sonnet-4-5", runs[0].Model)
	}
}

// TestListRuns_NoModelLeavesBlank confirms a run without a recorded model
// leaves Model empty rather than erroring — best-effort surface.
func TestListRuns_NoModelLeavesBlank(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_plain", "COMPLETED", "USER", now)
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].Model != "" {
		t.Errorf("expected one run with blank model, got %+v", runs)
	}
}
