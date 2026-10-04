package leader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStoppedLeaseNoLongerAuthorizesScheduling(t *testing.T) {
	db := newTestDB(t)
	l := New(db, WithScope("isolated"), WithTTL(0), WithRenewInterval(0), WithLogger(nil))
	if !strings.HasPrefix(l.HolderID(), "replica-") {
		t.Fatalf("missing generated holder identity: %q", l.HolderID())
	}
	l.Start(context.Background())
	l.Start(context.Background())
	if !l.IsLeader() {
		t.Fatal("initial lease not acquired")
	}
	l.Stop()
	l.Stop()
	if l.IsLeader() {
		t.Fatal("stopped participant still authorizes scheduler work")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scheduler_leader WHERE scope = 'isolated'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stopped lease was not released: %d, %v", count, err)
	}
}

func TestCancelledLeaseNoLongerAuthorizesScheduling(t *testing.T) {
	l := New(newTestDB(t), WithHolderID("cancelled"))
	ctx, cancel := context.WithCancel(context.Background())
	l.Start(ctx)
	t.Cleanup(l.Stop)
	cancel()
	select {
	case <-l.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled participant did not stop")
	}
	if l.IsLeader() {
		t.Fatal("cancelled participant still authorizes scheduling")
	}
}

func TestLeaseDatabaseFailuresExpireLocalAuthority(t *testing.T) {
	db := newTestDB(t)
	l := New(db, WithHolderID("holder"), WithTTL(time.Second))
	now := time.Now()
	l.localNow = func() time.Time { return now }
	l.attempt(context.Background())
	if !l.IsLeader() {
		t.Fatal("initial acquisition failed")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	l.attempt(context.Background())
	if !l.IsLeader() {
		t.Fatal("one failed renewal unnecessarily discarded a fresh lease")
	}
	now = now.Add(time.Second)
	if l.IsLeader() {
		t.Fatal("failed renewals extended authority past its TTL")
	}
	store := NewStore(db)
	store.nowFn = func(context.Context) (int64, error) { return 100, nil }
	if held, err := store.TryAcquire(context.Background(), "scope", "holder", time.Second); err == nil || held {
		t.Fatalf("failed write acquired lease: %v, %v", held, err)
	}
	sentinel := errors.New("authoritative clock unavailable")
	store.nowFn = func(context.Context) (int64, error) { return 0, sentinel }
	if held, err := store.TryAcquire(context.Background(), "scope", "holder", time.Second); held || !errors.Is(err, sentinel) {
		t.Fatalf("clock failure was not preserved: %v, %v", held, err)
	}
}
