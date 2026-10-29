package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// The child supplies transport DATA. Only receipts and native deltas retained
// after this host's actual SQL COMMIT can qualify these two output shapes.
// The original current-reader SQL gate still covers every physical send.
func (c *collaborationHTTPConnection) committedOutput(ctx context.Context, q protocol.SessionParticipantOutput, raw []byte, kind string) error {
	d := c.document
	if d.originals == nil {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	gate := c.joined.gate
	c.mu.Unlock()
	return c.committedOutputRecipient(ctx, q, raw, kind, gate, false)
}

// originalGate validates the unchanged old envelope; checkedSendLocked below
// independently uses the NEW current SQL gate. Denial never publishes bytes.
func (c *collaborationHTTPConnection) committedOutputRecipient(ctx context.Context, q protocol.SessionParticipantOutput, raw []byte, kind string, originalGate mooring.DeliveryGrant, denied bool) error {
	d := c.document
	if d.originals == nil {
		return errCollaborationHTTP
	}
	if kind == "document_update" {
		return c.committedDocumentUpdate(ctx, q, raw, originalGate, denied)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.joinedDelivered || (c.closed && !denied) || q.ParticipantRef != protocol.Identifier(c.admission.participant) || q.GateID != protocol.Identifier(originalGate.GateID) || q.PermissionVersion != protocol.Counter(originalGate.PermissionVersion) {
		return errCollaborationHTTP
	}
	switch kind {
	case "changes_received":
		var pending protocol.ChangesReceived
		if json.Unmarshal(raw, &pending) != nil || pending.State != "pending" || pending.Scope != d.wireScope() || pending.SessionID != protocol.Identifier(d.activation.SessionID()) {
			return errCollaborationHTTP
		}
		d.originals.mu.Lock()
		group := d.originals.groups[c.ref+"\x00"+string(pending.RequestID)]
		valid := group != nil && group.captured && group.sent && group.source != nil && group.source.connection == c && q.RequestID == group.outerID && len(group.changes) == len(pending.ChangeIds)
		if valid {
			for i, id := range pending.ChangeIds {
				if string(id) != group.changes[i] {
					valid = false
					break
				}
			}
		}
		d.originals.mu.Unlock()
		if !valid {
			return errCollaborationHTTP
		}
		// Queue admission only; this neither publishes history nor marks Saved.
		if denied {
			return nil
		}
		return c.checkedSendLocked(raw)
	case "commit_result":
		var receipt protocol.CommitResult
		if json.Unmarshal(raw, &receipt) != nil {
			return errCollaborationHTTP
		}
		d.originals.mu.Lock()
		group := d.originals.groups[c.ref+"\x00"+string(receipt.RequestID)]
		valid := group != nil && group.source != nil && group.source.connection == c && group.captured && group.sent && group.receipt != nil && q.RequestID == group.outerID && collaborationEqualJSON(raw, group.receipt)
		d.originals.mu.Unlock()
		if !valid {
			return errCollaborationHTTP
		}
		if denied {
			return nil
		}
		return c.checkedSendLocked(raw)

	default:
		return errCollaborationHTTP
	}
}

func collaborationEqualJSON(raw []byte, value any) bool {
	expected, err := json.Marshal(value)
	if err != nil {
		return false
	}
	a, ae := mooring.CanonicalJSON(raw)
	b, be := mooring.CanonicalJSON(expected)
	return ae == nil && be == nil && bytes.Equal(a, b)
}

func collaborationEqualJSONReceipt(actual protocol.CommitResult, expected *protocol.CommitResult) bool {
	raw, err := json.Marshal(actual)
	return err == nil && collaborationEqualJSON(raw, expected)
}

// Recipient fanout must not collide in the transport's unchanged global request
// deduplicator. The public inner ID is the cut; this private ID also binds the
// exact authenticated connection, participant and current delivery gate.
func collaborationPeerOutputID(revision, connection, participant, gate string) (protocol.Identifier, error) {
	if !collaborationOriginalRevision(revision) || revision == "0" || !mooring.ValidWireIdentifier(connection) || !mooring.ValidWireIdentifier(participant) || !mooring.ValidWireIdentifier(gate) {
		return "", errCollaborationHTTP
	}
	raw, err := json.Marshal([]string{connection, participant, gate})
	if err != nil {
		return "", err
	}
	canonical, err := mooring.CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return protocol.Identifier("actor-update:" + revision + ":sha256:" + hex.EncodeToString(digest[:])), nil
}

// Validate the original child envelope before planning any missing DATA. Release
// recipient/RAM locks before requalification; each physical frame is serialized
// separately with its cursor publication so management renewals can progress.
func (c *collaborationHTTPConnection) committedDocumentUpdate(ctx context.Context, q protocol.SessionParticipantOutput, raw []byte, originalGate mooring.DeliveryGrant, denied bool) error {
	plan := func() ([]collaborationSavedFrame, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		d := c.document
		if !c.joinedDelivered || (c.closed && !denied) || q.ParticipantRef != protocol.Identifier(c.admission.participant) || q.GateID != protocol.Identifier(originalGate.GateID) || q.PermissionVersion != protocol.Counter(originalGate.PermissionVersion) {
			return nil, errCollaborationHTTP
		}
		var update protocol.DocumentUpdate
		if json.Unmarshal(raw, &update) != nil || update.Scope != d.wireScope() || update.SessionID != protocol.Identifier(d.activation.SessionID()) || update.Generation != d.wireFence().Generation || update.Encoding != "json" || update.RequestID != protocol.Identifier("actor-update:"+string(update.ToRevision)) {
			return nil, errCollaborationHTTP
		}
		expectedOuter, err := collaborationPeerOutputID(string(update.ToRevision), c.ref, c.admission.participant, originalGate.GateID)
		if err != nil || q.RequestID != expectedOuter {
			return nil, errCollaborationHTTP
		}
		latest, frames, err := c.savedFramesLocked()
		if err != nil {
			return nil, err
		}
		var observed collaborationObservedCut
		valid := false
		for _, frame := range frames {
			if frame.update.ToRevision == update.ToRevision {
				if valid || frame.update.FromRevision != update.FromRevision || !collaborationEqualJSONReceipt(update.Receipt, &frame.update.Receipt) || !collaborationEqualJSON([]byte(update.Data), json.RawMessage(frame.update.Data)) {
					return nil, errCollaborationHTTP
				}
				observed, valid = frame.observed, true
			}
		}
		if !valid {
			return nil, errCollaborationHTTP
		}
		if denied {
			return nil, nil
		}
		if latest == "" {
			return nil, errCollaborationHTTP
		}
		// A current FULL snapshot can already include an older queued delta.
		// Suppress that duplicate; never mint a historical observed basis for it.
		if !collaborationRevisionLess(latest, observed.revision) {
			return nil, nil
		}
		if collaborationRevisionLess(string(update.FromRevision), latest) {
			return nil, errCollaborationHTTP
		}
		chain, err := collaborationSavedChain(latest, string(update.FromRevision), frames)
		if err != nil {
			return nil, err
		}
		chain = append(chain, collaborationSavedFrame{update: update, observed: observed, raw: append([]byte(nil), raw...)})
		return chain, nil

	}
	chain, err := plan()
	if err != nil {
		return err
	}
	return c.sendSavedChain(ctx, chain)
}
