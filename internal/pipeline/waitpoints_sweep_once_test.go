package pipeline

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// waitpoints.go — sweepOnce.
//
// Internal method invoked by the background timeout loop. It scans
// pipeline_waitpoints for pending rows whose timeout_at has passed
// and flips them to 'timed_out' (status + decided_at). Sibling
// methods (CreateApproval / CompleteApproval / WaitFor /
// RecoverPending) are exercised by waitpoints_test.go but sweepOnce
// itself is private and has no direct test — yet it carries critical
// safety logic:
//
//   1. RowsAffected==0 gate: if CompleteApproval flipped the row
//      between sweep's SELECT and UPDATE, the cascade MUST NOT
//      re-fire (would deliver wrong outcome to a WaitFor goroutine
//      and resolve the inbox row with a stale "timed_out" action)
//   2. LIMIT 200 batch: bounded scan keeps a long backlog from
//      stalling other operations on the same DB
//   3. Listener notification: a WaitFor goroutine waiting on the
//      token MUST receive waitDecision{approved: false}
//
// All three are observable but only via private-method access from
// inside the package — hence this in-package test file.
// ---------------------------------------------------------------------------

// seedPendingWaitpoint inserts a row directly so the test controls
// timeout_at exactly. Bypasses CreateApproval to avoid creating an
// inbox row that the test would have to special-case.
func seedPendingWaitpoint(t *testing.T, s *SQLWaitpointStore, token string, timeoutAt time.Time) {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO pipeline_waitpoints (
    token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at
) VALUES (?, 'ws_test', 'run_1', 'step_1', 'approval', 'pending', ?)`,
		token, timeoutAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed waitpoint %s: %v", token, err)
	}
}

func waitpointStatus(t *testing.T, s *SQLWaitpointStore, token string) string {
	t.Helper()
	var status string
	if err := s.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&status); err != nil {
		t.Fatalf("read status %s: %v", token, err)
	}
	return status
}

func TestSweepOnce_FlipsExpiredPending_LeavesFreshAlone(t *testing.T) {
	// Two rows: one with timeout_at in the past (must flip), one
	// with timeout_at in the future (must stay pending). Pin both
	// directions — a regression in the WHERE clause that dropped the
	// timeout filter would flip valid live waitpoints.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	seedPendingWaitpoint(t, store, "tok-expired", time.Now().Add(-1*time.Hour))
	seedPendingWaitpoint(t, store, "tok-fresh", time.Now().Add(1*time.Hour))

	store.sweepOnce()

	if got := waitpointStatus(t, store, "tok-expired"); got != "timed_out" {
		t.Errorf("expired waitpoint status = %q, want \"timed_out\"", got)
	}
	if got := waitpointStatus(t, store, "tok-fresh"); got != "pending" {
		t.Errorf("fresh waitpoint status = %q, want \"pending\" (timeout_at in future must not flip)", got)
	}
}

func TestSweepOnce_DecidedAtSetToNow(t *testing.T) {
	// Source: `SET status='timed_out', decided_at = ?` — the same
	// `now` value used in the SELECT comparison. Pin that decided_at
	// is non-empty after the sweep, otherwise the audit timeline
	// would render "Timed out at unknown".
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	seedPendingWaitpoint(t, store, "tok-stamped", time.Now().Add(-1*time.Hour))
	store.sweepOnce()

	var decidedAt string
	if err := store.db.QueryRow(`SELECT COALESCE(decided_at, '') FROM pipeline_waitpoints WHERE token = ?`, "tok-stamped").Scan(&decidedAt); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if decidedAt == "" {
		t.Error("decided_at empty after sweep; audit timeline would show \"unknown\"")
	}
}

func TestSweepOnce_NonPendingRow_NotTouched(t *testing.T) {
	// WHERE status='pending' — already-terminal rows must NOT be
	// re-touched. A regression that dropped the status filter would
	// re-stamp decided_at on every sweep, breaking audit ordering.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	// Seed a row already in approved state with an old decided_at.
	originalDecidedAt := "2026-01-01T00:00:00.000000000Z"
	_, err := store.db.ExecContext(context.Background(), `
INSERT INTO pipeline_waitpoints
(token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at, decided_at)
VALUES ('tok-already', 'ws_test', 'run_1', 'step_1', 'approval', 'approved', ?, ?)`,
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano), originalDecidedAt)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	store.sweepOnce()

	if got := waitpointStatus(t, store, "tok-already"); got != "approved" {
		t.Errorf("already-approved row got reflipped to %q (sweep ignored status filter)", got)
	}
	var decidedAt string
	if err := store.db.QueryRow(`SELECT decided_at FROM pipeline_waitpoints WHERE token = 'tok-already'`).Scan(&decidedAt); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if decidedAt != originalDecidedAt {
		t.Errorf("decided_at was overwritten: got %q, want preserved %q", decidedAt, originalDecidedAt)
	}
}

func TestSweepOnce_NotifiesListener(t *testing.T) {
	// The cascade signals s.listeners[tok] with waitDecision{approved: false}.
	// A WaitFor goroutine relies on this to wake up from a sweep-driven
	// timeout. We register a listener manually (mirroring what
	// CreateApproval would do) and assert delivery.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	seedPendingWaitpoint(t, store, "tok-listened", time.Now().Add(-1*time.Hour))

	ch := make(chan waitDecision, 1)
	store.mu.Lock()
	store.listeners["tok-listened"] = ch
	store.mu.Unlock()

	store.sweepOnce()

	select {
	case d := <-ch:
		if d.approved {
			t.Errorf("listener received approved=true; sweep must deliver approved=false on timeout")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("listener did not receive timeout notification after sweepOnce")
	}
}

func TestSweepOnce_NoListener_DoesNotCrash(t *testing.T) {
	// Defensive: a sweep can fire for a row whose listener was
	// already deregistered (WaitFor returned via ctx.Done). Source
	// uses `select { case ch <- ...: default: }` so missing listener
	// is fine, but pin that the no-listener-at-all path also works.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	seedPendingWaitpoint(t, store, "tok-no-listener", time.Now().Add(-1*time.Hour))
	// No listener registered.

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sweepOnce without listener panicked: %v", r)
		}
	}()
	store.sweepOnce()

	if got := waitpointStatus(t, store, "tok-no-listener"); got != "timed_out" {
		t.Errorf("row not flipped: %q", got)
	}
}

func TestSweepOnce_NoRows_NoOp(t *testing.T) {
	// Empty table → SELECT returns 0 rows → no UPDATE, no listener
	// signal, no cascade. Function must return cleanly.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sweepOnce on empty table panicked: %v", r)
		}
	}()
	store.sweepOnce()
}

func TestSweepOnce_LimitedTo200_PerInvocation(t *testing.T) {
	// `LIMIT 200` in the SELECT — pin so a regression that dropped
	// the limit doesn't let a 100k-row backlog stall the sweep.
	// Insert 201 expired rows, run one sweep, verify 200 flipped and
	// 1 still pending.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	for i := 0; i < 201; i++ {
		seedPendingWaitpoint(t, store, "tok-many-"+itoa(i), time.Now().Add(-1*time.Hour))
	}

	store.sweepOnce()

	var flippedCount, pendingCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'timed_out'`).Scan(&flippedCount); err != nil {
		t.Fatalf("count flipped: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&pendingCount); err != nil {
		t.Fatalf("count pending: %v", err)
	}

	if flippedCount != 200 {
		t.Errorf("flipped = %d, want 200 (LIMIT 200 must apply)", flippedCount)
	}
	if pendingCount != 1 {
		t.Errorf("pending = %d, want 1 (the row beyond the limit must stay)", pendingCount)
	}

	// Second sweep picks up the remaining row.
	store.sweepOnce()
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&pendingCount); err != nil {
		t.Fatalf("recount pending: %v", err)
	}
	if pendingCount != 0 {
		t.Errorf("after second sweep, pending = %d, want 0", pendingCount)
	}
}

// itoa is a tiny strconv-free int-to-string for test tokens. Avoids
// pulling strconv just for fixture generation; the existing
// waitpoints_test.go avoids extra imports the same way.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
