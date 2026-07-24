package journal

import (
	"context"
	"testing"
	"time"
)

// TestList_FiltersByActorID covers the ActorID filter added for #1403:
// pipeline (routine) journal entries correlate to their run via
// ActorID == runID rather than TraceID (internal/pipeline/journal.go
// never sets TraceID on its emits), so the post-run verdict generator
// needs List to narrow on actor_id the same way it already narrows on
// trace_id for ad-hoc agent runs.
func TestList_FiltersByActorID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineRunStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_a",
		Summary:     "pipeline run_a started",
		TS:          now,
	}); err != nil {
		t.Fatalf("emit run_a started: %v", err)
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineRunCompleted,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_a",
		Summary:     "pipeline run_a completed",
		TS:          now.Add(time.Second),
	}); err != nil {
		t.Fatalf("emit run_a completed: %v", err)
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineRunStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_b",
		Summary:     "pipeline run_b started",
		TS:          now,
	}); err != nil {
		t.Fatalf("emit run_b started: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", ActorID: "run_a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (only run_a's entries)", len(entries))
	}
	for _, e := range entries {
		if e.ActorID != "run_a" {
			t.Errorf("entry %s has ActorID %q, want run_a", e.ID, e.ActorID)
		}
	}
}
