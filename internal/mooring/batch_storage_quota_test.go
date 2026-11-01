package mooring

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// A collaboration write that no longer fits the aggregate artifact budget is
// refused as artifact storage capacity (not as a generic store error), and it
// leaves no partial durable history.
func TestBatchCommitAggregateStorageRefusalIsExplicit(t *testing.T) {
	for _, scope := range []struct{ name, query string }{
		{"workspace", `UPDATE artifact_storage_workspaces SET max_bytes=used_bytes`},
		{"instance", `UPDATE artifact_storage_global SET max_bytes=used_bytes WHERE id=1`},
	} {
		t.Run(scope.name, func(t *testing.T) {
			s, _, lease := targetFixture(t)
			if _, err := s.db.Exec(scope.query); err != nil {
				t.Fatal(err)
			}
			change := testBatchChange(t, lease.Key, "full", "", targetReadOp("a", "0"))
			events := 0
			s.config.AfterCommit = func(BatchCommitEvent) { events++ }
			result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}})
			if !errors.Is(artifacts.StorageBudgetError(err), artifacts.ErrStorageQuota) || len(result.Changes) != 0 || events != 0 {
				t.Fatalf("aggregate refusal was not explicit: result=%+v err=%v events=%d", result, err, events)
			}
			if batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_changes") != 0 {
				t.Fatal("storage refusal left partial durable history")
			}
		})
	}
}
