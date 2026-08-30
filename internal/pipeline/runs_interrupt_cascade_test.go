package pipeline

import (
	"context"
	"database/sql"
	"testing"
)

// A run that is interrupted must not leave an actionable approval behind — #2163.
//
// The failure this pins is not a crash. A run parked on a wait step gets
// marked `interrupted` (the definition drifted, resume was disabled, the
// process died mid-flight), and its waitpoint stays `pending`. The inbox keeps
// offering the decision, the approve endpoint accepts it and returns ok, the
// row flips to resolved — and the run is still interrupted, so the approved
// action never runs. The operator is told they approved a production change
// that did not happen, and the audit trail records the approval.
//
// CancelWaitpointsForRun already existed for exactly this ("used when a parked
// or blocking run is cancelled or dies"). It was wired to the explicit-cancel
// path and not to the interrupt path.
//
// The cascade lives on the status transition rather than at its call sites:
// resume.go marks runs interrupted from five places and the boot fallback from
// two more, and an invariant enforced at seven call sites is one the eighth
// will miss.

// interruptRig builds a store pair over a schema carrying pipeline_runs,
// pipeline_waitpoints and inbox_items, with the canceller wired as production
// wires it.
func interruptRig(t *testing.T) (*RunStore, *SQLWaitpointStore, *sql.DB) {
	t.Helper()
	db := openTrustGateTestDB(t)
	wp := NewSQLWaitpointStore(db)
	rs := NewRunStore(db)
	rs.SetWaitpointCanceller(wp)
	return rs, wp, db
}

