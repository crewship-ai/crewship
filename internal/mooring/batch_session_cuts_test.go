package mooring

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Actual FULL SQLite transactions; application, clock and original submission
// seals are MOCK fixtures. These tests do not qualify native ingress or delivery.
func TestSessionSavedCutsActualAtomicOrderAndKnownReplay(t *testing.T) {
	for _, first := range []string{"c", "a"} {
		t.Run(first, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			app := s.config.Targets.Adapter.(*causalExecutionAdapter)
			app.mutateCausal = func(candidate *BatchTargetCandidate) { candidate.Checkpoint = []byte(strconv.Itoa(app.causalCalls)) }
			second := "a"
			if first == "a" {
				second = "c"
			}
			one := testBatchChange(t, lease.Key, "change-"+first, "", targetReadOp(first, "0"))
			op := targetReadOp(second, "0")
			op.Dependencies = []string{first}
			two := testBatchChange(t, lease.Key, "change-"+second, "", op)
			op = targetReadOp("b", "0")
			op.Dependencies = []string{second}
			three := testBatchChange(t, lease.Key, "change-b", "", op)
			committer := sessionTestCommitter(t, s, lease, []BatchChange{one, two, three}, nil, fullTestOutput)
			// Reverse request order: dependency planning, not request position,
			// determines the actual per-atomic checkpoint cuts.
			r, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{two, one}})
			want := []SessionSavedCut{
				{one.ID, one.MembershipHash, "1", canonicalDigest([]byte("1"))},
				{two.ID, two.MembershipHash, "2", canonicalDigest([]byte("2"))},
			}
			if err != nil || !reflect.DeepEqual(r.SavedCuts, want) {
				t.Fatalf("cuts=%+v want=%+v err=%v", r.SavedCuts, want, err)
			}
			// Known Saved uses retained revision 1 while fresh B produces 3.
			r.SavedCuts[0].StateDigest = "caller mutation"
			r, err = committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{three, one}})
			want[1] = SessionSavedCut{three.ID, three.MembershipHash, "3", canonicalDigest([]byte("3"))}
			if err != nil || !reflect.DeepEqual(r.SavedCuts, want) || app.causalCalls != 3 {
				t.Fatalf("retry cuts=%+v err=%v calls=%d", r.SavedCuts, err, app.causalCalls)
			}
		})
	}
}

func TestSessionSavedCutsNeverEscapeFailedCommit(t *testing.T) {
	for _, mode := range []string{"callback", "deferred-commit", "owner-expired", "source-revoked"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			if mode == "deferred-commit" {
				if _, err := s.db.Exec(`CREATE TABLE cut_parent(id INTEGER PRIMARY KEY); CREATE TABLE cut_child(parent INTEGER REFERENCES cut_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
					t.Fatal(err)
				}
			}
			change := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
			denied := false
			write := func(SessionBatchAccess) error {
				if denied {
					return &BatchDenied{Code: "permission_revoked"}
				}
				return nil
			}
			output := func(ctx context.Context, tx *sql.Tx, pending SessionPendingOutput) ([]byte, error) {
				if mode == "callback" {
					return nil, errors.New("uncertain output")
				}
				raw, err := fullTestOutput(ctx, tx, pending)
				if err != nil {
					return nil, err
				}
				switch mode {
				case "deferred-commit":
					_, err = tx.ExecContext(ctx, `INSERT INTO cut_child(parent) VALUES(987)`)
				case "owner-expired":
					s.config.Owners.clock.(*ownerTestClock).advance(6 * time.Second)
				case "source-revoked":
					denied = true
				}
				return raw, err
			}
			committer := sessionTestCommitter(t, s, lease, []BatchChange{change}, write, output)
			r, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}})
			if err == nil || !reflect.DeepEqual(r, SessionBatchResult{}) || batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_checkpoints") != 1 {
				t.Fatalf("provisional cut/output escaped: %+v err=%v", r, err)
			}
		})
	}
}

func TestSessionSavedCutsWholeMultiOperationAtomic(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	app := s.config.Targets.Adapter.(*causalExecutionAdapter)
	app.mutateCausal = func(candidate *BatchTargetCandidate) { candidate.Checkpoint = []byte(strconv.Itoa(app.causalCalls)) }
	a, b := targetReadOp("a", "0"), targetReadOp("b", "0")
	b.Dependencies = []string{"a"}
	whole := testBatchChange(t, lease.Key, "whole", "", a, b)
	c := targetReadOp("c", "0")
	c.Dependencies = []string{"b"}
	peer := testBatchChange(t, lease.Key, "peer", "", c)
	committer := sessionTestCommitter(t, s, lease, []BatchChange{whole, peer}, nil, fullTestOutput)
	r, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{peer, whole}})
	want := []SessionSavedCut{
		{whole.ID, whole.MembershipHash, "2", canonicalDigest([]byte("1"))},
		{peer.ID, peer.MembershipHash, "3", canonicalDigest([]byte("2"))},
	}
	if err != nil || !reflect.DeepEqual(r.SavedCuts, want) {
		t.Fatalf("atomic cut was split or relabeled: %+v err=%v", r.SavedCuts, err)
	}
}

func TestSessionSavedCutsMissingRetainedCutIsNonterminal(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	one := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
	op := targetReadOp("b", "0")
	op.Dependencies = []string{"a"}
	two := testBatchChange(t, lease.Key, "change-b", "", op)
	committer := sessionTestCommitter(t, s, lease, []BatchChange{one, two}, nil, fullTestOutput)
	if _, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{one, two}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM mooring_collab_checkpoints WHERE page_id=? AND revision='1'`, lease.Key.ArtifactID); err != nil {
		t.Fatal(err)
	}
	r, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{one}})
	if err == nil || !reflect.DeepEqual(r, SessionBatchResult{}) || batchCount(t, s, "mooring_collab_operations") != 2 {
		t.Fatalf("missing cut fabricated: %+v err=%v", r, err)
	}
}
