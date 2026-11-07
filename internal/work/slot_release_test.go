package work

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// oneBackgroundSlot admits one background run per agent, so a single held slot
// is visible as a refused claim.
func oneBackgroundSlot() Limits {
	l := DefaultLimits()
	l.AgentTotal, l.AgentBackground = 1, 1
	return l
}

// parkByLeaseLoss claims and starts the agent's next item and lets its lease
// expire, which parks it in needs_reconciliation with its runtime unverified.
func parkByLeaseLoss(t *testing.T, s *Store, clock *fixedClock) *Claimed {
	t.Helper()
	a, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "departed", Limits: oneBackgroundSlot()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseDuration + time.Second)
	if _, err := s.RecoverExpiredLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(t.Context(), a.Item.ID)
	if err != nil || item.State != StateNeedsReconciliation {
		t.Fatalf("not parked: %+v %v", item, err)
	}
	return a
}

func claimNext(t *testing.T, s *Store) (*Claimed, error) {
	t.Helper()
	return s.Claim(t.Context(), ClaimOptions{LeaseOwner: "next", Limits: oneBackgroundSlot()})
}

// #5108: a reconciliation item whose runtime end is confirmed gives its
// execution slot back. Nothing else about it changes: it stays in
// reconciliation, keeps its evidence, is not retried or replayed, and only a
// resolution at its generation ends it.
func TestConfirmedRuntimeEndReleasesReconciliationSlot(t *testing.T) {
	s, db, clock := newTestStore(t)
	parked := accept(t, s, db, backgroundReq("agent"))
	a := parkByLeaseLoss(t, s, clock)
	if a.Item.ID != parked.WorkID {
		t.Fatalf("claimed %s, want %s", a.Item.ID, parked.WorkID)
	}
	next := accept(t, s, db, backgroundReq("agent"))

	// Unverified: the runtime may be alive, the slot stays held.
	if got, err := claimNext(t, s); !errors.Is(err, ErrNoWork) {
		t.Fatalf("unconfirmed reconciliation released its slot: %+v %v", got, err)
	}

	// The protected capture proves the end (the caller validated it).
	code := 143
	if _, err := s.RecoverRetainedResult(t.Context(), a.RunID, RunResult{ExitCode: &code}, false); err != nil {
		t.Fatal(err)
	}
	var confirmed sql.NullString
	var status, evidence string
	var result sql.NullString
	if err := db.QueryRow(`SELECT runtime_end_confirmed_at,run_status,exit_evidence,run_result_json FROM work_attempts WHERE run_id=?`, a.RunID).
		Scan(&confirmed, &status, &evidence, &result); err != nil {
		t.Fatal(err)
	}
	if !confirmed.Valid || status != "FAILED" || evidence != "protected host capture" || !result.Valid {
		t.Fatalf("confirmation or evidence missing: confirmed=%v status=%q evidence=%q result=%v", confirmed, status, evidence, result.Valid)
	}

	got, err := claimNext(t, s)
	if err != nil || got.Item.ID != next.WorkID {
		t.Fatalf("confirmed end did not release the slot: %+v %v", got, err)
	}

	// The parked item itself is untouched: same state and generation, one
	// attempt, never queued or retried.
	item, err := s.Get(t.Context(), parked.WorkID)
	if err != nil || item.State != StateNeedsReconciliation || item.Generation != a.Generation {
		t.Fatalf("reconciliation changed: %+v %v", item, err)
	}
	var attempts, retries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_attempts WHERE work_id=?`, parked.WorkID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d %v", attempts, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_events WHERE work_id=? AND to_state IN ('queued','retry_wait')`, parked.WorkID).Scan(&retries); err != nil || retries != 1 {
		// The single queued event is the original acceptance.
		t.Fatalf("queued/retry events=%d %v", retries, err)
	}
	// Resolution still belongs to a person, at the generation.
	if err := s.Resolve(t.Context(), parked.WorkID, a.Generation, StateFailed, "operator", "effects checked"); err != nil {
		t.Fatal(err)
	}
}

// Only the capture-backed settlement confirms an end. A park reason, a lease
// loss or an operator note never does, and a refused settlement leaves the
// slot held.
func TestReconciliationWithoutConfirmedEndKeepsSlot(t *testing.T) {
	s, db, clock := newTestStore(t)
	accept(t, s, db, backgroundReq("agent"))
	a := parkByLeaseLoss(t, s, clock)
	accept(t, s, db, backgroundReq("agent"))

	// A settlement for another run does not touch this attempt.
	code := 1
	if owned, err := s.RecoverRetainedResult(t.Context(), "other-run", RunResult{ExitCode: &code}, false); owned || err != nil {
		t.Fatalf("foreign run: %v %v", owned, err)
	}
	if got, err := claimNext(t, s); !errors.Is(err, ErrNoWork) {
		t.Fatalf("slot released without a confirmed end: %+v %v", got, err)
	}
	var confirmed sql.NullString
	if err := db.QueryRow(`SELECT runtime_end_confirmed_at FROM work_attempts WHERE run_id=?`, a.RunID).Scan(&confirmed); err != nil || confirmed.Valid {
		t.Fatalf("confirmed without a capture: %v %v", confirmed, err)
	}
}

// The confirmation belongs to one attempt. A later generation of the same item
// that is parked again without a confirmed end holds the slot again: an old
// confirmation never stands in for a new runtime.
func TestRuntimeEndConfirmationDoesNotCarryToNextGeneration(t *testing.T) {
	s, db, clock := newTestStore(t)
	parked := accept(t, s, db, backgroundReq("agent"))
	a := parkByLeaseLoss(t, s, clock)
	code := 143
	if _, err := s.RecoverRetainedResult(t.Context(), a.RunID, RunResult{ExitCode: &code}, false); err != nil {
		t.Fatal(err)
	}
	// An operator decides the effects are safe to repeat.
	if err := s.Resolve(t.Context(), parked.WorkID, a.Generation, StateQueued, "operator", "safe to repeat"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	again := parkByLeaseLoss(t, s, clock)
	if again.Item.ID != parked.WorkID || again.Generation <= a.Generation {
		t.Fatalf("expected the next generation of the same item: %+v", again)
	}
	accept(t, s, db, backgroundReq("agent"))
	if got, err := claimNext(t, s); !errors.Is(err, ErrNoWork) {
		t.Fatalf("an earlier generation's confirmation released the new runtime's slot: %+v %v", got, err)
	}
}

// The conservative state-only answer is unchanged for callers that see only a
// state; the SQL rule is what the counting queries use.
func TestStateOnlySlotAnswerStaysConservative(t *testing.T) {
	if !StateNeedsReconciliation.HoldsExecutionSlot() {
		t.Fatal("state-only callers must keep treating reconciliation as holding a slot")
	}
}
