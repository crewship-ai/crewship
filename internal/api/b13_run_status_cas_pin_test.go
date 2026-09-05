package api

// B13 (#2370, PRD-ISSUES-AND-ROUTINES-2026 §3.1) retired COMPLETED from
// missions.status in favor of DONE — a decision scoped to `missions` only.
// This file pins that assignments.status was NOT touched: finishAssignment's
// terminal CAS ("status NOT IN ('COMPLETED','FAILED','CANCELLED')",
// internal/api/assignments_run.go) still guards the run vocabulary exactly
// as it did before B13, so a second/late terminal write is still refused.
//
// See internal/pipeline/b13_run_status_cas_pin_test.go for the
// pipeline_runs.status twin.

import (
	"context"
	"testing"
)

func TestFinishAssignment_RunStatusCASUntouchedByMissionStatusWordChange(t *testing.T) {
	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)

	asgID := "asg-b13-cas"
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		asgID, wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	if ok := h.finishAssignment(context.Background(), asgID, "", chatID, "asg-worker", wsID, "first result", "", nil); !ok {
		t.Fatal("first finishAssignment should have won the terminal CAS")
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, asgID).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "COMPLETED" {
		t.Fatalf("status after first call = %q, want COMPLETED", status)
	}

	// A second, later call reporting FAILED must be a no-op: the CAS
	// ("status NOT IN ('COMPLETED','FAILED','CANCELLED')") already won on
	// the first call and is untouched by the missions.status vocabulary
	// decision — assignments keep COMPLETED, never DONE.
	if ok := h.finishAssignment(context.Background(), asgID, "", chatID, "asg-worker", wsID, "", "late duplicate failure", nil); ok {
		t.Error("second finishAssignment should have lost the terminal CAS (row already terminal)")
	}
	if err := h.db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, asgID).Scan(&status); err != nil {
		t.Fatalf("query assignment after second call: %v", err)
	}
	if status != "COMPLETED" {
		t.Errorf("status after second call = %q, want it still COMPLETED — the run-status CAS must be untouched by B13", status)
	}
}
