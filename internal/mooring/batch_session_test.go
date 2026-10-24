package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// MOCK ingress seals and app/clock only. No API authentication, live runtime or
// FIFO transport qualification is implied by these actual FULL-SQL tests.
func sessionTestCommitter(t *testing.T, s *BatchStore, lease OwnerLease, changes []BatchChange, write func(SessionBatchAccess) error, output SessionOutputPreparer) *SessionCommitter {
	t.Helper()
	normalized, err := normalizeBatch(BatchRequest{lease, "0", changes})
	if err != nil {
		t.Fatal(err)
	}
	sealed := map[string]BatchOperation{}
	membership := map[string]sessionMembership{}
	for _, c := range normalized {
		for _, op := range c.Operations {
			sealed[op.ID] = op
			membership[op.ID] = sessionMembership{c.ID, c.MembershipHash}
		}
	}
	authority := func(_ context.Context, _ *sql.Tx, a SessionBatchAccess) error {
		if a.Access.Scope != lease.Key || a.Lease != lease {
			return ErrInvalid
		}
		if a.Access.Operation == nil {
			return ErrInvalid
		}
		original, ok := sealed[a.Access.Operation.ID]
		if membership[a.Access.Operation.ID] != (sessionMembership{a.ChangeID, a.MembershipHash}) {
			return ErrInvalid
		}
		if !ok || !reflect.DeepEqual(original, *a.Access.Operation) {
			return ErrInvalid
		}
		if a.Purpose == SessionProcessSealedOutcome {
			return nil
		}
		if a.Purpose != SessionCurrentWrite {
			return ErrInvalid
		}
		if write != nil {
			return write(a)
		}
		return nil
	}
	if output == nil {
		output = func(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
			return json.Marshal(p.Result)
		}
	}
	committer, err := NewSessionCommitter(s, lease, authority, output)
	if err != nil {
		t.Fatal(err)
	}
	return committer
}
func sessionRequest(lease OwnerLease, changes ...BatchChange) SessionBatchRequest {
	return SessionBatchRequest{Lease: lease, Basis: SessionBasis{"0", canonicalDigest([]byte("0"))}, Changes: changes}
}
func TestSessionBatchDerivesCurrentHeadInsideWriterWithoutRewritingQueuedBasis(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	first := testBatchChange(t, lease.Key, "first", "", targetReadOp("a", "0"))
	b := targetReadOp("b", "0")
	b.Dependencies = []string{"a"}
	second := testBatchChange(t, lease.Key, "second", "", b)
	committer := sessionTestCommitter(t, s, lease, []BatchChange{first, second}, nil, nil)
	queued := sessionRequest(lease, second)
	before, _ := json.Marshal(queued)
	entered, release := make(chan struct{}), make(chan struct{})
	a.mutateCausal = func(_ *BatchTargetCandidate) {
		if a.causalCalls == 1 {
			close(entered)
			<-release
		}
	}
	type completion struct {
		result SessionBatchResult
		err    error
	}
	doneA, doneB := make(chan completion, 1), make(chan completion, 1)
	go func() { r, e := committer.Commit(t.Context(), sessionRequest(lease, first)); doneA <- completion{r, e} }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first never entered writer-bound app")
	}
	go func() { r, e := committer.Commit(t.Context(), queued); doneB <- completion{r, e} }()
	select {
	case r := <-doneB:
		t.Fatal("queued operation passed active SQL writer", r.err)
	default:
	}
	close(release)
	rA, rB := <-doneA, <-doneB
	if rA.err != nil || rB.err != nil || rA.result.Result.Revision != "1" || rB.result.Result.Revision != "2" || rB.result.Result.Changes[0].Status != "saved" {
		t.Fatal(rA.err, rB.err, rA.result.Result, rB.result.Result)
	}
	after, _ := json.Marshal(queued)
	if string(before) != string(after) || a.seen[1].Operations[0].Reads[0].Revision != "0" || a.contexts[1].DocumentRevision != "1" {
		t.Fatal("immutable basis/read rewritten", a.contexts)
	}
	// Old public transport semantics remain exact-head equality.
	old := testBatchChange(t, lease.Key, "old-mode", "", targetReadOp("old", "2"))
	oldResult, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{old}})
	if err != nil || oldResult.Changes[0].Operations[0].Reason.Code != "precondition_failed" {
		t.Fatal("public Commit silently became queued", oldResult, err)
	}
}
func TestSessionBatchRevokedSealedAuthorRejectsAndIndependentAuthorSaves(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	jana := targetReadOp("jana", "0")
	jana.Participant = "jana"
	dependent := targetReadOp("dependent", "0")
	dependent.Participant = "agent"
	dependent.Dependencies = []string{"jana"}
	independent := targetReadOp("independent", "0")
	independent.Participant = "agent"
	changes := []BatchChange{testBatchChange(t, lease.Key, "jana-change", "", jana), testBatchChange(t, lease.Key, "dependent-change", "", dependent), testBatchChange(t, lease.Key, "independent-change", "", independent)}
	c := sessionTestCommitter(t, s, lease, changes, func(a SessionBatchAccess) error {
		if a.Access.Participant == "jana" {
			return &BatchDenied{Code: "permission_revoked"}
		}
		return nil
	}, nil)
	s.config.Authority = func(context.Context, *sql.Tx, BatchAccess) error { return errors.New("external reader revoked") }
	result, err := c.Commit(t.Context(), sessionRequest(lease, changes...))
	if err != nil || result.Result.Revision != "1" || result.Result.Changes[0].Operations[0].Reason.Code != "permission_revoked" || result.Result.Changes[1].Operations[0].Reason.RootOperationID != "jana" || result.Result.Changes[2].Status != "saved" || a.causalCalls != 1 {
		t.Fatal(result.Result, err, a.causalCalls)
	}
	if _, err = s.Status(t.Context(), lease.Key, "independent", changes[2].Operations[0].Fingerprint); err == nil {
		t.Fatal("internal processing weakened external current-reader check")
	}
	unsealed := testBatchChange(t, lease.Key, "invented", "", targetReadOp("invented", "1"))
	if r, e := c.Commit(t.Context(), sessionRequest(lease, unsealed)); e == nil || len(r.Result.Changes) != 0 || batchCount(t, s, "mooring_collab_operations") != 3 {
		t.Fatal("child invented an operation", r, e)
	}
}
func TestSessionBatchBasisMustBeRetainedExactAtomicCheckpoint(t *testing.T) {
	for _, kind := range []string{"digest", "future", "missing-boundary", "metadata", "huge-text"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := targetFixture(t)
			a := &causalExecutionAdapter{}
			enableCausalExecution(s, a)
			op := testBatchChange(t, lease.Key, "op", "", targetReadOp("op", "0"))
			c := sessionTestCommitter(t, s, lease, []BatchChange{op}, nil, nil)
			request := sessionRequest(lease, op)
			switch kind {
			case "digest":
				request.Basis.Digest = "sha256:" + strings.Repeat("0", 64)
			case "future":
				request.Basis.Revision = "1"
			case "missing-boundary":
				if _, e := s.db.Exec(`DELETE FROM mooring_collab_checkpoints WHERE revision='0'`); e != nil {
					t.Fatal(e)
				}
			case "metadata":
				if _, e := s.db.Exec(`UPDATE mooring_collab_checkpoints SET decoded_bytes=1000000000`); e != nil {
					t.Fatal(e)
				}
			case "huge-text":
				if _, e := s.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_base64=CAST(zeroblob(4000000) AS TEXT)`); e != nil {
					t.Fatal(e)
				}
			}
			if r, e := c.Commit(t.Context(), request); e == nil || len(r.Result.Changes) != 0 || a.causalCalls != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
				t.Fatal("bad basis became terminal", r, e, a.causalCalls)
			}
		})
	}
}
func TestSessionBatchOutputCapacityAndLateOwnerExpiryRollbackBeforeSaved(t *testing.T) {
	for _, kind := range []string{"oversize", "owner-expiry"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := targetFixture(t)
			a := &causalExecutionAdapter{}
			enableCausalExecution(s, a)
			op := testBatchChange(t, lease.Key, "op", "", targetReadOp("op", "0"))
			c := sessionTestCommitter(t, s, lease, []BatchChange{op}, nil, func(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
				if p.Result.Changes[0].Status != "saved" {
					t.Fatal("no speculative candidate to budget")
				}
				if kind == "oversize" {
					return make([]byte, sessionOutputBytes+1), nil
				}
				s.config.Owners.clock.(*ownerTestClock).advance(6 * time.Second)
				return json.Marshal(p.Result)
			})
			var events int
			s.config.AfterCommit = func(BatchCommitEvent) { events++ }
			if r, e := c.Commit(t.Context(), sessionRequest(lease, op)); e == nil || len(r.EncodedOutput) != 0 || len(r.Result.Changes) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_checkpoints") != 1 || events != 0 {
				t.Fatal("uncommitted result escaped", r, e, events)
			}
		})
	}
}
func TestSessionBatchAllKnownRetryStillRequiresCurrentOwnerAfterOutputWork(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	op := testBatchChange(t, lease.Key, "op", "", targetReadOp("op", "0"))
	expire := false
	c := sessionTestCommitter(t, s, lease, []BatchChange{op}, nil, func(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
		if expire {
			s.config.Owners.clock.(*ownerTestClock).advance(6 * time.Second)
		}
		return json.Marshal(p.Result)
	})
	if _, e := c.Commit(t.Context(), sessionRequest(lease, op)); e != nil {
		t.Fatal(e)
	}
	expire = true
	if r, e := c.Commit(t.Context(), sessionRequest(lease, op)); e == nil || len(r.EncodedOutput) != 0 || a.causalCalls != 1 || batchCount(t, s, "mooring_collab_operations") != 1 {
		t.Fatal("old Saved replay escaped dead owner", r, e)
	}
}

func TestSessionBatchLateCanonicalDenialAndOutputAliasCannotEscapeCommit(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	op := testBatchChange(t, lease.Key, "op", "", targetReadOp("op", "0"))
	revoked := false
	c := sessionTestCommitter(t, s, lease, []BatchChange{op}, func(SessionBatchAccess) error {
		if revoked {
			return &BatchDenied{Code: "permission_revoked"}
		}
		return nil
	}, func(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
		p.Result.Changes[0].Operations[0].Status = "forged"
		p.Checkpoint[0] = '9'
		revoked = true
		return []byte(`{"candidate":true}`), nil
	})
	if r, e := c.Commit(t.Context(), sessionRequest(lease, op)); e == nil || len(r.EncodedOutput) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
		t.Fatal("late canonical denial passed final fence", r, e)
	}
	revoked = false
	c = sessionTestCommitter(t, s, lease, []BatchChange{op}, nil, func(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
		p.Result.Changes[0].Operations[0].Status = "forged"
		p.Checkpoint[0] = '9'
		return []byte(`{"bounded":true}`), nil
	})
	result, err := c.Commit(t.Context(), sessionRequest(lease, op))
	if err != nil || result.Result.Changes[0].Operations[0].Status != "saved" {
		t.Fatal("callback mutated actual receipts", result, err)
	}
	receipt, err := s.Status(t.Context(), lease.Key, "op", op.Operations[0].Fingerprint)
	if err != nil || receipt.Status != "saved" {
		t.Fatal("stored receipt aliased callback", receipt, err)
	}
	if _, err = c.Commit(t.Context(), sessionRequest(lease, testBatchChange(t, lease.Key, "repacked", "", op.Operations...))); !errors.Is(err, ErrInvalid) {
		t.Fatal("original op was repackaged under unsealed membership", err)
	}
}
