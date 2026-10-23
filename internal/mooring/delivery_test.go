package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Only the authority callback is synthetic here. SQL serialization, persisted
// gates/barriers, owner fencing and commit/rollback use the real database.
func deliveryFixture(t *testing.T) (*DeliveryStore, *sql.DB, *ownerTestClock, OwnerLease) {
	t.Helper()
	owners, db, clock, key := ownerFixture(t)
	if _, e := db.Exec(`CREATE TABLE test_delivery_rights(participant TEXT PRIMARY KEY,can_read INTEGER,version TEXT); INSERT INTO test_delivery_rights VALUES('human-a',1,'7'),('agent-b',1,'8')`); e != nil {
		t.Fatal(e)
	}
	authority := func(ctx context.Context, tx *sql.Tx, a DeliveryAccess) (string, error) {
		if a.Scope != key {
			return "", ErrDeliveryClosed
		}
		var allowed bool
		var version string
		e := tx.QueryRowContext(ctx, `SELECT can_read,version FROM test_delivery_rights WHERE participant=?`, a.Participant).Scan(&allowed, &version)
		if e != nil {
			return "", e
		}
		if (a.Intent == "connect" || a.Intent == "renew") && !allowed {
			return "", ErrDeliveryClosed
		}
		return version, nil
	}
	lease, e := owners.Acquire(t.Context(), key, "runtime-a")
	if e != nil {
		t.Fatal(e)
	}
	return NewDeliveryStore(owners, true, authority, time.Second, 20), db.DB, clock, lease
}
func deliveryOpen(t *testing.T, s *DeliveryStore, l OwnerLease, p, r string) DeliveryGrant {
	t.Helper()
	g, e := s.Open(t.Context(), l, p, r)
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func deliveryRevoke(t *testing.T, s *DeliveryStore, db *sql.DB, l OwnerLease, p string, commit bool) DeliveryBarrier {
	t.Helper()
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(t.Context(), `UPDATE test_delivery_rights SET can_read=0,version='9' WHERE participant=?`, p); e != nil {
		t.Fatal(e)
	}
	b, e := s.RevokeTx(t.Context(), tx, l.Key, p)
	if e != nil {
		t.Fatal(e)
	}
	if b.Complete {
		t.Fatal("uncommitted revocation claimed complete")
	}
	if commit {
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	return b
}
func TestDeliveryDisabledNoDatabase(t *testing.T) {
	s := NewDeliveryStore(nil, false, nil, 0, 0)
	if _, e := s.Open(t.Context(), OwnerLease{}, "p", "r"); !errors.Is(e, ErrDeliveryDisabled) {
		t.Fatal(e)
	}
	if _, e := s.RevokeTx(t.Context(), nil, SessionKey{}, "p"); !errors.Is(e, ErrDeliveryDisabled) {
		t.Fatal(e)
	}
}
func TestDeliveryRevokeLostControlWaitsExpiryAndNeverReopens(t *testing.T) {
	s, db, clock, l := deliveryFixture(t)
	a := deliveryOpen(t, s, l, "human-a", "request-a")
	b := deliveryOpen(t, s, l, "agent-b", "request-b")
	barrier := deliveryRevoke(t, s, db, l, "human-a", true)
	status, e := s.Barrier(t.Context(), l.Key, "human-a", barrier.ID)
	if e != nil || status.Complete || status.RemainingMilliseconds != 1000 {
		t.Fatal(status, e)
	}
	if _, e = s.Renew(t.Context(), l, a.GateID, "after-revoke"); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatal(e)
	}
	if _, e = s.Renew(t.Context(), l, b.GateID, "independent-renew"); e != nil {
		t.Fatal(e)
	}
	clock.advance(999 * time.Millisecond)
	status, e = s.Barrier(t.Context(), l.Key, "human-a", barrier.ID)
	if e != nil || status.Complete || status.RemainingMilliseconds != 1 {
		t.Fatal(status, e)
	}
	clock.advance(time.Millisecond)
	status, e = s.Barrier(t.Context(), l.Key, "human-a", barrier.ID)
	if e != nil || !status.Complete {
		t.Fatal(status, e)
	}
	if _, e = db.Exec(`UPDATE test_delivery_rights SET can_read=1,version='10' WHERE participant='human-a'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Renew(t.Context(), l, a.GateID, "after-regrant"); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatal("old gate revived", e)
	}
	fresh := deliveryOpen(t, s, l, "human-a", "fresh")
	if fresh.GateID == a.GateID || fresh.PermissionVersion != "10" {
		t.Fatal(fresh)
	}
	status, e = s.Barrier(t.Context(), l.Key, "human-a", barrier.ID)
	if e != nil || !status.Complete {
		t.Fatal("fresh gate changed old immutable barrier", status, e)
	}
}
func TestDeliveryRevocationRollbackAndRuntimeAckScope(t *testing.T) {
	s, db, _, l := deliveryFixture(t)
	g := deliveryOpen(t, s, l, "human-a", "r1")
	rolledBack := deliveryRevoke(t, s, db, l, "human-a", false)
	if _, e := s.Barrier(t.Context(), l.Key, "human-a", rolledBack.ID); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	if _, e := s.Renew(t.Context(), l, g.GateID, "r2"); e != nil {
		t.Fatal("rollback closed grant", e)
	}
	b := deliveryRevoke(t, s, db, l, "human-a", true)
	forged := l
	forged.Token = "wrong"
	if e := s.AcknowledgeBlocked(t.Context(), forged, g.GateID, b.ID); !errors.Is(e, ErrOwnerStale) {
		t.Fatal(e)
	}
	if e := s.AcknowledgeBlocked(t.Context(), l, g.GateID, "unknown-barrier"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	if e := s.AcknowledgeBlocked(t.Context(), l, g.GateID, b.ID); e != nil {
		t.Fatal(e)
	}
	status, e := s.Barrier(t.Context(), l.Key, "human-a", b.ID)
	if e != nil || !status.Complete {
		t.Fatal(status, e)
	}
	if _, e = s.Barrier(t.Context(), l.Key, "agent-b", b.ID); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("barrier escaped participant scope", e)
	}
}
func TestDeliveryOwnerTakeoverStillWaitsOldIssuedLease(t *testing.T) {
	s, db, clock, l := deliveryFixture(t)
	clock.advance(4500 * time.Millisecond)
	g := deliveryOpen(t, s, l, "human-a", "last-before-takeover")
	clock.advance(500 * time.Millisecond)
	replacement, e := s.owners.Acquire(t.Context(), l.Key, "replacement")
	if e != nil {
		t.Fatal(e)
	}
	b := deliveryRevoke(t, s, db, l, "human-a", true)
	if e = s.AcknowledgeBlocked(t.Context(), l, g.GateID, b.ID); !errors.Is(e, ErrOwnerStale) {
		t.Fatal(e)
	}
	if e = s.AcknowledgeBlocked(t.Context(), replacement, g.GateID, b.ID); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatal("replacement claimed old runtime acknowledgement", e)
	}
	status, e := s.Barrier(t.Context(), l.Key, "human-a", b.ID)
	if e != nil || status.Complete || status.RemainingMilliseconds != 500 {
		t.Fatal(status, e)
	}
	clock.advance(500 * time.Millisecond)
	status, e = s.Barrier(t.Context(), l.Key, "human-a", b.ID)
	if e != nil || !status.Complete {
		t.Fatal(status, e)
	}
}
func TestDeliveryExactRenewRetryDoesNotExtendHostBarrier(t *testing.T) {
	s, db, clock, l := deliveryFixture(t)
	g := deliveryOpen(t, s, l, "human-a", "same-request")
	clock.advance(750 * time.Millisecond)
	if retry, e := s.Renew(t.Context(), l, g.GateID, "same-request"); e != nil || retry != g {
		t.Fatal(retry, e)
	}
	b := deliveryRevoke(t, s, db, l, "human-a", true)
	status, e := s.Barrier(t.Context(), l.Key, "human-a", b.ID)
	if e != nil || status.RemainingMilliseconds != 250 {
		t.Fatal("retry stretched issued lease", status, e)
	}
}
func TestDeliveryExpiryPermissionChangeAndSlowAuthorityFailClosed(t *testing.T) {
	s, db, clock, l := deliveryFixture(t)
	g := deliveryOpen(t, s, l, "human-a", "r1")
	if _, e := db.Exec(`UPDATE test_delivery_rights SET version='11' WHERE participant='human-a'`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Renew(t.Context(), l, g.GateID, "r2"); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE test_delivery_rights SET version='7' WHERE participant='human-a'`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Renew(t.Context(), l, g.GateID, "r3"); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatal("changed permission gate reopened", e)
	}
	g = deliveryOpen(t, s, l, "human-a", "slow")
	original := s.authorize
	s.authorize = func(ctx context.Context, tx *sql.Tx, a DeliveryAccess) (string, error) {
		v, e := original(ctx, tx, a)
		clock.advance(time.Second)
		return v, e
	}
	if _, e := s.Renew(t.Context(), l, g.GateID, "slow-renew"); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatal("expired during authority call", e)
	}
	s.authorize = original
	s.maxGates = 2
	if _, e := s.Open(t.Context(), l, "human-a", "capacity"); !errors.Is(e, ErrDeliveryCapacity) {
		t.Fatal(e)
	}
	s.maxGates = 20
	s.authorize = func(ctx context.Context, tx *sql.Tx, a DeliveryAccess) (string, error) {
		v, e := original(ctx, tx, a)
		clock.advance(5 * time.Second)
		return v, e
	}
	if _, e := s.Open(t.Context(), l, "human-a", "owner-expired"); !errors.Is(e, ErrOwnerStale) {
		t.Fatal("owner expired during authority call", e)
	}
}

