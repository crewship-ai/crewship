package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

type sessionHumanTestAdapter struct {
	*causalExecutionAdapter
	sessionCalls int
}

func (a *sessionHumanTestAdapter) PrepareSessionTargets(ctx context.Context, cp []byte, change BatchChange, labels BatchCausalPrepareContext, human *protocol.SessionHumanPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	if human != nil {
		return BatchTargetCandidate{}, nil, ErrInvalid
	}
	a.sessionCalls++
	return a.PrepareCausalTargets(ctx, cp, change, labels)
}

// Actual FULL SQLite; adapter, source resolver and sealed authority are MOCK.
// This proves the writer/lifetime seam, not human admission or a native profile.
func TestSessionHumanResolverWriterLifetimeAndImmutableCapture(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	adapter := &sessionHumanTestAdapter{causalExecutionAdapter: s.config.Targets.Adapter.(*causalExecutionAdapter)}
	s.config.Targets.Adapter = adapter
	one := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
	var escaped SessionHumanPrepareAccess
	var escapedCtx context.Context
	called := 0
	s.config.Targets.SessionHumanPrepare = func(ctx context.Context, tx *sql.Tx, access SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
		called++
		if !access.FromStore(s) || access.FromStore(&BatchStore{}) {
			t.Fatal("wrong original store")
		}
		gotLease, err := access.Lease()
		if err != nil || gotLease != lease {
			t.Fatal("wrong lease", err)
		}
		change, err := access.Change()
		if err != nil || change.ID != one.ID {
			t.Fatal("wrong original", err)
		}
		change.Operations[0].ID = "caller-mutation"
		cp, err := access.CurrentCheckpoint()
		if err != nil || string(cp) != "0" {
			t.Fatal("wrong current state", err)
		}
		cp[0] = 'x'
		cp, err = access.CurrentCheckpoint()
		if err != nil || string(cp) != "0" {
			t.Fatal("checkpoint alias", err)
		}
		revision, err := access.DocumentRevision()
		if err != nil || revision != "0" {
			t.Fatal("wrong revision", err)
		}
		if _, _, err = access.LoadCheckpoint(context.Background(), "0"); err == nil {
			t.Fatal("foreign context loaded history")
		}
		full, digest, err := access.LoadCheckpoint(ctx, "0")
		if err != nil || string(full.Digest) != basis.Digest || digest != canonicalDigest([]byte("0")) {
			t.Fatal("wrong qualified original basis", err)
		}
		escaped = access
		escapedCtx = ctx
		return nil, nil // An actual agent has no human source context.
	}
	committer := sessionTestCommitter(t, s, lease, []BatchChange{one}, nil, fullTestOutput)
	// Configuration mutation cannot replace the callback captured by this
	// committer; a replacement owner/runtime needs a newly constructed one.
	s.config.Targets.SessionHumanPrepare = func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
		return nil, errors.New("not captured")
	}
	result, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{one}})
	if err != nil || result.Result.Revision != "1" || called != 1 || adapter.sessionCalls != 1 {
		t.Fatal("session resolver did not commit", err, called, adapter.sessionCalls)
	}
	if escaped.FromStore(s) {
		t.Fatal("escaped access stayed active")
	}
	if _, err = escaped.Lease(); err == nil {
		t.Fatal("escaped lease getter")
	}
	if _, err = escaped.Change(); err == nil {
		t.Fatal("escaped change getter")
	}
	if _, err = escaped.CurrentCheckpoint(); err == nil {
		t.Fatal("escaped checkpoint getter")
	}
	if _, err = escaped.DocumentRevision(); err == nil {
		t.Fatal("escaped revision getter")
	}
	if _, _, err = escaped.LoadCheckpoint(escapedCtx, "0"); err == nil {
		t.Fatal("escaped historical loader")
	}
	var zero SessionHumanPrepareAccess
	if _, _, err = zero.LoadCheckpoint(t.Context(), "0"); err == nil {
		t.Fatal("empty access loaded history")
	}
}

func TestSessionHumanResolverFailurePrecedesTerminalReadClassification(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	adapter := &sessionHumanTestAdapter{causalExecutionAdapter: s.config.Targets.Adapter.(*causalExecutionAdapter)}
	s.config.Targets.Adapter = adapter
	op := targetReadOp("a", "0")
	op.Reads[0].TargetID = "missing"
	change := testBatchChange(t, lease.Key, "change-a", "", op)
	called := false
	s.config.Targets.SessionHumanPrepare = func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
		called = true
		return nil, ErrInvalid
	}
	committer := sessionTestCommitter(t, s, lease, []BatchChange{change}, nil, fullTestOutput)
	result, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}})
	if err == nil || !called || adapter.sessionCalls != 0 || len(result.EncodedOutput) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
		t.Fatal("unqualified human source became a terminal read outcome", err, called)
	}
}

func TestSessionHumanResolverHistoricalReadBudgetAndOrdinaryIsolation(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	adapter := &sessionHumanTestAdapter{causalExecutionAdapter: s.config.Targets.Adapter.(*causalExecutionAdapter)}
	s.config.Targets.Adapter = adapter
	one := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
	b := targetReadOp("b", "0")
	b.Dependencies = []string{"a"}
	two := testBatchChange(t, lease.Key, "change-b", "", b)
	c := targetReadOp("c", "0")
	c.Dependencies = []string{"b"}
	three := testBatchChange(t, lease.Key, "change-c", "", c)
	initial := sessionTestCommitter(t, s, lease, []BatchChange{one, two}, nil, fullTestOutput)
	if _, err := initial.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{one, two}}); err != nil {
		t.Fatal(err)
	}
	called := 0
	s.config.Targets.SessionHumanPrepare = func(ctx context.Context, _ *sql.Tx, access SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
		called++
		for _, revision := range []string{"0", "1"} {
			if _, _, err := access.LoadCheckpoint(ctx, revision); err != nil {
				t.Fatal("allowed original/replay basis denied", err)
			}
		}
		if _, _, err := access.LoadCheckpoint(ctx, "1"); err != nil {
			t.Fatal("same revision consumed another slot", err)
		}
		_, _, err := access.LoadCheckpoint(ctx, "2")
		if !errors.Is(err, ErrBatchCapacity) {
			t.Fatal("third historical revision accepted", err)
		}
		return nil, err
	}
	next := sessionTestCommitter(t, s, lease, []BatchChange{three}, nil, fullTestOutput)
	if result, err := next.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{three}}); err == nil || len(result.EncodedOutput) != 0 || batchCount(t, s, "mooring_collab_operations") != 2 {
		t.Fatal("overbudget resolver committed", err)
	}
	if result, err := s.Commit(t.Context(), BatchRequest{lease, "2", []BatchChange{three}}); err != nil || result.Revision != "3" || called != 1 || adapter.sessionCalls != 0 {
		t.Fatal("ordinary causal path invoked session resolver", err, called, adapter.sessionCalls)
	}
}
