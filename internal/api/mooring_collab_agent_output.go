package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Called only after the private runtime proved its ORIGINAL provider ExecID
// terminated. Close its exact SQL gate and physically retire its native binding
// before the controller erases that original attached handle.
func (a *collaborationLiveAgent) closeConfirmedRun(ctx context.Context) error {
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	a.mu.Lock()
	gate, binding, pending, retired := a.gate, a.binding, a.pending, a.retired
	a.mu.Unlock()
	d := a.document
	if retired == nil && gate.GateID != "" {
		err := a.run(ctx, func(ctx context.Context) error {
			return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
				if d.cell.check(ctx, tx) != nil || d.manager.owners.Check(ctx, tx, d.owner) != nil {
					return errCollaborationAgentAccess
				}
				hash := sha256.Sum256([]byte(d.owner.Token))
				result, err := tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND owner_token_hash=? AND permission_version=?`, gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, a.participant.id, d.owner.OwnerID, d.owner.Generation, hash[:], gate.PermissionVersion)
				if err != nil {
					return err
				}
				n, err := result.RowsAffected()
				if err != nil || n != 1 {
					return errCollaborationAgentAccess
				}
				return nil
			})
		})
		if err != nil {
			return err
		}
		if binding == nil {
			return errCollaborationAgentAccess
		}
		proof, err := d.cell.runtime.RetireParticipant(ctx, d.activation, binding)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.retired = proof
		a.binding = nil
		a.mu.Unlock()
	} else if retired == nil && pending != nil {
		// An uncertain physical Open cannot be abandoned or called a completed
		// Leave. Preserve safety by closing the runtime rather than a fake ACK.
		if d.cell.runtime.DiscardUnsentParticipantOpen(pending) != nil {
			d.cancel()
			return errCollaborationAgentAccess
		}
	}
	a.mu.Lock()
	if !a.closed && !a.joined {
		close(a.ready)
	}
	a.closed = true
	a.mu.Unlock()
	d.originals.mu.Lock()
	if source := d.originals.agents[a]; source != nil {
		source.retired = true
	}
	d.originals.mu.Unlock()
	return nil
}

func (a *collaborationLiveAgent) renew(ctx context.Context, token mooring.SessionControl, q protocol.DeliveryRenew) error {
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	d := a.document
	a.mu.Lock()
	retired, gate, delivery := a.retired, a.gate, a.delivery
	closed := a.closed
	a.mu.Unlock()
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != d.wireFence() || a.participant == nil || q.ParticipantRef != protocol.Identifier(a.participant.id) || q.GateID != protocol.Identifier(gate.GateID) || q.PermissionVersion != protocol.Counter(gate.PermissionVersion) {
		return errCollaborationAgentAccess
	}
	if retired != nil {
		return d.cell.runtime.CompleteRetiredDeliveryControl(token, retired)
	}
	if closed || delivery == nil {
		return errCollaborationAgentAccess
	}
	var grant mooring.DeliveryGrant
	err := a.run(ctx, func(ctx context.Context) error {
		var err error
		grant, err = delivery.Renew(ctx, d.owner, gate.GateID, string(q.RenewalNonce))
		return err
	})
	if err != nil {
		if !errors.Is(err, errCollaborationAuthorChanged) && !errors.Is(err, mooring.ErrDeliveryClosed) {
			return err
		}
		if err = a.retireClosedGate(ctx); err != nil {
			return err
		}
		a.mu.Lock()
		retired = a.retired
		a.mu.Unlock()
		return d.cell.runtime.CompleteRetiredDeliveryControl(token, retired)
	}
	return collaborationPumpDeliveryReply(ctx, d.cell.runtime, token, q, protocol.DeliveryGrant{Type: "delivery_grant", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, Generation: q.Fence.Generation, ParticipantRef: q.ParticipantRef, GateID: q.GateID, PermissionVersion: protocol.Counter(grant.PermissionVersion), DurationMs: protocol.PositiveCounter(strconv.FormatInt(grant.DurationMilliseconds, 10)), RenewalNonce: q.RenewalNonce})
}

// Caller holds deliveryMu. Definitive original-vector revocation or the exact
// permanently closed SQL gate permits recipient-only retirement. Generic ACL,
// SQL, process or clock errors cannot be turned into a denial acknowledgement.
func (a *collaborationLiveAgent) retireClosedGate(ctx context.Context) error {
	d := a.document
	a.mu.Lock()
	binding, gate := a.binding, a.gate
	a.mu.Unlock()
	if binding == nil || gate.GateID == "" {
		return errCollaborationAgentAccess
	}
	err := a.run(ctx, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if d.cell.check(ctx, tx) != nil || d.manager.owners.Check(ctx, tx, d.owner) != nil {
				return errCollaborationAgentAccess
			}
			hash := sha256.Sum256([]byte(d.owner.Token))
			var closed bool
			var boot string
			var expires int64
			err := tx.QueryRowContext(ctx, `SELECT closed,boot_id,expires_ns FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND owner_token_hash=? AND permission_version=?`, gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, a.participant.id, d.owner.OwnerID, d.owner.Generation, hash[:], gate.PermissionVersion).Scan(&closed, &boot, &expires)
			if err != nil {
				return err
			}
			current, now, err := d.manager.clock.Now()
			if err != nil || current != boot || now < 0 {
				return errCollaborationAgentAccess
			}
			if !closed && int64(now) < expires {
				decision, err := a.confirmed.store.DecideCaptured(ctx, tx, a.participant.captured, d.key, a.participant.captured.intent)
				if err != nil {
					return err
				}
				if decision.kind != collaborationAgentCanonicalPermissionChanged {
					return errCollaborationAgentAccess
				}
			}
			_, err = tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE id=? AND page_id=? AND participant_ref=? AND owner_id=? AND owner_generation=?`, gate.GateID, d.key.ArtifactID, a.participant.id, d.owner.OwnerID, d.owner.Generation)
			return err
		})
	})
	if err != nil {
		return err
	}
	proof, err := d.cell.runtime.RetireParticipant(ctx, d.activation, binding)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.retired = proof
	a.binding = nil
	a.closed = true
	a.mu.Unlock()
	d.originals.mu.Lock()
	if source := d.originals.agents[a]; source != nil {
		source.retired = true
	}
	d.originals.mu.Unlock()
	return nil // RetireParticipant already physically wrote the exact Leave.
}

