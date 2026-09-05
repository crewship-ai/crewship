package pipeline

// B13 (#2370, PRD-ISSUES-AND-ROUTINES-2026 §3.1) retired COMPLETED from
// missions.status in favor of DONE — a decision scoped to `missions` only.
// This file pins that pipeline_runs.status was NOT touched: MarkTerminal's
// CAS clause still guards the lowercase run vocabulary
// ('completed','failed','cancelled','interrupted','dry_run') exactly as it
// did before B13, so a second/late terminal write is still refused.
//
// See internal/api/b13_run_status_cas_pin_test.go for the assignments.status
// twin.

import (
	"context"
	"testing"
)

func TestMarkTerminal_RunStatusCASUntouchedByMissionStatusWordChange(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := store.Insert(ctx, &RunRecord{ID: "run_b13_cas", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo"}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_b13_cas", Status: RunStatusCompleted, Output: "first"}); err != nil {
		t.Fatalf("first MarkTerminal: %v", err)
	}
	got, err := store.Get(ctx, "run_b13_cas")
	if err != nil {
		t.Fatalf("get after first call: %v", err)
	}
	if got.Status != RunStatusCompleted {
		t.Fatalf("status after first call = %q, want %q", got.Status, RunStatusCompleted)
	}

	// A second call reporting something else entirely must be a no-op: the
	// CAS ("status NOT IN ('completed','failed','cancelled','interrupted',
	// 'dry_run')") already won on the first call and is untouched by the
	// missions.status vocabulary decision.
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_b13_cas", Status: RunStatusFailed, ErrorMessage: "late duplicate"}); err != nil {
		t.Fatalf("second MarkTerminal: %v", err)
	}
	got, err = store.Get(ctx, "run_b13_cas")
	if err != nil {
		t.Fatalf("get after second call: %v", err)
	}
	if got.Status != RunStatusCompleted {
		t.Errorf("status after second call = %q, want it still %q — the run-status CAS must be untouched by B13", got.Status, RunStatusCompleted)
	}
}
