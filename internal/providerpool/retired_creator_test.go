package providerpool

import (
	"errors"
	"testing"
	"time"
)

func TestStoreAnonymizedRetiredMemberLeavesActiveAccountAvailable(t *testing.T) {
	store, db, _ := poolFixture(t)
	if err := store.Create(t.Context(), fixturePool()); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Choose(t.Context(), "ws", "pool", time.Now()); err != nil || got.ID != "a" {
		t.Fatalf("healthy calibration: %+v %v", got, err)
	}
	// Real registered migrations permit anonymous retired history while keeping
	// live creator custody mandatory. No schema edits or synthetic owner IDs.
	execPoolSQL(t, db, `UPDATE credentials SET deleted_at='2026-10-19T00:00:00Z' WHERE id='b'`)
	execPoolSQL(t, db, `UPDATE credentials SET created_by=NULL WHERE id='b'`)
	var erased bool
	if err := db.QueryRow(`SELECT created_by IS NULL AND encrypted_value='' AND deleted_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM credential_fields WHERE credential_id='b') FROM credentials WHERE id='b'`).Scan(&erased); err != nil || !erased {
		t.Fatalf("native anonymous tombstone calibration: erased=%v err=%v", erased, err)
	}
	got, err := store.Choose(t.Context(), "ws", "pool", time.Now())
	if err != nil || got.ID != "a" {
		t.Fatalf("anonymous retired history blocked live account: id=%q err=%v", got.ID, err)
	}
	_, candidates, err := store.Snapshot(t.Context(), "ws", "pool")
	if err != nil || len(candidates) != 1 || candidates[0].ID != "a" {
		t.Fatalf("anonymous tombstone remained a candidate: %+v %v", candidates, err)
	}
	execPoolSQL(t, db, `UPDATE credentials SET deleted_at='2026-10-19T00:00:00Z' WHERE id='a'`)
	if _, err := store.Choose(t.Context(), "ws", "pool", time.Now()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("anonymous tombstone became selectable: %v", err)
	}
}