func TestDeliveryNewCapabilityCannotAckSourceGateAfterOwnerReset(t *testing.T) {
	s, db, _, old := deliveryFixture(t)
	g := deliveryOpen(t, s, old, "human-a", "before-recovery")
	b := deliveryRevoke(t, s, db, old, "human-a", true)
	// Model the already-qualified physical recovery owner reset. A new owner
	// can reuse the public owner name/generation, but NEVER its opaque capability.
	if _, e := db.Exec(`DELETE FROM mooring_session_owners`); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.owners.Acquire(t.Context(), old.Key, old.OwnerID)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Generation != old.Generation || fresh.Token == old.Token {
		t.Fatal("fixture did not recreate public identity with fresh capability")
	}
	if e = s.AcknowledgeBlocked(t.Context(), fresh, g.GateID, b.ID); !errors.Is(e, ErrDeliveryClosed) {
		t.Fatalf("new capability acknowledged old runtime's content paths: %v", e)
	}
	status, e := s.Barrier(t.Context(), old.Key, "human-a", b.ID)
	if e != nil || status.Complete {
		t.Fatal("source expiry bound disappeared", status, e)
	}
}

func TestDeliveryRevocationNeverConsumesAdmissionCapacity(t *testing.T) {
	s, db, _, l := deliveryFixture(t)
	s.maxGates = 1
	for range 3 {
		empty := deliveryRevoke(t, s, db, l, "human-a", true)
		if !empty.NoIssuedGates || empty.Complete {
			t.Fatal("empty set is only a prepared post-commit decision", empty)
		}
	}
	var barriers int
	if e := db.QueryRow(`SELECT count(*) FROM mooring_delivery_barriers`).Scan(&barriers); e != nil || barriers != 0 {
		t.Fatal("empty revocations consumed runtime capacity", barriers, e)
	}
	if _, e := db.Exec(`UPDATE test_delivery_rights SET can_read=1,version='10' WHERE participant='human-a'`); e != nil {
		t.Fatal(e)
	}
	deliveryOpen(t, s, l, "human-a", "only-admission-slot")
	var first DeliveryBarrier
	for i := 0; i < 3; i++ {
		b := deliveryRevoke(t, s, db, l, "human-a", true)
		if b.NoIssuedGates {
			t.Fatal(b)
		}
		if i == 0 {
			first = b
		} else if b.ID != first.ID {
			t.Fatal("same immutable gate set created another barrier", b)
		}
	}
	if e := db.QueryRow(`SELECT count(*) FROM mooring_delivery_barriers`).Scan(&barriers); e != nil || barriers != 1 {
		t.Fatal(barriers, e)
	}
	status, e := s.Barrier(t.Context(), l.Key, "human-a", first.ID)
	if e != nil || status.Complete {
		t.Fatal("admission quota caused premature revocation confirmation", status, e)
	}
}
