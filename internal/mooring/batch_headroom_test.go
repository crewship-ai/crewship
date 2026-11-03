package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/diskusage"
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

// Review of #4835: the pre-engine probe can only estimate from the current
// checkpoint. A small checkpoint that prepares into a large candidate must be
// probed again with the real candidate before any history or head row: free
// space here covers the estimate but not the candidate.
func TestBatchCommitLargeCandidateBelowFreeDiskReserveWritesNothing(t *testing.T) {
	s, adapter, _, _, lease := batchFixture(t)
	before := mooringCollabRows(t, s)
	large := append([]byte("1"), bytes.Repeat([]byte(" "), 3<<19)...)
	adapter.apply = func(context.Context, []byte, BatchChange) ([]byte, *PreparedFailure, error) {
		return large, nil, nil
	}
	lowFreeDisk(t, s.db, false)
	var dbBytes int64
	if err := s.db.QueryRow(`SELECT (SELECT page_count FROM pragma_page_count)*(SELECT page_size FROM pragma_page_size)`).Scan(&dbBytes); err != nil {
		t.Fatal(err)
	}
	// Enough for the database copy, the small estimate and 1 MiB of slack for
	// ledger rows; far less than the ~4 MiB the encoded candidate needs twice.
	free := uint64(dbBytes) + 1<<20
	restore := storageheadroom.SetUsageForTest(func(string) (diskusage.Stats, error) {
		return diskusage.Stats{FreeBytes: free}, nil
	})
	defer restore()
	change := testBatchChange(t, lease.Key, "large", "", testBatchOp("large"))
	events := 0
	s.config.AfterCommit = func(BatchCommitEvent) { events++ }
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}})
	wantFreeDiskRefusal(t, "large candidate", err)
	if adapter.calls.Load() != 1 {
		t.Fatalf("the pre-engine estimate should admit the small checkpoint; engine calls=%d", adapter.calls.Load())
	}
	if len(result.Changes) != 0 || events != 0 {
		t.Fatalf("refused commit leaked: %+v events=%d", result, events)
	}
	if got := mooringCollabRows(t, s); got != before {
		t.Fatalf("refused candidate wrote rows: before=%v after=%v", before, got)
	}
	if revision, checkpoint, err := s.ReadCheckpoint(t.Context(), lease.Key); err != nil || revision != "0" || string(checkpoint) != "0" {
		t.Fatalf("confirmed checkpoint changed: %q %d bytes %v", revision, len(checkpoint), err)
	}
	free = 1 << 62
	if r, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}}); err != nil || r.Revision != "1" {
		t.Fatalf("same commit after space returned = %+v %v", r, err)
	}
}

// Review round 2 of #4835: free disk can shrink while the engine runs and the
// engine can answer with a terminal rejection. That rejection is persisted
// rows too, so it must pass the real-size probe: refused, nothing durable,
// and the same request records its rejection once space returns.
func TestBatchCommitRejectionBelowFreeDiskReserveWritesNothing(t *testing.T) {
	s, adapter, _, _, lease := batchFixture(t)
	lowFreeDisk(t, s.db, false)
	before := mooringCollabRows(t, s)
	var restore func()
	defer func() {
		if restore != nil {
			restore()
		}
	}()
	adapter.apply = func(_ context.Context, _ []byte, c BatchChange) ([]byte, *PreparedFailure, error) {
		if restore == nil {
			restore = storageheadroom.SetUsageForTest(func(string) (diskusage.Stats, error) { return diskusage.Stats{}, nil })
		}
		return nil, &PreparedFailure{c.Operations[0].ID, batchReason("precondition_failed")}, nil
	}
	change := testBatchChange(t, lease.Key, "rejected", "", testBatchOp("rejected"))
	events := 0
	s.config.AfterCommit = func(BatchCommitEvent) { events++ }
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}})
	wantFreeDiskRefusal(t, "terminal rejection", err)
	if adapter.calls.Load() != 1 || len(result.Changes) != 0 || events != 0 {
		t.Fatalf("refused rejection leaked: calls=%d result=%+v events=%d", adapter.calls.Load(), result, events)
	}
	if got := mooringCollabRows(t, s); got != before {
		t.Fatalf("refused rejection wrote rows: before=%v after=%v", before, got)
	}
	restore()
	restore = func() {}
	result, err = s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}})
	if err != nil || len(result.Changes) != 1 || result.Changes[0].Status != "rejected" || result.Revision != "0" {
		t.Fatalf("same request after space returned = %+v %v", result, err)
	}
}

// persistedBytes sums every column of every row in the ledger tables a
// change writes, so the estimate can be compared with real stored bytes.
func persistedBytes(t *testing.T, s *BatchStore) int64 {
	t.Helper()
	var total int64
	for _, table := range []string{"mooring_collab_documents", "mooring_collab_checkpoints", "mooring_collab_changes", "mooring_collab_operations", "mooring_collab_audit", "mooring_collab_target_versions", "mooring_collab_target_effects"} {
		rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		expr := "0"
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			expr += `+coalesce(length(CAST("` + name + `" AS BLOB)),0)`
		}
		rows.Close()
		var n int64
		if err = s.db.QueryRow(`SELECT coalesce(sum(` + expr + `),0) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		total += n
	}
	return total
}

// The real-size estimate is at least the bytes a saved and a rejected change
// actually store (document head rewrite counted as growth).
func TestChangeDiskBytesCoversPersistedRows(t *testing.T) {
	s, adapter, _, _, lease := batchFixture(t)
	candidate := append([]byte("1"), bytes.Repeat([]byte(" "), 64<<10)...)
	adapter.apply = func(context.Context, []byte, BatchChange) ([]byte, *PreparedFailure, error) {
		return candidate, nil, nil
	}
	op := testBatchOp("estimate-saved")
	op.Dependencies = []string{}
	saved := testBatchChange(t, lease.Key, "estimate-saved", "", op)
	before := persistedBytes(t, s)
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{saved}})
	if err != nil || len(result.Changes) != 1 || result.Changes[0].Status != "saved" {
		t.Fatal(result, err)
	}
	grown := persistedBytes(t, s) - before
	estimate, err := changeDiskBytes(lease.Key, saved, result.Changes[0], candidate, nil, nil)
	if err != nil || estimate < grown {
		t.Fatalf("saved estimate %d < persisted %d (%v)", estimate, grown, err)
	}

	adapter.apply = func(_ context.Context, _ []byte, c BatchChange) ([]byte, *PreparedFailure, error) {
		return nil, &PreparedFailure{c.Operations[0].ID, batchReason("precondition_failed")}, nil
	}
	rop := testBatchOp("estimate-rejected")
	rop.BaseRevision = "1"
	rejected := testBatchChange(t, lease.Key, "estimate-rejected", "", rop)
	before = persistedBytes(t, s)
	result, err = s.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{rejected}})
	if err != nil || len(result.Changes) != 1 || result.Changes[0].Status != "rejected" {
		t.Fatal(result, err)
	}
	grown = persistedBytes(t, s) - before
	estimate, err = changeDiskBytes(lease.Key, rejected, result.Changes[0], nil, nil, nil)
	if err != nil || estimate < grown || grown <= 0 {
		t.Fatalf("rejected estimate %d < persisted %d (%v)", estimate, grown, err)
	}
}
