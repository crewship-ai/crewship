package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

var errCollaborationRecipientDenied = errors.New("original participant no longer qualified")

type collaborationRetiredGate struct {
	gate  mooring.DeliveryGrant
	proof *mooring.SessionRetiredDelivery
}
type collaborationGateTransition struct {
	ready chan struct{}
	err   error
}

// Only the original bounded connection/source tables can locate a recipient.
// A retired source remains DATA provenance, never current recipient authority.
func (d *collaborationHTTPDocument) participantConnection(ref, participant string) *collaborationHTTPConnection {
	d.mu.Lock()
	for _, c := range d.connections {
		if (ref == "" || c.ref == ref) && c.admission.participant == participant {
			d.mu.Unlock()
			return c
		}
	}
	d.mu.Unlock()
	if d.originals != nil {
		d.originals.mu.Lock()
		defer d.originals.mu.Unlock()
		for c := range d.originals.sources {
			if (ref == "" || c.ref == ref) && c.admission.participant == participant {
				return c
			}
		}
	}
	return nil
}
func (c *collaborationHTTPConnection) pruneRetirementsLocked() {
	for gate, r := range c.retirements {
		select {
		case <-r.proof.Done():
			delete(c.retirements, gate)
		default:
		}
	}
}

// The caller holds deliveryMu. This denial-only FULL writer proves the EXACT
// original gate is permanently closed; generic callback errors prove nothing.
func (c *collaborationHTTPConnection) closeObsoleteGate(ctx context.Context, gate mooring.DeliveryGrant) (bool, error) {
	d := c.document
	obsolete := false
	err := d.humans.runHumanOperation(c.request, c.admission.participant, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if err := d.cell.check(ctx, tx); err != nil {
				return err
			}
			hash := sha256.Sum256([]byte(d.owner.Token))
			var boot, version, current string
			var expires int64
			var closed bool
			err := tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns,closed,permission_version FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND owner_token_hash=?`, gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, c.admission.participant, d.owner.OwnerID, d.owner.Generation, hash[:]).Scan(&boot, &expires, &closed, &version)
			if err != nil {
				return err
			}
			if version != gate.PermissionVersion {
				return errCollaborationHTTP
			}
			if err = tx.QueryRowContext(ctx, `SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=? AND deleted_at IS NULL`, d.key.WorkspaceID).Scan(&current); err != nil {
				return err
			}
			currentBoot, now, err := d.manager.clock.Now()
			if err != nil {
				return err
			}
			if now < 0 || currentBoot != boot {
				return errCollaborationHTTP
			}
			obsolete = closed || version != current || int64(now) >= expires
			if obsolete {
				if _, err = tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND owner_token_hash=? AND permission_version=?`, gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID, c.admission.participant, d.owner.OwnerID, d.owner.Generation, hash[:], version); err != nil {
					return err
				}
			}
			return d.cell.check(ctx, tx)
		})
	})
	return obsolete, err
}

