package journal

import (
	"context"
	"testing"
	"time"
)

// A run id reaches the journal by three different doors, and which door
// depends on which execution engine ran the work:
//
//   - ad-hoc agent runs stamp trace_id      (internal/api/assignments_run.go)
//   - routine runs stamp actor_id           (internal/pipeline/journal.go)
//   - routine runs ALSO stamp payload.run_id, surfaced as the generated
//     column journal_entries.run_id (v120, indexed idx_journal_ws_run)
//
// internal/api/pipeline_runs.go:452 documents the consequence and matches
// two of the three so its run-log console works for both engines. Callers
// that want "everything that happened during run X" had no single filter:
// TraceID and ActorID are separate fields that AND together, so asking for
// both returns nothing at all.
//
// RunID is that single filter. It ORs the doors instead of ANDing them.
func TestList_FiltersByRunID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	// Door 1 — an agent run correlating via trace_id.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryRunStarted,
		ActorType:   ActorAgent,
		TraceID:     "run_x",
		Summary:     "agent run via trace_id",
		TS:          now,
	}); err != nil {
		t.Fatalf("emit trace_id entry: %v", err)
	}

	// Door 2 — a routine run correlating via actor_id.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineStepStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_x",
		Summary:     "routine step via actor_id",
		TS:          now.Add(time.Second),
	}); err != nil {
		t.Fatalf("emit actor_id entry: %v", err)
	}

	// Door 3 — the generated run_id column over payload.run_id.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineRunCompleted,
		ActorType:   ActorOrchestrator,
		Summary:     "routine run via payload.run_id",
		Payload:     map[string]any{"run_id": "run_x"},
		TS:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("emit payload.run_id entry: %v", err)
	}

	// A neighbouring run that must never be swept in.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineRunStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_y",
		Summary:     "a different run",
		TS:          now,
	}); err != nil {
		t.Fatalf("emit other run: %v", err)
	}

	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", RunID: "run_x"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		for _, e := range entries {
			t.Logf("got %s / actor=%s trace=%s", e.Type, e.ActorID, e.TraceID)
		}
		t.Fatalf("RunID must reach all three doors: want 3 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Summary == "a different run" {
			t.Fatalf("RunID swept in a neighbouring run: %q", e.Summary)
		}
	}
}

// RunID must not become a way to read another workspace's runs. Run ids are
// CUIDs and not guessable, but "not guessable" is not an authorisation
// boundary, and every other filter in this struct is workspace-scoped.
func TestList_RunIDStaysInsideWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	for _, ws := range []string{"ws_a", "ws_b"} {
		if _, err := w.Emit(ctx, Entry{
			WorkspaceID: ws,
			Type:        EntryPipelineRunStarted,
			ActorType:   ActorOrchestrator,
			ActorID:     "run_shared",
			Summary:     "run in " + ws,
			TS:          now,
		}); err != nil {
			t.Fatalf("emit %s: %v", ws, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := List(ctx, db, Query{WorkspaceID: "ws_a", RunID: "run_shared"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry scoped to ws_a, got %d", len(entries))
	}
	if entries[0].Summary != "run in ws_a" {
		t.Fatalf("crossed a workspace boundary: %q", entries[0].Summary)
	}
}

// RunID and TraceID are different questions — "everything in this run"
// versus "entries whose trace_id column equals this". Setting both must
// narrow, not widen, or a caller combining them silently gets more than
// they asked for.
func TestList_RunIDCombinesWithOtherFiltersAsAnd(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineStepStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_x",
		Summary:     "step started",
		TS:          now,
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPipelineRunFailed,
		ActorType:   ActorOrchestrator,
		ActorID:     "run_x",
		Severity:    SeverityError,
		Summary:     "run failed",
		TS:          now.Add(time.Second),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := List(ctx, db, Query{
		WorkspaceID: "ws_test",
		RunID:       "run_x",
		Severities:  []Severity{SeverityError},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("RunID must AND with Severities: want 1, got %d", len(entries))
	}
	if entries[0].Summary != "run failed" {
		t.Fatalf("wrong entry survived: %q", entries[0].Summary)
	}
}
