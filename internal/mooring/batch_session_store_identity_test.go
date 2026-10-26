package mooring

import (
	"context"
	"database/sql"
	"testing"
)

func TestSessionBatchKeepsOriginalStoreIdentityForCommitAndRetry(t *testing.T) {
	store, _, lease := targetFixture(t)
	enableCausalExecution(store, &causalExecutionAdapter{})
	change := testBatchChange(t, lease.Key, "identity-change", "", targetReadOp("identity-op", "0"))
	committer := sessionTestCommitter(t, store, lease, []BatchChange{change}, nil, nil)
	original := committer.authority
	calls := 0
	committer.authority = func(ctx context.Context, tx *sql.Tx, access SessionBatchAccess) error {
		if !access.Access.FromStore(store) {
			t.Error("sealed-processing copy lost initialized store identity")
			return ErrInvalid
		}
		calls++
		return original(ctx, tx, access)
	}
	request := sessionRequest(lease, change)
	for i := 0; i < 2; i++ {
		result, err := committer.Commit(t.Context(), request)
		if err != nil || result.Result.Revision != "1" || len(result.Result.Changes) != 1 || result.Result.Changes[0].Status != "saved" {
			t.Fatalf("commit/retry %d: %+v %v", i, result, err)
		}
	}
	if calls < 2 {
		t.Fatal("commit/retry did not consult host authority")
	}
}
