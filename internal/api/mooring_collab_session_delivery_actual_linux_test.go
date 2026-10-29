//go:build linux && mooring_rust_integration

package api

import (
	"strconv"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func TestCollaborationActualDeliveryRenewRejectsForeignAndRevokedSources(t *testing.T) {
	withActualActorSQLParticipant(t, func(f *actualActorSQLParticipant) {
		request := protocol.DeliveryRenew{Type: "delivery_renew", RequestID: "actual-renewal",
			Scope: f.opening.Scope, SessionID: f.opening.SessionID,
			ParticipantRef: protocol.Identifier(f.joined.admission.participant), Fence: f.opening.Fence,
			PermissionVersion: protocol.Counter(f.joined.gate.PermissionVersion),
			RenewalNonce:      "original-renewal-nonce", GateID: protocol.Identifier(f.joined.gate.GateID)}
		grant, err := f.joined.renewDelivery(t.Context(), f.humans, f.owner, request)
		if err != nil || grant.DurationMs != "5000" || grant.RenewalNonce != request.RenewalNonce {
			t.Fatal("original SQL renewal", grant, err)
		}
		expires := func() int64 {
			var value int64
			if err := f.x.f.rig.db.QueryRow(`SELECT expires_ns FROM mooring_delivery_gates WHERE id=?`, f.joined.gate.GateID).Scan(&value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		originalExpiry := expires()
		if _, err = f.joined.renewDelivery(t.Context(), f.humans, f.owner, request); err != nil || expires() != originalExpiry {
			t.Fatal("exact nonce retry moved SQL expiry", err)
		}
		mutations := []func(*protocol.DeliveryRenew){
			func(q *protocol.DeliveryRenew) { q.Scope.ArtifactID = "foreign-artifact" },
			func(q *protocol.DeliveryRenew) { q.SessionID = "foreign-session" },
			func(q *protocol.DeliveryRenew) { q.ParticipantRef = "foreign-participant" },
			func(q *protocol.DeliveryRenew) { q.GateID = "foreign-gate" },
			func(q *protocol.DeliveryRenew) {
				q.Fence.Generation = protocol.Counter(strconv.FormatInt(f.owner.Generation+1, 10))
			},
			func(q *protocol.DeliveryRenew) { q.Fence.Token = "foreign-owner-token" },
			func(q *protocol.DeliveryRenew) { q.PermissionVersion = "9999" },
		}
		for _, mutate := range mutations {
			foreign := request
			mutate(&foreign)
			if _, err := f.joined.renewDelivery(t.Context(), f.humans, f.owner, foreign); err == nil {
				t.Fatal("foreign renewal accepted", foreign)
			}
			if expires() != originalExpiry {
				t.Fatal("denied foreign request changed SQL grant")
			}
		}
		mustExec(t, f.x.f.rig.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='original-session'`)
		request.RenewalNonce = "after-original-source-revocation"
		if _, err := f.joined.renewDelivery(t.Context(), f.humans, f.owner, request); err == nil {
			t.Fatal("revoked original source renewed")
		}
	})
}
