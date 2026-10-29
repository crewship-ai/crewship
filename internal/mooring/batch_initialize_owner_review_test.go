package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestReviewInitializeOwnerExpiresDuringCheckpointValidationRollsBack(t *testing.T) {
	owners, db, clock, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "initializer")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &initializeFenceAdapter{validate: func() error { clock.advance(6 * time.Second); return nil }}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Adapter: adapter, Limits: DefaultBatchLimits(), Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }})
	if err = store.Initialize(t.Context(), lease, []byte("state")); !errors.Is(err, ErrOwnerStale) {
		t.Fatalf("expired owner initialized checkpoint: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatal("fixture did not reach checkpoint validation")
	}
	for _, table := range []string{"mooring_collab_documents", "mooring_collab_checkpoints", "mooring_collab_audit"} {
		if n := batchCount(t, store, table); n != 0 {
			t.Fatalf("%s survived expired owner: %d", table, n)
		}
	}
}
