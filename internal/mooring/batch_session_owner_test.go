package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSessionBatchCommitterCannotAdoptReplacementOwner(t *testing.T) {
	s, _, oldOwner := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	change := testBatchChange(t, oldOwner.Key, "sealed", "", targetReadOp("sealed", "0"))
	calls := 0
	// Deliberately permissive MOCK seal callback isolates the independent exact
	// owner-binding guard. Production callbacks must also verify captured proof.
	authority := func(context.Context, *sql.Tx, SessionBatchAccess) error { calls++; return nil }
	output := func(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
		return json.Marshal(p.Result)
	}
	old, err := NewSessionCommitter(s, oldOwner, authority, output)
	if err != nil {
		t.Fatal(err)
	}
	s.config.Owners.clock.(*ownerTestClock).advance(6 * time.Second)
	replacement, err := s.config.Owners.Acquire(t.Context(), oldOwner.Key, "replacement-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation <= oldOwner.Generation {
		t.Fatal("owner generation did not advance")
	}
	request := sessionRequest(replacement, change)
	result, err := old.Commit(t.Context(), request)
	if !errors.Is(err, ErrInvalid) || len(result.Result.Changes) != 0 || calls != 0 || a.causalCalls != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
		t.Fatal("old committer adopted replacement owner", err, "authority calls", calls, "engine calls", a.causalCalls)
	}
	fresh, err := NewSessionCommitter(s, replacement, authority, output)
	if err != nil {
		t.Fatal(err)
	}
	result, err = fresh.Commit(t.Context(), request)
	if err != nil || result.Result.Revision != "1" || result.Result.Changes[0].Status != "saved" {
		t.Fatal("new bound committer rejected current owner", result, err)
	}
}