func (a *collaborationLiveAgent) output(ctx context.Context, token mooring.SessionControl, q protocol.SessionParticipantOutput) error {
	d := a.document
	raw, err := d.manager.schema.ValidateE3(q.Message)
	if err != nil {
		return err
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return errCollaborationAgentAccess
	}
	a.mu.Lock()
	if q.ConnectionRef != protocol.Identifier(a.ref) || a.participant == nil || q.ParticipantRef != protocol.Identifier(a.participant.id) || q.GateID != protocol.Identifier(a.gate.GateID) || q.PermissionVersion != protocol.Counter(a.gate.PermissionVersion) {
		a.mu.Unlock()
		return errCollaborationAgentAccess
	}
	if header.Type == "session_joined" {
		var joined protocol.SessionJoined
		if a.closed || a.joined || a.pending == nil || q.RequestID != a.openID || json.Unmarshal(raw, &joined) != nil || joined.RequestID != a.joinID || joined.Scope != d.wireScope() || joined.SessionID != protocol.Identifier(d.activation.SessionID()) || joined.Versions != d.wireVersions() || joined.Generation != d.wireFence().Generation || joined.ParticipantRef != q.ParticipantRef || joined.GateID != q.GateID || joined.PermissionVersion != q.PermissionVersion {
			a.mu.Unlock()
			return errCollaborationAgentAccess
		}
		binding, err := d.cell.runtime.CaptureParticipantJoined(d.activation, a.pending, token)
		if err != nil {
			a.mu.Unlock()
			return err
		}
		a.pending = nil
		a.binding = binding
		a.joined = true
		close(a.ready)
		a.mu.Unlock()
		return nil
	}
	joined, retired := a.joined, a.retired
	a.mu.Unlock()
	if !joined {
		return errCollaborationAgentAccess
	}
	if retired != nil {
		if err = d.cell.runtime.ValidateRetiredOutput(token, retired); err != nil {
			return err
		}
	}
	if header.Type == "read_result" || header.Type == "protocol_error" {
		return a.capturePageRead(ctx, token, q, raw, retired)
	}
	l := d.originals
	l.mu.Lock()
	var group *collaborationOriginalGroup
	switch header.Type {
	case "changes_received":
		var pending protocol.ChangesReceived
		if json.Unmarshal(raw, &pending) != nil || pending.State != "pending" || pending.Scope != d.wireScope() || pending.SessionID != protocol.Identifier(d.activation.SessionID()) {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
		group = l.groups[a.ref+"\x00"+string(pending.RequestID)]
		if group == nil || group.source.agent != a || !group.captured || !group.sent || q.RequestID != group.outerID || len(group.changes) != len(pending.ChangeIds) {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
		for i, id := range pending.ChangeIds {
			if string(id) != group.changes[i] {
				l.mu.Unlock()
				return errCollaborationAgentAccess
			}
		}
	case "commit_result":
		var receipt protocol.CommitResult
		if json.Unmarshal(raw, &receipt) != nil {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
		group = l.groups[a.ref+"\x00"+string(receipt.RequestID)]
		if group == nil || group.source.agent != a || !group.captured || !group.sent || group.receipt == nil || q.RequestID != group.outerID || !collaborationEqualJSON(raw, group.receipt) {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
	case "document_update":
		var update protocol.DocumentUpdate
		if json.Unmarshal(raw, &update) != nil || update.Scope != d.wireScope() || update.SessionID != protocol.Identifier(d.activation.SessionID()) || update.Generation != d.wireFence().Generation || update.Encoding != "json" || update.RequestID != protocol.Identifier("actor-update:"+string(update.ToRevision)) {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
		expected, err := collaborationPeerOutputID(string(update.ToRevision), a.ref, a.participant.id, a.gate.GateID)
		if err != nil || expected != q.RequestID {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
		matched := false
		for _, slot := range l.changes {
			if slot.saved == nil || slot.saved.cut.Revision != string(update.ToRevision) || slot.group == nil || slot.group.privateReceipt == nil || !collaborationEqualJSONReceipt(update.Receipt, slot.group.privateReceipt) {
				continue
			}
			var outcome *protocol.ChangeResult
			for i := range slot.group.privateReceipt.Changes {
				if slot.group.privateReceipt.Changes[i].ChangeID == protocol.Identifier(slot.change.ID) {
					outcome = &slot.group.privateReceipt.Changes[i]
					break
				}
			}
			if outcome == nil || outcome.FirstRevision == nil || outcome.LastRevision == nil || string(*outcome.LastRevision) != slot.saved.cut.Revision {
				continue
			}
			first, err := strconv.ParseUint(string(*outcome.FirstRevision), 10, 64)
			if err != nil || first == 0 || update.FromRevision != protocol.Counter(strconv.FormatUint(first-1, 10)) {
				continue
			}
			data := struct {
				Kind    string                `json:"kind"`
				Update  string                `json:"update"`
				Targets []protocol.Identifier `json:"targets"`
			}{"wordcraft.yrs-update.v1", slot.saved.delta.Update, slot.saved.delta.Targets}
			matched = collaborationEqualJSON([]byte(update.Data), data)
			if matched {
				break
			}
		}
		if !matched {
			l.mu.Unlock()
			return errCollaborationAgentAccess
		}
	default:
		l.mu.Unlock()
		return errCollaborationAgentAccess
	}
	l.mu.Unlock()
	if retired != nil {
		return nil
	} // Exact denied old recipient; no external bytes.
	err = a.run(ctx, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error { _, err := a.allowed(ctx, tx, "read"); return err })
	})
	if err != nil {
		if !errors.Is(err, errCollaborationAuthorChanged) {
			return err
		}
		a.deliveryMu.Lock()
		err = a.retireClosedGate(ctx)
		a.deliveryMu.Unlock()
		if err != nil {
			return err
		}
		a.mu.Lock()
		proof := a.retired
		a.mu.Unlock()
		return d.cell.runtime.ValidateRetiredOutput(token, proof)
	}
	// This typed-command client reads a fresh FULL snapshot for its next tool
	// call; it has no optimistic raw-CRDT overlay or externally delivered peer
	// cursor. Validated peer notifications are consumed only by this host client.
	if header.Type == "commit_result" {
		l.mu.Lock()
		if !group.agentReceiptDelivered {
			group.agentReceiptDelivered = true
			close(group.agentReceiptReady)
		}
		l.mu.Unlock()
	}
	return nil
}