// parkedRun seeds a run in `status` with one pending waitpoint against it, and
// returns the waitpoint token.
func parkedRun(t *testing.T, db *sql.DB, wp *SQLWaitpointStore, runID, status string) string {
	t.Helper()
	seedTrustRun(t, db, runID, "hash1", "")
	if _, err := db.ExecContext(t.Context(),
		`UPDATE pipeline_runs SET status = ? WHERE id = ?`, status, runID); err != nil {
		t.Fatalf("set run status: %v", err)
	}
	token, err := wp.CreateApproval(t.Context(), WaitpointApprovalRequest{
		WorkspaceID:   "ws_test",
		PipelineRunID: runID,
		StepID:        "approve",
		Prompt:        "Approve this production action?",
		Title:         "Delete the staging bucket",
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	return token
}

func waitpointRowStatus(t *testing.T, db *sql.DB, token string) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(t.Context(),
		`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&s); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	return s
}

func inboxState(t *testing.T, db *sql.DB, token string) string {
	t.Helper()
	var s string
	err := db.QueryRowContext(t.Context(),
		`SELECT state FROM inbox_items WHERE source_id = ?`, token).Scan(&s)
	if err == sql.ErrNoRows {
		return "(no row)"
	}
	if err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	return s
}

func TestMarkInterrupted_CancelsPendingWaitpoints(t *testing.T) {
	t.Parallel()
	rs, wp, db := interruptRig(t)
	token := parkedRun(t, db, wp, "run_interrupt_one", "waiting")

	if got := waitpointRowStatus(t, db, token); got != "pending" {
		t.Fatalf("precondition: waitpoint status = %q, want pending", got)
	}

	if err := rs.MarkInterrupted(t.Context(), "run_interrupt_one", "definition changed since run started"); err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}

	if got := waitpointRowStatus(t, db, token); got != "cancelled" {
		t.Errorf("waitpoint status = %q, want cancelled — an interrupted run must not leave an approvable gate", got)
	}
	// The inbox row is the surface the human actually clicks, so the
	// projection has to move too. A cancelled waitpoint under an unread
	// blocking inbox row is the same bug wearing a different hat.
	if got := inboxState(t, db, token); got != "resolved" {
		t.Errorf("inbox row state = %q, want resolved", got)
	}
}

func TestRecoverInterruptedAtBoot_CancelsPendingWaitpoints(t *testing.T) {
	t.Parallel()
	rs, wp, db := interruptRig(t)
	// The bulk path only promotes queued/running, which is the state a run
	// killed mid-step is left in.
	token := parkedRun(t, db, wp, "run_interrupt_bulk", "running")

	n, err := rs.RecoverInterruptedAtBoot(t.Context())
	if err != nil {
		t.Fatalf("RecoverInterruptedAtBoot: %v", err)
	}
	if n == 0 {
		t.Fatal("precondition: no rows promoted")
	}

	if got := waitpointRowStatus(t, db, token); got != "cancelled" {
		t.Errorf("waitpoint status = %q, want cancelled (bulk boot fallback path)", got)
	}
	if got := inboxState(t, db, token); got != "resolved" {
		t.Errorf("inbox row state = %q, want resolved", got)
	}
}

// The cascade must follow the status write, not merely accompany it.
// MarkInterrupted is guarded on status so a run that finished between the
// resume scan's read and this write is never clobbered — and in that case its
// waitpoints must be left exactly as they are.
func TestMarkInterrupted_NoCascadeWhenTheGuardRefuses(t *testing.T) {
	t.Parallel()
	rs, wp, db := interruptRig(t)
	token := parkedRun(t, db, wp, "run_already_done", "completed")

	if err := rs.MarkInterrupted(t.Context(), "run_already_done", "stale scan"); err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}

	var status string
	if err := db.QueryRowContext(t.Context(),
		`SELECT status FROM pipeline_runs WHERE id = ?`, "run_already_done").Scan(&status); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "completed" {
		t.Fatalf("precondition: the guard should have refused, run status = %q", status)
	}
	if got := waitpointRowStatus(t, db, token); got != "pending" {
		t.Errorf("waitpoint status = %q, want pending — the run was never interrupted, so nothing should have cascaded", got)
	}
}

// The canceller is optional: RunStore is constructed without one in tests and
// in any wiring that has no waitpoint store, and must keep working.
func TestMarkInterrupted_WithoutCancellerIsANoOp(t *testing.T) {
	t.Parallel()
	db := openTrustGateTestDB(t)
	wp := NewSQLWaitpointStore(db)
	rs := NewRunStore(db) // deliberately not wired
	token := parkedRun(t, db, wp, "run_no_canceller", "waiting")

	if err := rs.MarkInterrupted(t.Context(), "run_no_canceller", "no canceller wired"); err != nil {
		t.Fatalf("MarkInterrupted must not fail without a canceller: %v", err)
	}
	if got := waitpointRowStatus(t, db, token); got != "pending" {
		t.Errorf("waitpoint status = %q, want pending — nothing is wired to cancel it", got)
	}
}

// A run with no waitpoints at all is the common case; the cascade must not
// turn it into an error or a write.
func TestMarkInterrupted_RunWithoutWaitpoints(t *testing.T) {
	t.Parallel()
	rs, _, db := interruptRig(t)
	seedTrustRun(t, db, "run_plain", "hash1", "")
	if _, err := db.ExecContext(context.Background(),
		`UPDATE pipeline_runs SET status = 'running' WHERE id = ?`, "run_plain"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if err := rs.MarkInterrupted(t.Context(), "run_plain", "nothing parked"); err != nil {
		t.Errorf("MarkInterrupted on a run with no waitpoints: %v", err)
	}
}

// Orphans predating the cascade must be repaired, not merely stopped from
// recurring — #2163.
//
// The cascade above fixes runs interrupted from now on. Every install that
// upgrades still carries the rows that already have the defect: a terminal run
// with a pending waitpoint, which is an approvable gate on a run that can
// never act on it. Fixing forward only would leave the reported symptom live
// exactly where it already bit.
//
// The invariant is unconditional: a waitpoint is reachable only while its run
// can still resume. Once the run is terminal, no decision on that gate can
// ever execute, whatever put the run there.
func TestCancelOrphanedWaitpoints(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		runStatus  string
		wantCancel bool
	}{
		{"interrupted run", "interrupted", true},
		{"failed run", "failed", true},
		{"completed run", "completed", true},
		{"cancelled run", "cancelled", true},
		// Still live: the gate is legitimately someone's to decide.
		{"waiting run", "waiting", false},
		{"running run", "running", false},
		{"queued run", "queued", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTrustGateTestDB(t)
			wp := NewSQLWaitpointStore(db)
			runID := "run_orphan_" + tc.runStatus
			token := parkedRun(t, db, wp, runID, tc.runStatus)

			n, err := wp.CancelOrphanedWaitpoints(t.Context())
			if err != nil {
				t.Fatalf("CancelOrphanedWaitpoints: %v", err)
			}

			got := waitpointRowStatus(t, db, token)
			if tc.wantCancel {
				if got != "cancelled" {
					t.Errorf("run %s: waitpoint = %q, want cancelled (the run can never act on it)", tc.runStatus, got)
				}
				if n != 1 {
					t.Errorf("run %s: reported %d cancelled, want 1", tc.runStatus, n)
				}
				if st := inboxState(t, db, token); st != "resolved" {
					t.Errorf("run %s: inbox row = %q, want resolved", tc.runStatus, st)
				}
			} else {
				if got != "pending" {
					t.Errorf("run %s: waitpoint = %q, want pending (the run is still live)", tc.runStatus, got)
				}
				if n != 0 {
					t.Errorf("run %s: reported %d cancelled, want 0", tc.runStatus, n)
				}
			}
		})
	}
}

// Running it twice must not double-count or re-resolve — it runs on every boot.
func TestCancelOrphanedWaitpoints_Idempotent(t *testing.T) {
	t.Parallel()
	db := openTrustGateTestDB(t)
	wp := NewSQLWaitpointStore(db)
	parkedRun(t, db, wp, "run_orphan_twice", "interrupted")

	first, err := wp.CancelOrphanedWaitpoints(t.Context())
	if err != nil || first != 1 {
		t.Fatalf("first pass: n=%d err=%v, want 1/nil", first, err)
	}
	second, err := wp.CancelOrphanedWaitpoints(t.Context())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second != 0 {
		t.Errorf("second pass cancelled %d, want 0 — the sweep must be idempotent", second)
	}
}

// A waitpoint whose run row is missing entirely (hard-deleted history) is also
// unreachable, and must not be left behind or crash the sweep.
func TestCancelOrphanedWaitpoints_MissingRunRow(t *testing.T) {
	t.Parallel()
	db := openTrustGateTestDB(t)
	wp := NewSQLWaitpointStore(db)
	token := parkedRun(t, db, wp, "run_vanishes", "waiting")
	if _, err := db.ExecContext(t.Context(), `DELETE FROM pipeline_runs WHERE id = ?`, "run_vanishes"); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	if _, err := wp.CancelOrphanedWaitpoints(t.Context()); err != nil {
		t.Fatalf("CancelOrphanedWaitpoints with a missing run row: %v", err)
	}
	if got := waitpointRowStatus(t, db, token); got != "cancelled" {
		t.Errorf("waitpoint = %q, want cancelled — there is no run left to act on it", got)
	}
}
