package api

import (
	"testing"
	"time"
)

func TestP3DeliverySQLRetryKeepsOriginalConfiguredHorizonAndExpiryIsPermanent(t *testing.T) {
	rig, registry, owner, ctx, clock := collaborationAdmissionFixture(t)
	registry.deliveryTTL = 3 * time.Second
	ticket, _, err := registry.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := registry.join(t.Context(), registry.runtime, owner, ticket, "p3-original-join")
	if err != nil {
		t.Fatal(err)
	}
	clock.now += 250 * time.Millisecond
	grant, err := joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "p3-first-renewal")
	if err != nil || grant.DurationMilliseconds != 3000 {
		t.Fatal("configured renewal", grant, err)
	}
	var expiry int64
	if err = rig.db.QueryRow(`SELECT expires_ns FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	clock.now += time.Second
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "p3-first-renewal"); err != nil {
		t.Fatal(err)
	}
	var duplicateExpiry int64
	if err = rig.db.QueryRow(`SELECT expires_ns FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&duplicateExpiry); err != nil {
		t.Fatal(err)
	}
	if duplicateExpiry != expiry {
		t.Fatal("same nonce extended SQL expiry")
	}
	clock.now = time.Duration(expiry)
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "p3-new-after-expiry"); err == nil {
		t.Fatal("exclusive expiry revived")
	}
	clock.now -= time.Second
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "p3-new-after-reversal"); err == nil {
		t.Fatal("closed gate reopened after clock reversal")
	}
}

func TestP3DeliverySQLSourceRevocationInsideRenewalCannotPublishGrant(t *testing.T) {
	rig, registry, owner, ctx, clock := collaborationAdmissionFixture(t)
	ticket, _, err := registry.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := registry.join(t.Context(), registry.runtime, owner, ticket, "p3-trigger-original-join")
	if err != nil {
		t.Fatal(err)
	}
	clock.now += time.Millisecond
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "p3-before-trigger"); err != nil {
		t.Fatal("positive renewal without the canonical-source trigger", err)
	}
	mustExec(t, rig.db, `CREATE TRIGGER p3_revoke_during_gate_renew AFTER UPDATE OF expires_ns ON mooring_delivery_gates BEGIN UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='admission-source'; END`)
	clock.now += time.Millisecond
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "p3-trigger-renewal"); err == nil {
		t.Fatal("renewal published a grant after its own SQL side effect revoked the original source")
	}
	t.Log("canonical-source trigger renewal denied", err)
}
