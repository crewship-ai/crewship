package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Like human originals, task originals are retained before the actor sees them.
// Agent IDs/numbers in a packet are not authority: sealAgent must bind the
// entire frozen group to this actual attached task before captured becomes true.
func (l *collaborationLiveOriginals) captureAgent(ctx context.Context, a *collaborationLiveAgent, r *http.Request, raw []byte) (*collaborationOriginalGroup, error) {
	if l == nil || a == nil || a.document != l.document || a.participant == nil || r == nil || len(raw) > collaborationHTTPFrameLimit {
		return nil, errCollaborationHTTP
	}
	canonical, err := l.document.manager.schema.ValidateE3(raw)
	if err != nil {
		return nil, err
	}
	var public protocol.SubmitChanges
	if json.Unmarshal(canonical, &public) != nil || public.Type != "submit_changes" || public.Scope != l.document.wireScope() || public.SessionID != protocol.Identifier(l.document.activation.SessionID()) || public.Versions != l.document.wireVersions() || public.TaskID == nil || string(*public.TaskID) != a.confirmed.workID {
		return nil, errCollaborationHTTP
	}
	encoded, err := json.Marshal(public.Changes)
	if err != nil {
		return nil, err
	}
	var changes []mooring.BatchChange
	if json.Unmarshal(encoded, &changes) != nil {
		return nil, errCollaborationHTTP
	}
	changes, err = mooring.NormalizeSessionIngress(l.document.key, changes)
	if err != nil {
		return nil, err
	}
	for _, change := range changes {
		if change.TaskID != a.confirmed.workID {
			return nil, errCollaborationHTTP
		}
		for _, op := range change.Operations {
			var payload struct {
				Kind string `json:"kind"`
			}
			if op.Participant != a.participant.id || json.Unmarshal(op.Payload, &payload) != nil || payload.Kind == "humancrdt.update" {
				return nil, errCollaborationHTTP
			}
		}
	}
	encoded, err = json.Marshal(changes)
	if err != nil || json.Unmarshal(encoded, &public.Changes) != nil {
		return nil, errCollaborationHTTP
	}
	packet, err := json.Marshal(public)
	if err != nil || len(packet) > collaborationHTTPFrameLimit {
		return nil, mooring.ErrBatchCapacity
	}
	outer, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	source := l.agents[a]
	if source == nil {
		if len(l.agents)+len(l.sources) >= collaborationHTTPParticipantLimit {
			l.mu.Unlock()
			return nil, mooring.ErrBatchCapacity
		}
		source = &collaborationOriginalSource{agent: a, observed: make(map[string]collaborationObservedCut)}
		l.agents[a] = source
	}
	key := a.ref + "\x00" + string(public.RequestID)
	if source.retired {
		l.mu.Unlock()
		return nil, errCollaborationHTTP
	}
	if prior := l.groups[key]; prior != nil {
		valid := prior.source == source && prior.captured && bytes.Equal(prior.packet, packet)
		l.mu.Unlock()
		if !valid {
			return nil, errCollaborationHTTP
		}
		return prior, nil
	}
	extra := len(packet)
	operations := 0
	for _, change := range changes {
		if l.changes[change.ID] != nil {
			l.mu.Unlock()
			return nil, errCollaborationHTTP
		}
		b, e := json.Marshal(change)
		if e != nil {
			l.mu.Unlock()
			return nil, e
		}
		extra += len(b)
		for _, op := range change.Operations {
			operations++
			if l.operations[op.ID] != nil {
				l.mu.Unlock()
				return nil, errCollaborationHTTP
			}
		}
	}
	if len(l.groups) >= collaborationOriginalRecordLimit || len(changes) > collaborationOriginalRecordLimit-len(l.changes) || operations > collaborationOriginalRecordLimit-len(l.operations) || extra > collaborationOriginalByteLimit-l.bytes {
		l.mu.Unlock()
		return nil, mooring.ErrBatchCapacity
	}
	group := &collaborationOriginalGroup{source: source, requestID: public.RequestID, outerID: protocol.Identifier(outer), packet: packet, changes: make([]string, len(changes)), agentReceiptReady: make(chan struct{})}
	slots := make([]*collaborationLiveOriginal, len(changes))
	for i, change := range changes {
		ref, e := collaborationOpaqueID()
		if e != nil {
			source.retired = true
			l.mu.Unlock()
			return nil, e
		}
		source.order++
		b, _ := json.Marshal(change)
		slot := &collaborationLiveOriginal{source: source, reference: ref, order: source.order, change: change, packet: b, ready: make(chan struct{}), group: group}
		slots[i] = slot
		group.changes[i] = change.ID
		l.changes[change.ID] = slot
		for _, op := range change.Operations {
			l.operations[op.ID] = slot
		}
	}
	l.bytes += extra
	l.groups[key] = group
	l.mu.Unlock()
	err = l.document.submissions.sealAgent(ctx, r, a.participant, changes)
	l.finishCapture(slots, err)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if source.retired {
		return nil, errCollaborationHTTP
	}
	group.captured = true
	return group, nil
}

