package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type initializeFenceAdapter struct {
	batchTestAdapter
	validate func() error
	calls    int
}

func (a *initializeFenceAdapter) ValidateCheckpoint(context.Context, []byte) error {
	a.calls++
	if a.validate != nil {
		return a.validate()
	}
	return nil
}

func TestBatchInitializeDeniesBeforeCheckpointDecode(t *testing.T) {
	owners, db, _, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "initializer")
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("current original author denied")
	adapter := &initializeFenceAdapter{}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Adapter: adapter, Limits: DefaultBatchLimits(), Authority: func(context.Context, *sql.Tx, BatchAccess) error { return denied }})
	if err = store.Initialize(t.Context(), lease, []byte("untrusted malformed state")); !errors.Is(err, denied) || adapter.calls != 0 {
		t.Fatalf("denied checkpoint decoded: calls=%d err=%v", adapter.calls, err)
	}
}

func TestBatchInitializeFinalAuthorityRollsBackTriggerChanges(t *testing.T) {
	owners, db, _, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "initializer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`CREATE TABLE test_initialize_epoch(value INTEGER NOT NULL); INSERT INTO test_initialize_epoch VALUES(1);
 CREATE TRIGGER test_initialize_revoke AFTER INSERT ON mooring_collab_audit WHEN NEW.kind='initialized'
 BEGIN UPDATE test_initialize_epoch SET value=2; END;`); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("original authorization generation changed")
	adapter := &initializeFenceAdapter{}
	checks := 0
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Adapter: adapter, Limits: DefaultBatchLimits(), Authority: func(ctx context.Context, tx *sql.Tx, _ BatchAccess) error {
		checks++
		var epoch int
		if err := tx.QueryRowContext(ctx, `SELECT value FROM test_initialize_epoch`).Scan(&epoch); err != nil {
			return err
		}
		if epoch != 1 {
			return denied
		}
		return nil
	}})
	if err = store.Initialize(t.Context(), lease, []byte("state")); !errors.Is(err, denied) || checks != 2 {
		t.Fatalf("authority changed at commit: checks=%d err=%v", checks, err)
	}
	for _, table := range []string{"mooring_collab_documents", "mooring_collab_checkpoints", "mooring_collab_audit"} {
		if n := batchCount(t, store, table); n != 0 {
			t.Fatalf("%s survived aborted initialization: %d", table, n)
		}
	}
	var epoch int
	if err = db.DB.QueryRow(`SELECT value FROM test_initialize_epoch`).Scan(&epoch); err != nil || epoch != 1 {
		t.Fatalf("trigger effect survived rollback: epoch=%d err=%v", epoch, err)
	}
}
