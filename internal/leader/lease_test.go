package leader

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB returns an in-memory SQLite DB with the scheduler_leader table
// created. The DDL mirrors migration v149 (migrate_consts_v149_scheduler_leader.go)
// so the store is exercised against the real column set.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE scheduler_leader (
    scope       TEXT PRIMARY KEY,
    holder_id   TEXT NOT NULL,
    acquired_at INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// fixedClock lets a test drive the store's authoritative (DB) clock so lease
// expiry can be fast-forwarded without sleeping.
func fixedClock(now *int64) func(context.Context) (int64, error) {
	return func(context.Context) (int64, error) { return *now, nil }
}

func TestStore_AcquireWhenFree(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	now := int64(1000)
	s.nowFn = fixedClock(&now)

	held, err := s.TryAcquire(context.Background(), "scheduler", "replica-A", 60*time.Second)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if !held {
		t.Fatal("a lone replica must win a free lease immediately")
	}
}

func TestStore_RenewWhileHeld(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	now := int64(1000)
	s.nowFn = fixedClock(&now)
	ctx := context.Background()

	if held, err := s.TryAcquire(ctx, "scheduler", "replica-A", 60*time.Second); err != nil || !held {
		t.Fatalf("initial acquire: held=%v err=%v", held, err)
	}
	// Renew well before expiry (t+30 of a 60s lease). The holder keeps the
	// lease and its expiry advances.
	now = 1030
	held, err := s.TryAcquire(ctx, "scheduler", "replica-A", 60*time.Second)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !held {
		t.Fatal("holder must keep the lease on renew")
	}
	var exp, acq int64
	if err := db.QueryRow(`SELECT expires_at, acquired_at FROM scheduler_leader WHERE scope='scheduler'`).Scan(&exp, &acq); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if exp != 1090 {
		t.Fatalf("renew must advance expiry to now+ttl=1090, got %d", exp)
	}
	if acq != 1000 {
		t.Fatalf("renew must preserve original acquired_at=1000, got %d", acq)
	}
}

func TestStore_SecondContenderBlockedUntilExpiry(t *testing.T) {
	db := newTestDB(t)
	// Two stores sharing one DB but with independent clocks that both read the
	// same DB — modelled here by a single shared clock (the authoritative one).
	now := int64(1000)
	a := NewStore(db)
	a.nowFn = fixedClock(&now)
	b := NewStore(db)
	b.nowFn = fixedClock(&now)
	ctx := context.Background()

	if held, err := a.TryAcquire(ctx, "scheduler", "replica-A", 60*time.Second); err != nil || !held {
		t.Fatalf("A acquire: held=%v err=%v", held, err)
	}
	// B contends while A's lease is valid (t+10). B must be blocked.
	now = 1010
	held, err := b.TryAcquire(ctx, "scheduler", "replica-B", 60*time.Second)
	if err != nil {
		t.Fatalf("B contend: %v", err)
	}
	if held {
		t.Fatal("a second contender must be blocked while the lease is non-expired")
	}
	// A can still confirm it holds the lease.
	if held, err := a.TryAcquire(ctx, "scheduler", "replica-A", 60*time.Second); err != nil || !held {
		t.Fatalf("A still leader: held=%v err=%v", held, err)
	}
}

func TestStore_ExpiredLeaseStealable(t *testing.T) {
	db := newTestDB(t)
	now := int64(1000)
	a := NewStore(db)
	a.nowFn = fixedClock(&now)
	b := NewStore(db)
	b.nowFn = fixedClock(&now)
	ctx := context.Background()

	if held, err := a.TryAcquire(ctx, "scheduler", "replica-A", 60*time.Second); err != nil || !held {
		t.Fatalf("A acquire: held=%v err=%v", held, err)
	}
	// A dies; time advances past A's expiry (t+61 of a 60s lease).
	now = 1061
	held, err := b.TryAcquire(ctx, "scheduler", "replica-B", 60*time.Second)
	if err != nil {
		t.Fatalf("B steal: %v", err)
	}
	if !held {
		t.Fatal("an expired lease must be stealable by a new contender")
	}
	// A, coming back late, must now see itself de-throned.
	if held, err := a.TryAcquire(ctx, "scheduler", "replica-A", 60*time.Second); err != nil || held {
		t.Fatalf("A must lose to B until B's lease expires: held=%v err=%v", held, err)
	}
}

func TestLease_LoneReplicaWinsImmediately(t *testing.T) {
	db := newTestDB(t)
	l := New(db, WithScope("scheduler"), WithHolderID("solo"), WithTTL(60*time.Second), WithRenewInterval(20*time.Second))
	if l.IsLeader() {
		t.Fatal("must not claim leadership before Start")
	}
	l.Start(context.Background())
	defer l.Stop()
	if !l.IsLeader() {
		t.Fatal("a lone replica must be leader immediately after Start")
	}
}

// TestLease_TwoReplicasOneLeader is the multi-replica acceptance check from
// #1376: two Lease participants against ONE database must not both be leader,
// so a due schedule/recurring-issue fires on exactly one replica.
func TestLease_TwoReplicasOneLeader(t *testing.T) {
	db := newTestDB(t)
	a := New(db, WithScope("scheduler"), WithHolderID("A"), WithTTL(60*time.Second), WithRenewInterval(20*time.Second))
	b := New(db, WithScope("scheduler"), WithHolderID("B"), WithTTL(60*time.Second), WithRenewInterval(20*time.Second))
	a.Start(context.Background())
	defer a.Stop()
	b.Start(context.Background())
	defer b.Stop()

	if a.IsLeader() == b.IsLeader() {
		t.Fatalf("exactly one replica must be leader: A=%v B=%v", a.IsLeader(), b.IsLeader())
	}
	if !a.IsLeader() {
		t.Fatal("the first replica to Start should hold the lease")
	}
}

func TestLease_GateExpiresWithoutRenewal(t *testing.T) {
	db := newTestDB(t)
	l := New(db, WithScope("scheduler"), WithHolderID("solo"), WithTTL(40*time.Millisecond), WithRenewInterval(time.Hour))
	l.Start(context.Background())
	defer l.Stop()
	if !l.IsLeader() {
		t.Fatal("leader immediately after Start")
	}
	// Renew interval is an hour, so no renewal fires; the local freshness gate
	// must fall to false once the TTL elapses even though the DB row lingers.
	time.Sleep(80 * time.Millisecond)
	if l.IsLeader() {
		t.Fatal("gate must expire locally when renewals stop")
	}
}
