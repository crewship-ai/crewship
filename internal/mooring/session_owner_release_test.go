package mooring

import (
	"errors"
	"testing"
	"time"
)

func TestOwnerReleaseFencesHolderAndPreservesIssuedBarrier(t *testing.T) {
	delivery, db, clock, old := deliveryFixture(t)
	a := deliveryOpen(t, delivery, old, "human-a", "first-a")
	b := deliveryOpen(t, delivery, old, "agent-b", "first-b")
	barrier := deliveryRevoke(t, delivery, db, old, "human-a", true)
	var beforeOrdinal, beforeExpiry int64
	var beforeBoot string
	if err := db.QueryRow(`SELECT ordinal,boot_id,expires_ns FROM mooring_delivery_gates WHERE id=?`, a.GateID).Scan(&beforeOrdinal, &beforeBoot, &beforeExpiry); err != nil {
		t.Fatal(err)
	}
	if err := delivery.owners.Release(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnerTx(t, delivery.owners, db, old); !errors.Is(err, ErrOwnerStale) {
		t.Fatalf("released holder retained authority: %v", err)
	}
	fresh, err := delivery.owners.Acquire(t.Context(), old.Key, "replacement-before-old-gate-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation != old.Generation+1 || fresh.Token == old.Token {
		t.Fatal("owner generation/token reset or reused")
	}
	if _, err = delivery.owners.Renew(t.Context(), old); !errors.Is(err, ErrOwnerStale) {
		t.Fatalf("old renew: %v", err)
	}
	if err = delivery.owners.Release(t.Context(), old); !errors.Is(err, ErrOwnerStale) {
		t.Fatalf("old release changed fresh owner: %v", err)
	}
	if err = checkOwnerTx(t, delivery.owners, db, fresh); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []DeliveryGrant{a, b} {
		var closed, blocked bool
		if err = db.QueryRow(`SELECT closed,blocked FROM mooring_delivery_gates WHERE id=?`, gate.GateID).Scan(&closed, &blocked); err != nil {
			t.Fatal(err)
		}
		if !closed || blocked {
			t.Fatal("release failed to close gate or fabricated blocked acknowledgement")
		}
	}
	var afterOrdinal, afterExpiry int64
	var afterBoot string
	if err = db.QueryRow(`SELECT ordinal,boot_id,expires_ns FROM mooring_delivery_gates WHERE id=?`, a.GateID).Scan(&afterOrdinal, &afterBoot, &afterExpiry); err != nil {
		t.Fatal(err)
	}
	if afterOrdinal != beforeOrdinal || afterExpiry != beforeExpiry || afterBoot != beforeBoot {
		t.Fatal("release changed original gate issuance evidence")
	}
	status, err := delivery.Barrier(t.Context(), old.Key, "human-a", barrier.ID)
	if err != nil || status.Complete || status.RemainingMilliseconds != 1000 {
		t.Fatal("owner replacement bypassed old delivery barrier", status, err)
	}
	clock.advance(time.Second)
	status, err = delivery.Barrier(t.Context(), old.Key, "human-a", barrier.ID)
	if err != nil || !status.Complete {
		t.Fatal(status, err)
	}
}

func TestOwnerReleaseChecksExactCapabilityAndRollsBackGateFailure(t *testing.T) {
	delivery, db, _, owner := deliveryFixture(t)
	gate := deliveryOpen(t, delivery, owner, "human-a", "first")
	for _, change := range []func(*OwnerLease){func(l *OwnerLease) { l.Token = "forged" }, func(l *OwnerLease) { l.Generation++ }, func(l *OwnerLease) { l.OwnerID = "other" }, func(l *OwnerLease) { l.Key.WorkspaceID = "other" }} {
		bad := owner
		change(&bad)
		if err := delivery.owners.Release(t.Context(), bad); !errors.Is(err, ErrOwnerStale) {
			t.Fatal("wrong capability release", err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER release_gate_failure BEFORE UPDATE OF closed ON mooring_delivery_gates BEGIN SELECT RAISE(ABORT,'synthetic gate closure failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := delivery.owners.Release(t.Context(), owner); err == nil {
		t.Fatal("release ignored failed gate closure")
	}
	if err := checkOwnerTx(t, delivery.owners, db, owner); err != nil {
		t.Fatal("failed release expired owner", err)
	}
	var closed bool
	if err := db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, gate.GateID).Scan(&closed); err != nil || closed {
		t.Fatal("failed release changed gate", closed, err)
	}
	if _, err := delivery.owners.Acquire(t.Context(), owner.Key, "other"); !errors.Is(err, ErrOwnerBusy) {
		t.Fatal("failed release permitted takeover", err)
	}
}