// startGateTransition owns deliveryMu only through exact Leave/Open writes.
// It NEVER waits for Ticket or Joined while holding connection/delivery locks.
func (c *collaborationHTTPConnection) startGateTransition(ctx context.Context) (*collaborationGateTransition, error) {
	d := c.document
	c.mu.Lock()
	c.pruneRetirementsLocked()
	if c.transition != nil {
		t := c.transition
		c.mu.Unlock()
		return t, nil
	}
	if c.closed || c.participantBinding == nil || c.joined.delivery == nil {
		c.mu.Unlock()
		return nil, errCollaborationHTTP
	}
	binding, gate := c.participantBinding, c.joined.gate
	c.mu.Unlock()
	obsolete, err := c.closeObsoleteGate(ctx, gate)
	if err != nil || !obsolete {
		return nil, err
	}
	proof, err := d.cell.runtime.RetireParticipant(ctx, d.activation, binding)
	if err != nil {
		return nil, err
	}
	retired := &collaborationRetiredGate{gate: gate, proof: proof}
	t := &collaborationGateTransition{ready: make(chan struct{})}
	c.mu.Lock()
	if c.retirements == nil {
		c.retirements = make(map[string]*collaborationRetiredGate)
	}
	c.retirements[gate.GateID] = retired
	c.transition = t
	c.participantBinding = nil
	c.mu.Unlock()
	ticket, err := d.humans.reconnect(ctx, d.committer.binding, d.owner, c.admission.participant)
	if err != nil {
		if errors.Is(err, errCollaborationAuthorChanged) {
			c.mu.Lock()
			t.err = errCollaborationRecipientDenied
			close(t.ready)
			c.mu.Unlock()
			c.closeRetiredRecipient()
			return t, nil
		}
		return nil, err
	}
	id, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	joinID, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	join, err := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: protocol.Identifier(joinID), Ticket: ticket, Versions: d.wireVersions()})
	if err != nil {
		return nil, err
	}
	pending, err := d.cell.runtime.PrepareParticipantReopen(d.activation, proof, protocol.SessionParticipantOpen{Type: "session_participant_open", RequestID: protocol.Identifier(id), ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(c.ref), Join: join})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.ticket = ticket
	c.openRequest = protocol.Identifier(id)
	c.joinRequest = protocol.Identifier(joinID)
	c.participantPending = pending
	c.joined = collaborationHumanJoined{}
	c.mu.Unlock()
	if err = d.cell.runtime.SendPreparedParticipantOpen(ctx, d.activation, pending); err != nil {
		return nil, err
	}
	return t, nil
}
func (c *collaborationHTTPConnection) ensureCurrentGate(ctx context.Context) error {
	c.deliveryMu.Lock()
	t, err := c.startGateTransition(ctx)
	c.deliveryMu.Unlock()
	if err != nil {
		c.document.cancel()
		return err
	}
	if t == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.document.ctx.Done():
		return c.document.ctx.Err()
	case <-t.ready:
		c.mu.Lock()
		err = t.err
		c.mu.Unlock()
		return err
	}
}

// Leave was already physically sent. Do not send another Leave, refresh the
// original source, or discard durable originals when this socket is denied.
func (c *collaborationHTTPConnection) closeRetiredRecipient() {
	c.mu.Lock()
	c.closed = true
	ws := c.socket
	c.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
	d := c.document
	d.originals.retire(c)
	d.mu.Lock()
	if d.connections[c.ref] == c {
		delete(d.connections, c.ref)
	}
	d.mu.Unlock()
}

func (d *collaborationHTTPDocument) deliveryControl(ctx context.Context, token mooring.SessionControl, q protocol.DeliveryRenew) error {
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.Fence != d.wireFence() {
		return errCollaborationHTTP
	}
	if a := d.agentParticipant("", string(q.ParticipantRef), ""); a != nil {
		return a.renew(ctx, token, q)
	}
	c := d.participantConnection("", string(q.ParticipantRef))
	if c == nil {
		return errCollaborationHTTP
	}
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	completeRetired := func() (bool, error) {
		c.mu.Lock()
		r := c.retirements[string(q.GateID)]
		c.mu.Unlock()
		if r == nil {
			return false, nil
		}
		return true, d.cell.runtime.CompleteRetiredDeliveryControl(token, r.proof)
	}
	if handled, err := completeRetired(); handled {
		return err
	}
	c.mu.Lock()
	valid := !c.closed && c.participantBinding != nil && q.GateID == protocol.Identifier(c.joined.gate.GateID) && q.PermissionVersion == protocol.Counter(c.joined.gate.PermissionVersion)
	c.mu.Unlock()
	if !valid {
		return errCollaborationHTTP
	}
	if _, err := c.startGateTransition(ctx); err != nil {
		return err
	}
	if handled, err := completeRetired(); handled {
		return err
	}
	result, err := d.renewDelivery(ctx, q)
	if err != nil {
		// A real SQL transition can happen after the preceding check. Re-prove
		// the original closed row; never convert this generic error into a grant.
		if _, proofErr := c.startGateTransition(ctx); proofErr != nil {
			return proofErr
		}
		if handled, completionErr := completeRetired(); handled {
			return completionErr
		}
		return err
	}
	return collaborationPumpDeliveryReply(ctx, d.cell.runtime, token, q, result)
}

