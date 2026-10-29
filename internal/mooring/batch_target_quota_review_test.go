package mooring

import (
	"errors"
	"testing"
)

func TestBatchTargetQuotaIncludesLaterRejectedDependencyHistory(t *testing.T) {
	s, _, lease := targetFixture(t)
	first := testBatchChange(t, lease.Key, "first", "", targetReadOp("a", "0"))
	laterOp := targetReadOp("b", "0")
	laterOp.Dependencies = []string{"a"}
	later := testBatchChange(t, lease.Key, "later-rejected", "", laterOp)
	row := preparedTargetRow{Before: &BatchTargetVersion{TargetID: "p", Revision: "0", StructureRevision: "0"}, After: BatchTargetVersion{TargetID: "p", Revision: "1", StructureRevision: "0"}, Effect: BatchTargetEffect{TargetID: "p", ContentChanged: true}, OperationID: "a", Revision: "1"}
	record, err := targetEffectRecord(lease.Key, row)
	if err != nil {
		t.Fatal(err)
	}
	// First accepted operation exactly fills the quota. The later operation
	// becomes stale after it, but its immutable rejected dependencies also count.
	s.config.Targets.Limits.ProvenanceBytes = int64(len(record) + 2)
	events := 0
	s.config.AfterCommit = func(BatchCommitEvent) { events++ }
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{first, later}})
	if !errors.Is(err, ErrBatchCapacity) || len(result.Changes) != 0 || events != 0 {
		var used int64
		_ = s.db.QueryRow(`SELECT (SELECT COALESCE(SUM(length(CAST(dependencies_json AS BLOB))),0) FROM mooring_collab_operations)+(SELECT COALESCE(SUM(length(CAST(record_json AS BLOB))),0) FROM mooring_collab_target_effects)`).Scan(&used)
		t.Fatalf("later rejected history exceeded quota: result=%+v error=%v events=%d used=%d limit=%d", result, err, events, used, s.config.Targets.Limits.ProvenanceBytes)
	}
	if batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_target_effects") != 0 || batchCount(t, s, "mooring_collab_checkpoints") != 1 {
		t.Fatal("quota refusal left partial durable history")
	}
}
