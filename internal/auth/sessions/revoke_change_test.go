package sessions

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRevokeWithChangePreservesRepeatContract(t *testing.T) {
	db := newTestDB(t)
	store := NewDBStore(db)
	session, err := store.Create(t.Context(), "u1", "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := store.RevokeWithChange(t.Context(), session.ID, ReasonLogout)
	if err != nil || !changed {
		t.Fatalf("first transition: %v %v", changed, err)
	}
	changed, err = store.RevokeWithChange(t.Context(), session.ID, ReasonAdminForce)
	if err != nil || changed {
		t.Fatalf("repeat transition: %v %v", changed, err)
	}
	row, err := store.Get(t.Context(), session.ID)
	if err != nil || row.RevokedAt == nil || row.RevokedReason != ReasonAdminForce {
		t.Fatalf("repeat metadata: %+v %v", row, err)
	}
	if changed, err = store.RevokeWithChange(t.Context(), "missing", ReasonLogout); changed || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v %v", changed, err)
	}
	if changed, err = store.RevokeWithChange(t.Context(), "", ReasonLogout); changed || err == nil {
		t.Fatalf("empty: %v %v", changed, err)
	}
}

func TestRevokeWithChangeFailureDoesNotCommit(t *testing.T) {
	db := newTestDB(t)
	store := NewDBStore(db)
	session, err := store.Create(t.Context(), "u1", "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_revoke BEFORE UPDATE ON user_sessions BEGIN SELECT RAISE(ABORT,'fixture refusal'); END`); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.RevokeWithChange(t.Context(), session.ID, ReasonLogout); changed || err == nil {
		t.Fatalf("failed transition: %v %v", changed, err)
	}
	row, err := store.Get(t.Context(), session.ID)
	if err != nil || row.RevokedAt != nil {
		t.Fatalf("failed revoke persisted: %+v %v", row, err)
	}
}

func TestRevokeWithChangeConcurrentSingleTransition(t *testing.T) {
	db := newTestDB(t)
	db.SetMaxOpenConns(1)
	store := NewDBStore(db)
	session, err := store.Create(t.Context(), "u1", "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var changedCount atomic.Int64
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := store.RevokeWithChange(t.Context(), session.ID, ReasonLogout)
			if err != nil {
				t.Error(err)
			}
			if changed {
				changedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if changedCount.Load() != 1 {
		t.Fatalf("committed transitions %d; want 1", changedCount.Load())
	}
}
