package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// The child supplies transport DATA. Only receipts and native deltas retained
// after this host's actual SQL COMMIT can qualify these two output shapes.
// The original current-reader SQL gate still covers every physical send.
func (c *collaborationHTTPConnection) committedOutput(q protocol.SessionParticipantOutput, raw []byte, kind string) error {
	d := c.document
	if d.originals == nil {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	gate := c.joined.gate
	c.mu.Unlock()
	return c.committedOutputRecipient(q, raw, kind, gate, false)
}

// originalGate validates the unchanged old envelope; checkedSendLocked below
// independently uses the NEW current SQL gate. Denial never publishes bytes.
func (c *collaborationHTTPConnection) committedOutputRecipient(q protocol.SessionParticipantOutput, raw []byte, kind string, originalGate mooring.DeliveryGrant, denied bool) error {
	d := c.document
	if d.originals == nil {
		return errCollaborationHTTP
	}
	switch kind {
	case "commit_result":
		mooringDiagRecord(diagTerminalObserved, c)
	case "changes_received":
		mooringDiagRecord(diagPendingObserved, c)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.joinedDelivered || (c.closed && !denied) || q.ParticipantRef != protocol.Identifier(c.admission.participant) || q.GateID != protocol.Identifier(originalGate.GateID) || q.PermissionVersion != protocol.Counter(originalGate.PermissionVersion) {
		mooringDiagRecord(diagOutputEnvelopeReject, c)
		return errCollaborationHTTP
	}
	switch kind {
	case "changes_received":
		mooringDiagRecord(diagPendingEntered, c)
		var pending protocol.ChangesReceived
		if json.Unmarshal(raw, &pending) != nil || pending.State != "pending" || pending.Scope != d.wireScope() || pending.SessionID != protocol.Identifier(d.activation.SessionID()) {
			mooringDiagRecord(diagPendingReject, c)
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
			mooringDiagRecord(diagPendingReject, c)
			return errCollaborationHTTP
		}
		// Queue admission only; this neither publishes history nor marks Saved.
		mooringDiagRecord(diagPendingQualified, c)
		if denied {
			mooringDiagRecord(diagPendingSuppressed, c)
			return nil
		}
		return c.checkedSendLocked(raw)
	case "commit_result":
		mooringDiagRecord(diagTerminalEntered, c)
		var receipt protocol.CommitResult
		if json.Unmarshal(raw, &receipt) != nil {
			mooringDiagRecord(diagTerminalReject, c)
			return errCollaborationHTTP
		}
		d.originals.mu.Lock()
		group := d.originals.groups[c.ref+"\x00"+string(receipt.RequestID)]
		valid := group != nil && group.source != nil && group.source.connection == c && group.captured && group.sent && group.receipt != nil && q.RequestID == group.outerID && collaborationEqualJSON(raw, group.receipt)
		d.originals.mu.Unlock()
		if !valid {
			mooringDiagRecord(diagTerminalReject, c)
			return errCollaborationHTTP
		}
		mooringDiagRecord(diagTerminalQualified, c)
		if denied {
			mooringDiagRecord(diagTerminalSuppressed, c)
			return nil
		}
		return c.checkedSendLocked(raw)
	case "document_update":
		var update protocol.DocumentUpdate
		if json.Unmarshal(raw, &update) != nil || update.Scope != d.wireScope() || update.SessionID != protocol.Identifier(d.activation.SessionID()) || update.Generation != d.wireFence().Generation || update.Encoding != "json" || update.RequestID != protocol.Identifier("actor-update:"+string(update.ToRevision)) {
			return errCollaborationHTTP
		}
		expectedOuter, err := collaborationPeerOutputID(string(update.ToRevision), c.ref, c.admission.participant, originalGate.GateID)
		if err != nil || q.RequestID != expectedOuter {
			return errCollaborationHTTP
		}
		d.originals.mu.Lock()
		var observed collaborationObservedCut
		valid := false
		for _, slot := range d.originals.changes {
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
			if outcome == nil || outcome.FirstRevision == nil || outcome.LastRevision == nil || *outcome.FirstRevision != *outcome.LastRevision {
				break
			}
			first, err := strconv.ParseUint(string(*outcome.FirstRevision), 10, 64)
			if err != nil || first == 0 || update.FromRevision != protocol.Counter(strconv.FormatUint(first-1, 10)) {
				break
			}
			expected := struct {
				Kind    string                `json:"kind"`
				Update  string                `json:"update"`
				Targets []protocol.Identifier `json:"targets"`
			}{"wordcraft.yrs-update.v1", slot.saved.delta.Update, slot.saved.delta.Targets}
			if !collaborationEqualJSON([]byte(update.Data), expected) {
				break
			}
			observed = collaborationObservedCut{revision: slot.saved.cut.Revision, stateDigest: slot.saved.cut.StateDigest}
			valid = true
			break
		}
		var latest string
		if source := d.originals.sources[c]; source != nil && !source.retired {
			for revision := range source.observed {
				if latest == "" || collaborationRevisionLess(latest, revision) {
					latest = revision
				}
			}
		}
		d.originals.mu.Unlock()
		if !valid {
			return errCollaborationHTTP
		}
		if denied {
			return nil
		}
		if latest == "" {
			return errCollaborationHTTP
		}
		// A current FULL snapshot can already include an older queued delta.
		// Suppress that duplicate; never mint a historical observed basis for it.
		if !collaborationRevisionLess(latest, observed.revision) {
			return nil
		}
		if latest != string(update.FromRevision) {
			return errCollaborationHTTP
		}
		if err := c.checkedSendLocked(raw); err != nil {
			return err
		}
		return d.originals.observe(c, observed)
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
