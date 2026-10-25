package api

import (
	"context"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// renewDelivery connects an ORIGINAL successful join to the SQL authority.
// Wire participant/gate fields select nothing: every one must match that join.
// This grants no content delivery or new admission and never refreshes the
// original credential, permission vector, runtime binding or ingress stamp.
func (j collaborationHumanJoined) renewDelivery(ctx context.Context, humans *collaborationHumanRegistry, owner mooring.OwnerLease, request protocol.DeliveryRenew) (protocol.DeliveryGrant, error) {
	var empty protocol.DeliveryGrant
	if ctx == nil || humans == nil || humans.sessionStore == nil || j.delivery == nil ||
		request.Type != "delivery_renew" || !mooring.ValidWireIdentifier(string(request.RequestID)) ||
		!mooring.ValidWireIdentifier(string(request.RenewalNonce)) || owner.Key != humans.scope ||
		request.Scope != (protocol.Scope{WorkspaceID: protocol.Identifier(owner.Key.WorkspaceID), ArtifactID: protocol.Identifier(owner.Key.ArtifactID), ApplicationID: protocol.Identifier(owner.Key.ApplicationID)}) ||
		request.SessionID != protocol.Identifier(humans.hostSessionID()) ||
		request.Fence != (protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}) ||
		request.ParticipantRef != protocol.Identifier(j.admission.participant) ||
		request.GateID != protocol.Identifier(j.gate.GateID) ||
		request.PermissionVersion != protocol.Counter(j.gate.PermissionVersion) ||
		owner.Generation != j.gate.OwnerGeneration {
		return empty, errCollaborationAdmission
	}
	unlock, err := humans.acquireRuntimeOperation()
	if err != nil {
		return empty, err
	}
	defer unlock()
	runtime, err := humans.sessionStore.live()
	if err != nil || runtime != humans.runtime || owner.OwnerID != runtime.RuntimeRef() {
		return empty, errCollaborationAdmission
	}
	release, err := humans.fence.acquireOriginal(j.admission.ingress)
	if err != nil {
		return empty, err
	}
	defer release()
	grant, err := j.delivery.Renew(ctx, owner, j.gate.GateID, string(request.RenewalNonce))
	if err != nil {
		return empty, err
	}
	if grant.GateID != j.gate.GateID || grant.PermissionVersion != j.gate.PermissionVersion || grant.OwnerGeneration != owner.Generation || grant.RenewalNonce != string(request.RenewalNonce) {
		return empty, errCollaborationAdmission
	}
	return protocol.DeliveryGrant{Type: "delivery_grant", RequestID: request.RequestID,
		Scope: request.Scope, SessionID: request.SessionID, ParticipantRef: request.ParticipantRef,
		Generation: request.Fence.Generation, PermissionVersion: protocol.Counter(grant.PermissionVersion),
		RenewalNonce: protocol.Identifier(grant.RenewalNonce), GateID: protocol.Identifier(grant.GateID),
		DurationMs: protocol.PositiveCounter(strconv.FormatInt(grant.DurationMilliseconds, 10))}, nil
}
