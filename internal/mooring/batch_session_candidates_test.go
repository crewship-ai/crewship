package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"testing"
)

func TestSessionPreparedCandidatesSameWriterNeverSavedBeforeCommit(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			s.config.Targets.SessionNativeCandidate = func(context.Context, *sql.Tx, SessionNativeCandidateAccess) error { return nil }
			s.config.Targets.Adapter = &sessionHumanTestAdapter{causalExecutionAdapter: s.config.Targets.Adapter.(*causalExecutionAdapter)}
			a := testBatchChange(t, lease.Key, "candidate-a", "", targetReadOp("a", "0"))
			op := targetReadOp("b", "0")
			op.Dependencies = []string{"a"}
			b := testBatchChange(t, lease.Key, "candidate-b", "", op)
			called := 0
			var escaped SessionHumanPrepareAccess
			s.config.Targets.SessionHumanPrepare = func(ctx context.Context, tx *sql.Tx, access SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
				p, err := access.PreparedCandidates()
				if err != nil {
					t.Fatal(err)
				}
				if p.TransactionRevision != "0" || p.TransactionStateDigest != canonicalDigest([]byte("0")) || len(p.Candidates) != called {
					t.Fatal("wrong transaction boundary/prefix", p, called)
				}
				if called == 1 {
					if _, _, e := access.LoadCheckpoint(ctx, "1"); e == nil {
						t.Fatal("provisional checkpoint exposed as durable history")
					}

					e := p.Candidates[0]
					if e.Change.ID != a.ID || e.Change.MembershipHash != a.MembershipHash || e.Revision != "1" || e.StateDigest != canonicalDigest([]byte("1")) || e.NativeDelta.Update != "AAA=" {
						t.Fatal("candidate was not concrete earlier prepare", e)
					}
					e.Change.Operations[0].ID = "mutation"
					p.Candidates[0].NativeDelta.Targets[0] = "mutation"
					again, _ := access.PreparedCandidates()
					if again.Candidates[0].Change.Operations[0].ID != "a" || again.Candidates[0].NativeDelta.Targets[0] != "p" {
						t.Fatal("candidate aliases")
					}
				}
				called++
				escaped = access
				return nil, nil
			}
			output := func(ctx context.Context, tx *sql.Tx, pending SessionPendingOutput) ([]byte, error) {
				if rollback {
					return nil, errors.New("injected output failure")
				}
				return sessionNativeTestOutput(ctx, tx, pending)
			}
			c := sessionTestCommitter(t, s, lease, []BatchChange{a, b}, nil, output)
			r, err := c.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{b, a}})
			if called != 2 {
				t.Fatal("missing ordered candidate", called, err)
			}
			if _, err := escaped.PreparedCandidates(); err == nil {
				t.Fatal("escaped prepared borrow")
			}
			if rollback {
				if err == nil || len(r.NativeDeltas) != 0 || len(r.SavedCuts) != 0 || len(r.EncodedOutput) != 0 {
					t.Fatal("provisional data escaped", r, err)
				}
				var count int
				if err := s.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations`).Scan(&count); err != nil || count != 0 {
					t.Fatal("rollback", count, err)
				}
				return
			}
			if err != nil || r.Result.Revision != "2" || len(r.NativeDeltas) != 2 || len(r.SavedCuts) != 2 {
				t.Fatal("missing committed deltas/cuts", r, err)
			}
			var wire protocol.SessionCommitResult
			if json.Unmarshal(r.EncodedOutput, &wire) != nil || wire.NativeDeltas == nil || len(*wire.NativeDeltas) != 2 {
				t.Fatal("missing encoded committed deltas")
			}
			for i, d := range *wire.NativeDeltas {
				if string(d.ChangeID) != r.NativeDeltas[i].ChangeID || string(d.CheckpointStateDigest) != r.NativeDeltas[i].StateDigest {
					t.Fatal("encoded cut mismatch")
				}
			}
			for i, d := range r.NativeDeltas {
				if d.ChangeID != []string{a.ID, b.ID}[i] || d.Revision != []string{"1", "2"}[i] || d.StateDigest != r.SavedCuts[i].StateDigest {
					t.Fatal("wrong committed cut", d)
				}
			}
		})
	}
}
func TestSessionNativeDeltaRejectsInventedAllocationAndTargets(t *testing.T) {
	effects := []BatchOperationEffects{{OperationID: "a", Targets: []BatchTargetEffect{{TargetID: "p", ContentChanged: true}}}}
	base := protocol.SessionNativeDelta{WriterOrigin: "31", AllocationOrigins: []protocol.PositiveCounter{}, Update: "AAA=", Targets: []protocol.Identifier{"p"}}
	for _, mutate := range []func(*protocol.SessionNativeDelta){
		func(d *protocol.SessionNativeDelta) { d.WriterOrigin = "0" }, func(d *protocol.SessionNativeDelta) { d.WriterOrigin = "9007199254740992" },
		func(d *protocol.SessionNativeDelta) { d.AllocationOrigins = []protocol.PositiveCounter{"32"} }, func(d *protocol.SessionNativeDelta) { d.AllocationOrigins = []protocol.PositiveCounter{"31", "31"} },
		func(d *protocol.SessionNativeDelta) { d.Targets = []protocol.Identifier{"q"} }, func(d *protocol.SessionNativeDelta) { d.Update = "AAB=" },
	} {
		raw, _ := json.Marshal(base)
		var d protocol.SessionNativeDelta
		_ = json.Unmarshal(raw, &d)
		mutate(&d)
		if _, err := freezeSessionNativeDelta(&d, effects); err == nil {
			t.Fatal("invalid native data accepted", d)
		}
	}
	if d, err := freezeSessionNativeDelta(&base, effects); err != nil || d.AllocationOrigins == nil {
		t.Fatal("zero-allocation delta lost", err)
	}
}