// Ticket and Joined remain on ONE serial management dispatcher. Only DATA
// enters the separate opaque-token FIFO, preserving the physical Ticket observer.
func (d *collaborationHTTPDocument) outputControl(ctx context.Context, token mooring.SessionControl, q protocol.SessionParticipantOutput) error {
	if q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.RuntimeRef != protocol.Identifier(d.cell.runtime.RuntimeRef()) || q.Fence != d.wireFence() {
		return errCollaborationHTTP
	}
	if a := d.agentParticipant(string(q.ConnectionRef), string(q.ParticipantRef), ""); a != nil {
		return a.output(ctx, token, q)
	}
	c := d.participantConnection(string(q.ConnectionRef), string(q.ParticipantRef))
	if c == nil {
		return errCollaborationHTTP
	}
	raw, err := d.manager.schema.ValidateE3(q.Message)
	if err != nil {
		return err
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return errCollaborationHTTP
	}
	if header.Type == "session_joined" {
		c.mu.Lock()
		pending := c.participantPending
		if c.closed || pending == nil {
			c.mu.Unlock()
			return errCollaborationHTTP
		}
		binding, err := d.cell.runtime.CaptureParticipantJoined(d.activation, pending, token)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		c.participantBinding = binding
		c.participantPending = nil
		transition := c.transition
		if transition != nil {
			if !c.joinedDelivered {
				c.mu.Unlock()
				return errCollaborationHTTP
			}
			c.transition = nil
			close(transition.ready)
			c.mu.Unlock()
			return nil // Existing connection/source; not a new public admission.
		}
		c.mu.Unlock()
		return d.output(ctx, q)
	}
	c.mu.Lock()
	retired := c.retirements[string(q.GateID)]
	validCurrent := !c.closed && c.participantBinding != nil && q.GateID == protocol.Identifier(c.joined.gate.GateID) && q.PermissionVersion == protocol.Counter(c.joined.gate.PermissionVersion)
	closed := c.closed
	c.mu.Unlock()
	if retired == nil && !validCurrent {
		return errCollaborationHTTP
	}
	if !closed {
		err = c.ensureCurrentGate(ctx)
		if err != nil && !errors.Is(err, errCollaborationRecipientDenied) {
			return err
		}
	}
	c.mu.Lock()
	retired = c.retirements[string(q.GateID)]
	denied := c.closed
	c.mu.Unlock()
	if retired != nil {
		if err = d.cell.runtime.ValidateRetiredOutput(token, retired.proof); err != nil {
			return err
		}
		return c.committedOutputRecipient(q, raw, header.Type, retired.gate, denied)
	}
	if denied {
		return errCollaborationHTTP
	}
	return c.committedOutput(q, raw, header.Type)
}

// Serial management callback: a successful original-vector denial after Open
// consumes only that pending ticket through the fixed physical negative reply.
func (d *collaborationHTTPDocument) ticketControl(ctx context.Context, token mooring.SessionControl, q protocol.TicketValidationRequest) error {
	if a := d.agentParticipant("", "", q.Ticket); a != nil {
		return a.ticketReply(ctx, token, q)
	}
	d.mu.Lock()
	var c *collaborationHTTPConnection
	for _, candidate := range d.connections {
		candidate.mu.Lock()
		match := candidate.ticket == q.Ticket
		candidate.mu.Unlock()
		if match {
			c = candidate
			break
		}
	}
	d.mu.Unlock()
	if c == nil {
		return errCollaborationHTTP
	}
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	c.mu.Lock()
	pending := c.participantPending
	c.mu.Unlock()
	result, err := d.ticket(ctx, q)
	if err == nil {
		return collaborationPumpTicketReply(ctx, d.cell.runtime, token, q, result)
	}
	if !errors.Is(err, errCollaborationAuthorChanged) {
		return err
	}
	if err = d.cell.runtime.DeclineParticipantTicket(ctx, d.activation, pending, token); err != nil {
		return err
	}
	c.mu.Lock()
	c.participantPending = nil
	if t := c.transition; t != nil {
		t.err = errCollaborationRecipientDenied
		close(t.ready)
	}
	c.mu.Unlock()
	c.closeRetiredRecipient() // negative already written: no second Leave
	return nil
}
