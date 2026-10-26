package api

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// One complete original public emission, including its original request ID.
// Private scheduling envelopes cannot split, merge, reorder or relabel it.
type collaborationOriginalGroup struct {
	source                *collaborationOriginalSource
	requestID, outerID    protocol.Identifier
	packet                []byte
	changes               []string
	captured, sent        bool
	receipt               *protocol.CommitResult
	privateReceipt        *protocol.CommitResult
	pendingPublic         []byte
	pendingPrivate        []byte
	agentReceiptReady     chan struct{}
	agentReceiptDelivered bool
}

func (l *collaborationLiveOriginals) capturePublic(ctx context.Context, c *collaborationHTTPConnection, raw []byte) (*collaborationOriginalGroup, error) {
	diagnostic := collaborationIngressDiagnosticFrom(ctx)
	diagnostic.at("capture_public_validate")
	if l == nil || l.document == nil || l.document.manager == nil || l.document.manager.schema == nil || c == nil || c.document != l.document || len(raw) == 0 || len(raw) > collaborationHTTPFrameLimit {
		return nil, errCollaborationHTTP
	}
	diagnostic.at("capture_public_schema")
	raw, err := l.document.manager.schema.ValidateE3(raw)
	if err != nil {
		return nil, err
	}
	diagnostic.at("capture_public_envelope")
	var public protocol.SubmitChanges
	if json.Unmarshal(raw, &public) != nil || public.Type != "submit_changes" || public.Scope != l.document.wireScope() || public.SessionID != protocol.Identifier(l.document.activation.SessionID()) || public.Versions != l.document.wireVersions() || public.TaskID != nil || !mooring.ValidWireIdentifier(string(public.RequestID)) {
		return nil, errCollaborationHTTP
	}
	diagnostic.at("capture_public_changes")
	encoded, err := json.Marshal(public.Changes)
	if err != nil {
		return nil, err
	}
	var changes []mooring.BatchChange
	if json.Unmarshal(encoded, &changes) != nil {
		return nil, errCollaborationHTTP
	}
	diagnostic.at("capture_public_normalize")
	changes, err = mooring.NormalizeSessionIngress(l.document.key, changes)
	if err != nil {
		return nil, err
	}
	diagnostic.at("capture_public_encode")
	encoded, err = json.Marshal(changes)
	if err != nil || json.Unmarshal(encoded, &public.Changes) != nil {
		return nil, errCollaborationHTTP
	}
	diagnostic.at("capture_public_packet")
	packet, err := json.Marshal(public)
	if err != nil || len(packet) > collaborationHTTPFrameLimit {
		return nil, mooring.ErrBatchCapacity
	}
	diagnostic.at("capture_public_id")
	outer, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	diagnostic.at("capture_public_source")
	source := l.sources[c]
	key := c.ref + "\x00" + string(public.RequestID)
	if source == nil || source.retired {
		l.mu.Unlock()
		return nil, errCollaborationHTTP
	}
	diagnostic.at("capture_public_duplicate")
	if old := l.groups[key]; old != nil {
		valid := old.source == source && old.captured && bytes.Equal(old.packet, packet)
		l.mu.Unlock()
		if !valid {
			return nil, errCollaborationHTTP
		}
		return old, nil
	}
	if len(l.groups) >= collaborationOriginalRecordLimit || len(packet) > collaborationOriginalByteLimit-l.bytes {
		diagnostic.capacityAt("capture_public_capacity", "capacity_records")
		if len(l.groups) < collaborationOriginalRecordLimit {
			diagnostic.capacityAt("capture_public_capacity", "capacity_bytes")
		}
		l.mu.Unlock()
		return nil, mooring.ErrBatchCapacity
	}
	for _, change := range changes {
		diagnostic.at("capture_public_change_identity")
		if l.changes[change.ID] != nil {
			l.mu.Unlock()
			return nil, errCollaborationHTTP
		}
		diagnostic.at("capture_public_operation_identity")
		for _, operation := range change.Operations {
			if l.operations[operation.ID] != nil {
				l.mu.Unlock()
				return nil, errCollaborationHTTP
			}
		}
	}
	group := &collaborationOriginalGroup{source: source, requestID: public.RequestID, outerID: protocol.Identifier(outer), packet: packet, changes: make([]string, len(changes))}
	for i, change := range changes {
		group.changes[i] = change.ID
	}
	l.bytes += len(packet)
	l.groups[key] = group
	l.mu.Unlock()
	slots, err := l.capture(ctx, c, changes)
	if err != nil {
		l.retire(c)
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	diagnostic.at("capture_public_slots")
	if source.retired || len(slots) != len(changes) {
		return nil, errCollaborationHTTP
	}
	for _, slot := range slots {
		if slot.source != source || !slot.captured || slot.captureErr != nil || slot.group != nil {
			source.retired = true
			return nil, errCollaborationHTTP
		}
	}
	for _, slot := range slots {
		slot.group = group
	}
	group.captured = true
	return group, nil
}

func (l *collaborationLiveOriginals) boundPublicGroup(changes []mooring.BatchChange) (*collaborationOriginalGroup, error) {
	if l == nil || len(changes) == 0 {
		return nil, errCollaborationHTTP
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	first := l.changes[changes[0].ID]
	if first == nil || first.group == nil || !first.group.captured || len(first.group.changes) != len(changes) {
		return nil, errCollaborationHTTP
	}
	group := first.group
	for i, change := range changes {
		slot := l.changes[change.ID]
		if group.changes[i] != change.ID || slot == nil || slot.group != group || slot.source != group.source || !slot.captured || slot.captureErr != nil || !reflect.DeepEqual(slot.change, change) {
			return nil, errCollaborationHTTP
		}
	}
	return group, nil
}

func (l *collaborationLiveOriginals) sendPublicGroup(group *collaborationOriginalGroup) (protocol.Identifier, []byte, bool, error) {
	if l == nil || group == nil {
		return "", nil, false, errCollaborationHTTP
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !group.captured || group.source == nil || group.source.retired {
		return "", nil, false, errCollaborationHTTP
	}
	if group.sent {
		// Exact retransmission is already represented by this one original
		// emission. It must not create a second actor mutation or relabel an ACK.
		return group.outerID, nil, true, nil
	}
	group.sent = true
	return group.outerID, append([]byte(nil), group.packet...), false, nil
}
