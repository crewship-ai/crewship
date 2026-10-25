package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// Real SQL transactions with synthetic sealed ingress/app authority. These
// tests do not qualify an actual agent, HTTP authentication or browser Saved.
func TestSessionSavedRecorderSharesCommitAndDoesNotRepeat(t *testing.T) {
	s, _, owner := targetFixture(t)
	enableCausalExecution(s, &causalExecutionAdapter{})
	change := testBatchChange(t, owner.Key, "recorded", "", targetReadOp("recorded_op", "0"))
	base := sessionTestCommitter(t, s, owner, []BatchChange{change}, nil, nil)
	if _, err := s.db.Exec(`CREATE TABLE recorded_saved(change_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	committer, err := NewSessionCommitterWithSavedRecorder(s, owner, base.authority, base.output, func(ctx context.Context, tx *sql.Tx, c BatchChange, saved BatchChangeResult) error {
		calls++
		var alreadySaved bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_changes WHERE page_id=? AND change_id=?)`, owner.Key.ArtifactID, c.ID).Scan(&alreadySaved); err != nil {
			return err
		}
		if !alreadySaved || saved.Status != "saved" {
			return errors.New("metadata callback preceded actual saved row")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO recorded_saved VALUES(?)`, c.ID)
		// Mutation must not change ledger contents, output, or final author input.
		c.Operations[0].Payload = []byte(`{"forged":true}`)
		saved.Operations[0].ID = "forged"
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := committer.Commit(t.Context(), sessionRequest(owner, change))
		if err != nil || result.Result.Revision != "1" || result.Result.Changes[0].Operations[0].ID != "recorded_op" {
			t.Fatalf("saved/retry changed: %+v %v", result, err)
		}
	}
	if calls != 1 || batchCount(t, s, "recorded_saved") != 1 {
		t.Fatal("identical retry duplicated provenance", calls)
	}
}

func TestSessionSavedRecorderRollsBackWithEveryLaterFailure(t *testing.T) {
	for _, failure := range []string{"recorder", "output", "final_author", "commit"} {
		t.Run(failure, func(t *testing.T) {
			s, _, owner := targetFixture(t)
			enableCausalExecution(s, &causalExecutionAdapter{})
			change := testBatchChange(t, owner.Key, "rollback", "", targetReadOp("rollback_op", "0"))
			if _, err := s.db.Exec(`CREATE TABLE recorded_saved(change_id TEXT PRIMARY KEY); CREATE TABLE invalid_record(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
				t.Fatal(err)
			}
			recorded := false
			base := sessionTestCommitter(t, s, owner, []BatchChange{change}, func(SessionBatchAccess) error {
				if recorded && failure == "final_author" {
					return ErrInvalid
				}
				return nil
			}, nil)
			output := base.output
			if failure == "output" {
				output = func(context.Context, *sql.Tx, SessionPendingOutput) ([]byte, error) { return nil, ErrInvalid }
			}
			committer, err := NewSessionCommitterWithSavedRecorder(s, owner, base.authority, output, func(ctx context.Context, tx *sql.Tx, c BatchChange, _ BatchChangeResult) error {
				recorded = true
				if _, err := tx.ExecContext(ctx, `INSERT INTO recorded_saved VALUES(?)`, c.ID); err != nil {
					return err
				}
				if failure == "recorder" {
					return ErrInvalid
				}
				if failure == "commit" {
					_, err := tx.ExecContext(ctx, `INSERT INTO invalid_record VALUES('missing-human')`)
					return err
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := committer.Commit(t.Context(), sessionRequest(owner, change))
			if err == nil || !recorded || len(result.EncodedOutput) != 0 || len(result.Result.Changes) != 0 {
				t.Fatal("failure released a saved outcome", result, err, recorded)
			}
			if batchCount(t, s, "recorded_saved") != 0 || batchCount(t, s, "mooring_collab_changes") != 0 || batchCount(t, s, "invalid_record") != 0 {
				t.Fatal("failed transaction retained edit or attribution")
			}
		})
	}
}

func TestSessionSavedRecorderOmitsRejectedChanges(t *testing.T) {
	s, _, owner := targetFixture(t)
	enableCausalExecution(s, &causalExecutionAdapter{})
	change := testBatchChange(t, owner.Key, "rejected", "", targetReadOp("rejected_op", "0"))
	base := sessionTestCommitter(t, s, owner, []BatchChange{change}, func(SessionBatchAccess) error {
		return &BatchDenied{Code: "permission_revoked"}
	}, nil)
	called := false
	committer, err := NewSessionCommitterWithSavedRecorder(s, owner, base.authority, base.output, func(context.Context, *sql.Tx, BatchChange, BatchChangeResult) error {
		called = true
		return ErrInvalid
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := committer.Commit(t.Context(), sessionRequest(owner, change))
	if err != nil || called || result.Result.Changes[0].Status != "rejected" {
		t.Fatal("rejection recorded as an edit", result, err, called)
	}
}
