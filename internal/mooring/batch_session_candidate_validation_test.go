package mooring

import (
	"context"
	"database/sql"
	"errors"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"strings"
	"testing"
)

// MOCK application, original seals and source/namespace validator; actual FULL
// SQLite. No numeric origin or this fixture grants production author authority.
func TestSessionNativeCandidateValidationBeforeSaveAndFinalFences(t *testing.T) {
	for _, mode := range []string{"commit", "validator-error", "output-rollback", "duplicate-output", "final-revoke"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			adapter := &sessionHumanTestAdapter{causalExecutionAdapter: s.config.Targets.Adapter.(*causalExecutionAdapter)}
			s.config.Targets.Adapter = adapter
			change := testBatchChange(t, lease.Key, "native-a", "", targetReadOp("a", "0"))
			s.config.Targets.SessionHumanPrepare = func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
				return nil, nil
			}
			var escaped SessionNativeCandidateAccess
			burned := false
			revoked := false
			s.config.Targets.SessionNativeCandidate = func(ctx context.Context, tx *sql.Tx, access SessionNativeCandidateAccess) error {
				if !access.FromStore(s) || access.FromStore(&BatchStore{}) {
					t.Fatal("candidate has foreign store")
				}
				owner, err := access.Lease()
				if err != nil || owner != lease {
					t.Fatal("wrong owner", err)
				}
				candidate, err := access.Candidate()
				if err != nil || candidate.Change.ID != change.ID || candidate.Revision != "1" || candidate.StateDigest != canonicalDigest([]byte("1")) || candidate.NativeDelta.WriterOrigin != "31" {
					t.Fatal("not exact actual candidate", candidate, err)
				}
				var count int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_operations`).Scan(&count); err != nil || count != 0 {
					t.Fatal("candidate validation ran after save", count, err)
				}
				candidate.Change.Operations[0].ID = "mutated"
				candidate.NativeDelta.Targets[0] = "mutated"
				again, _ := access.Candidate()
				if again.Change.Operations[0].ID != "a" || again.NativeDelta.Targets[0] != "p" {
					t.Fatal("candidate aliases")
				}
				escaped = access
				burned = true
				revoked = mode == "final-revoke"
				if mode == "validator-error" {
					return ErrInvalid
				}
				return nil
			}
			output := func(ctx context.Context, tx *sql.Tx, p SessionPendingOutput) ([]byte, error) {
				if _, err := escaped.Candidate(); err == nil {
					t.Fatal("candidate callback remained active in serializer")
				}
				if mode == "output-rollback" {
					return nil, errors.New("injected rollback")
				}
				raw, err := fullTestOutput(ctx, tx, p)
				if mode == "duplicate-output" {
					raw = []byte(strings.Replace(string(raw), `"type":"session_commit_result"`, `"type":"session_commit_result","type":"session_commit_result"`, 1))
				}
				return raw, err
			}
			committer := sessionTestCommitter(t, s, lease, []BatchChange{change}, func(SessionBatchAccess) error {
				if revoked {
					return &BatchDenied{Code: "permission_revoked"}
				}
				return nil
			}, output)
			// The callback is captured with the original store; later config mutation
			// cannot install a permissive callback or bypass the namespace check.
			s.config.Targets.SessionNativeCandidate = func(context.Context, *sql.Tx, SessionNativeCandidateAccess) error {
				t.Fatal("uncaptured callback used")
				return nil
			}
			result, err := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}})
			if !burned {
				t.Fatal("candidate validator was skipped", err)
			}
			if escaped.FromStore(s) {
				t.Fatal("candidate borrow escaped")
			}
			if _, err := escaped.Lease(); err == nil {
				t.Fatal("owner borrow escaped")
			}
			switch mode {
			case "commit":
				if err != nil || result.Result.Revision != "1" || len(result.NativeDeltas) != 1 {
					t.Fatal("commit", result, err)
				}
			case "final-revoke":
				if err != nil || result.Result.Revision != "0" || result.Result.Changes[0].Status != "rejected" || len(result.NativeDeltas) != 0 {
					t.Fatal("final source check omitted", result, err)
				}
			default:
				if err == nil || len(result.NativeDeltas) != 0 || len(result.SavedCuts) != 0 || len(result.EncodedOutput) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
					t.Fatal("failed validation leaked outcome", result, err)
				}
			}
			// The simulated host namespace burn remains true even on SQL rollback.
			if !burned {
				t.Fatal("rollback reused source origin")
			}
		})
	}
}
func TestSessionNativeCandidateValidatorCannotBeMissing(t *testing.T) {
	s, lease, _ := fullBasisFixture(t)
	s.config.Targets.SessionHumanPrepare = func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
		return nil, nil
	}
	if _, err := NewSessionCommitter(s, lease, func(context.Context, *sql.Tx, SessionBatchAccess) error { return nil }, fullTestOutput); err == nil {
		t.Fatal("native session omitted candidate validation")
	}
}