// A concrete SAME-writer native candidate supplies DATA, not agent authority.
// The original task/participant/operation publication is checked independently
// before every outcome path. Namespace burns survive rollback and uncertainty.
func (l *collaborationLiveOriginals) validateAgentNative(tx *sql.Tx, access mooring.SessionNativeCandidateAccess, candidate mooring.SessionPreparedCandidate) error {
	d := l.document
	if tx == nil || !access.FromStore(d.cell.store) {
		return errCollaborationHTTP
	}
	delta := candidate.NativeDelta
	writer, err := strconv.ParseUint(string(delta.WriterOrigin), 10, 53)
	if err != nil || writer == 0 || strconv.FormatUint(writer, 10) != string(delta.WriterOrigin) {
		return errCollaborationHTTP
	}
	for _, origin := range delta.AllocationOrigins {
		if origin != delta.WriterOrigin {
			return errCollaborationHTTP
		}
	}
	encoded, err := json.Marshal(delta)
	if err != nil || len(encoded) > collaborationOriginalByteLimit {
		return mooring.ErrBatchCapacity
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	slot := l.changes[candidate.Change.ID]
	if slot == nil || !slot.captured || slot.captureErr != nil || slot.source == nil || slot.source.agent == nil || slot.source.retired || slot.source.agent.document != d || slot.change.TaskID != slot.source.agent.confirmed.workID || !reflect.DeepEqual(slot.change, candidate.Change) {
		return errCollaborationHTTP
	}
	if slot.native != nil {
		if !reflect.DeepEqual(*slot.native, delta) {
			return errCollaborationHTTP
		}
		return nil
	}
	if len(encoded) > collaborationOriginalByteLimit-l.bytes {
		return mooring.ErrBatchCapacity
	}
	d.manager.mu.Lock()
	defer d.manager.mu.Unlock()
	if d.manager.closed || d.manager.docs[d.key] != d {
		return errCollaborationHTTP
	}
	used := d.manager.usedOrigins[d.key]
	if used == nil {
		used = make(map[uint64]struct{})
		d.manager.usedOrigins[d.key] = used
	}
	if _, collision := used[writer]; collision || len(used) >= 4096 {
		return errCollaborationHTTP
	}
	used[writer] = struct{}{}
	var owned protocol.SessionNativeDelta
	if json.Unmarshal(encoded, &owned) != nil {
		return errCollaborationHTTP
	}
	slot.native = &owned
	l.bytes += len(encoded)
	return nil
}

// Called under the actual agent's serialized tool operation, ONLY with the
// transport's definite no-write proof. No IDs, bytes, seals or quota are freed.
func (l *collaborationLiveOriginals) retryUnsentAgentGroup(group *collaborationOriginalGroup, a *collaborationLiveAgent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if group == nil || a == nil || group.source == nil || group.source.agent != a || !group.captured || !group.sent || group.receipt != nil || group.privateReceipt != nil {
		return errCollaborationHTTP
	}
	key := a.ref + "\x00" + string(group.requestID)
	if l.groups[key] != group {
		return errCollaborationHTTP
	}
	group.sent = false
	return nil
}
