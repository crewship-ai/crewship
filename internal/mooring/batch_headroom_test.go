package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

func lowFreeDisk(t *testing.T, db *sql.DB, low bool) {
	t.Helper()
	reserve := int64(0)
	if low {
		reserve = int64(1) << 62
	}
	if _, err := db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, reserve); err != nil {
		t.Fatal(err)
	}
}

func mooringCollabRows(t *testing.T, s *BatchStore) [6]int {
	t.Helper()
	var n [6]int
	for i, table := range []string{"mooring_collab_documents", "mooring_collab_checkpoints", "mooring_collab_changes", "mooring_collab_operations", "mooring_collab_tasks", "mooring_collab_audit"} {
		n[i] = batchCount(t, s, table)
	}
	return n
}

func wantFreeDiskRefusal(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, storageheadroom.ErrInsufficient) || !errors.Is(artifacts.StorageBudgetError(err), artifacts.ErrStorageQuota) {
		t.Fatalf("%s below reserve = %v, want the shared storage refusal", what, err)
	}
}

// #4835: Mooring collaboration Initialize below the free-disk reserve writes
// nothing; the owner lease it runs under stays usable.
func TestBatchInitializeBelowFreeDiskReserveWritesNothing(t *testing.T) {
	owners, db, _, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	s := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Adapter: &batchTestAdapter{}, Limits: DefaultBatchLimits()})
	lowFreeDisk(t, db.DB, true)
	wantFreeDiskRefusal(t, "initialize", s.Initialize(t.Context(), lease, []byte("0")))
	if got := mooringCollabRows(t, s); got != [6]int{} {
		t.Fatalf("refused initialize wrote rows %v", got)
	}
	lowFreeDisk(t, db.DB, false)
	if err = s.Initialize(t.Context(), lease, []byte("0")); err != nil {
		t.Fatalf("initialize after space returned: %v", err)
	}
}

// #4835: a fresh batch below the reserve is refused before the engine applies
// it, writes nothing and keeps the confirmed checkpoint. Reads, status of a
// confirmed operation, an exact retry, and the owner lease lifecycle
// (renew, release, reacquire) keep working while the reserve is low.
func TestBatchCommitBelowFreeDiskReserveWritesNothingAndKeepsLeaseAndReads(t *testing.T) {
	s, adapter, owners, _, lease := batchFixture(t)
	confirmed := testBatchChange(t, lease.Key, "confirmed", "", testBatchOp("a"))
	first, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{confirmed}})
	if err != nil || first.Revision != "1" {
		t.Fatal(first, err)
	}
	before := mooringCollabRows(t, s)
	calls := adapter.calls.Load()
	lowFreeDisk(t, s.db, true)

	op := testBatchOp("b")
	op.BaseRevision = "1"
	fresh := testBatchChange(t, lease.Key, "fresh", "", op)
	events := 0
	s.config.AfterCommit = func(BatchCommitEvent) { events++ }
	result, err := s.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{fresh}})
	wantFreeDiskRefusal(t, "commit", err)
	if len(result.Changes) != 0 || events != 0 || adapter.calls.Load() != calls {
		t.Fatalf("refused commit leaked: result=%+v events=%d engine calls=%d->%d", result, events, calls, adapter.calls.Load())
	}
	if got := mooringCollabRows(t, s); got != before {
		t.Fatalf("refused commit wrote rows: before=%v after=%v", before, got)
	}
	revision, checkpoint, err := s.ReadCheckpoint(t.Context(), lease.Key)
	if err != nil || revision != "1" || string(checkpoint) != "1" {
		t.Fatalf("read below reserve = %q %q %v", revision, checkpoint, err)
	}
	if status, err := s.Status(t.Context(), lease.Key, "a", confirmed.Operations[0].Fingerprint); err != nil || status == nil || status.Status != "saved" {
		t.Fatalf("status below reserve = %+v %v", status, err)
	}
	if retry, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{confirmed}}); err != nil || len(retry.Changes) != 1 || retry.Changes[0].Status != "saved" {
		t.Fatalf("exact retry of a confirmed change below reserve = %+v %v", retry, err)
	}
	renewed, err := owners.Renew(t.Context(), lease)
	if err != nil {
		t.Fatalf("lease renew below reserve: %v", err)
	}
	if err = owners.Release(t.Context(), renewed); err != nil {
		t.Fatalf("lease release below reserve: %v", err)
	}
	lease, err = owners.Acquire(t.Context(), lease.Key, "runtime")
	if err != nil {
		t.Fatalf("lease acquire below reserve: %v", err)
	}
	wantFreeDiskRefusal(t, "task registration", s.RegisterTask(t.Context(), lease, "task-low", "person", false))
	if got := mooringCollabRows(t, s); got != before {
		t.Fatalf("refused task registration wrote rows: before=%v after=%v", before, got)
	}

	lowFreeDisk(t, s.db, false)
	if err = s.RegisterTask(t.Context(), lease, "task-low", "person", false); err != nil {
		t.Fatalf("task registration after space returned: %v", err)
	}
	if r, err := s.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{fresh}}); err != nil || r.Revision != "2" {
		t.Fatalf("same commit after space returned = %+v %v", r, err)
	}
}
