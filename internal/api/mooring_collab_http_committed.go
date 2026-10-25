package api

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// This is called only by the SAME SQL writer's output preparer. Reserving bytes
// does not publish Saved: pending receipts are inaccessible to delivery lookup.
// A rollback burns this bounded reservation until the process is retired.
func (l *collaborationLiveOriginals) preparePublicReceipt(group *collaborationOriginalGroup, private protocol.CommitResult) (*protocol.CommitResult, error) {
	if l == nil || group == nil {
		return nil, errCollaborationHTTP
	}
	public := private
	public.RequestID = group.requestID
	encode := func(receipt protocol.CommitResult) ([]byte, error) {
		raw, err := json.Marshal(receipt)
		if err != nil || len(raw) > collaborationHTTPFrameLimit {
			return nil, mooring.ErrBatchCapacity
		}
		return mooring.CanonicalJSON(raw)
	}
	privateBytes, err := encode(private)
	if err != nil {
		return nil, err
	}
	publicBytes, err := encode(public)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !group.captured || len(group.changes) != len(private.Changes) {
		return nil, errCollaborationHTTP
	}
	for i, outcome := range private.Changes {
		slot := l.changes[group.changes[i]]
		if slot == nil || slot.group != group || !slot.captured || slot.captureErr != nil || outcome.ChangeID != protocol.Identifier(slot.change.ID) || len(outcome.Operations) != len(slot.change.Operations) {
			return nil, errCollaborationHTTP
		}
		for j, operation := range outcome.Operations {
			var bound struct {
				OpID protocol.Identifier `json:"op_id"`
			}
			if json.Unmarshal(operation, &bound) != nil || bound.OpID != protocol.Identifier(slot.change.Operations[j].ID) {
				return nil, errCollaborationHTTP
			}
		}
	}
	if group.pendingPrivate != nil {
		if !bytes.Equal(group.pendingPrivate, privateBytes) || !bytes.Equal(group.pendingPublic, publicBytes) {
			return nil, errCollaborationHTTP
		}
	} else {
		if len(privateBytes)+len(publicBytes) > mooring.MaxPrivatePrepareRPCFrame-l.receiptBytes {
			return nil, mooring.ErrBatchCapacity
		}
		l.receiptBytes += len(privateBytes) + len(publicBytes)
		group.pendingPrivate, group.pendingPublic = privateBytes, publicBytes
	}
	var owned protocol.CommitResult
	if json.Unmarshal(publicBytes, &owned) != nil {
		return nil, errCollaborationHTTP
	}
	return &owned, nil
}

// Only submitWithCuts calls this after actual CommitFull success. Exported core
// result DATA alone is not an admission, native replay or external-read proof.
// Verify every entry first, then publish the complete live history atomically.
func (l *collaborationLiveOriginals) publishCommitted(group *collaborationOriginalGroup, changes []mooring.BatchChange, result protocol.SessionCommitResult, cuts []mooring.SessionSavedCut, deltas []mooring.SessionCommittedNativeDelta) error {
	if l == nil || group == nil || result.PublicReceipt == nil || result.NativeDeltas == nil || len(changes) != len(result.Receipt.Changes) || len(*result.NativeDeltas) != len(deltas) {
		return errCollaborationHTTP
	}
	cutByID := make(map[string]mooring.SessionSavedCut, len(cuts))
	for _, cut := range cuts {
		if _, duplicate := cutByID[cut.ChangeID]; duplicate || !collaborationOriginalRevision(cut.Revision) || cut.Revision == "0" || !collaborationStateDigest(cut.StateDigest) {
			return errCollaborationHTTP
		}
		cutByID[cut.ChangeID] = cut
	}
	deltaByID := make(map[string]mooring.SessionCommittedNativeDelta, len(deltas))
	for i, delta := range deltas {
		wire := (*result.NativeDeltas)[i]
		if _, duplicate := deltaByID[delta.ChangeID]; duplicate || string(wire.ChangeID) != delta.ChangeID || string(wire.MembershipHash) != delta.MembershipHash || string(wire.Revision) != delta.Revision || string(wire.CheckpointStateDigest) != delta.StateDigest || !reflect.DeepEqual(wire.NativeDelta, delta.NativeDelta) {
			return errCollaborationHTTP
		}
		cut, found := cutByID[delta.ChangeID]
		if !found || cut.MembershipHash != delta.MembershipHash || cut.Revision != delta.Revision || cut.StateDigest != delta.StateDigest {
			return errCollaborationHTTP
		}
		deltaByID[delta.ChangeID] = delta
	}
	encode := func(receipt protocol.CommitResult) ([]byte, error) {
		raw, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		return mooring.CanonicalJSON(raw)
	}
	privateBytes, err := encode(result.Receipt)
	if err != nil {
		return err
	}
	publicBytes, err := encode(*result.PublicReceipt)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !group.captured || len(changes) != len(group.changes) || !bytes.Equal(group.pendingPrivate, privateBytes) || !bytes.Equal(group.pendingPublic, publicBytes) {
		return errCollaborationHTTP
	}
	type publication struct {
		slot     *collaborationLiveOriginal
		saved    *collaborationSavedOriginal
		rejected bool
	}
	plans := make([]publication, 0, len(changes))
	for i, change := range changes {
		slot := l.changes[change.ID]
		outcome := result.Receipt.Changes[i]
		if group.changes[i] != change.ID || slot == nil || slot.group != group || !slot.captured || slot.captureErr != nil || !reflect.DeepEqual(slot.change, change) || outcome.ChangeID != protocol.Identifier(change.ID) {
			return errCollaborationHTTP
		}
		cut, hasCut := cutByID[change.ID]
		delta, fresh := deltaByID[change.ID]
		switch outcome.Status {
		case "saved":
			if !hasCut || cut.MembershipHash != change.MembershipHash || outcome.LastRevision == nil || string(*outcome.LastRevision) != cut.Revision || slot.rejected {
				return errCollaborationHTTP
			}
			delete(cutByID, change.ID)
			if !fresh {
				// A legacy/uncertain history gap cannot be backfilled from payload.
				if slot.saved == nil || slot.saved.cut != cut {
					return errCollaborationHTTP
				}
				plans = append(plans, publication{slot: slot, saved: slot.saved})
				continue
			}
			delete(deltaByID, change.ID)
			view := collaborationOriginalView{payload: slot.payload}
			if !collaborationMatchesHumanDelta(view, delta.NativeDelta) {
				return errCollaborationHTTP
			}
			owned := delta.NativeDelta
			owned.Targets = append([]protocol.Identifier{}, owned.Targets...)
			owned.AllocationOrigins = append([]protocol.PositiveCounter{}, owned.AllocationOrigins...)
			saved := &collaborationSavedOriginal{cut: cut, delta: owned}
			if slot.saved != nil && !reflect.DeepEqual(slot.saved, saved) {
				return errCollaborationHTTP
			}
			plans = append(plans, publication{slot: slot, saved: saved})
		case "rejected":
			if hasCut || fresh || slot.saved != nil {
				return errCollaborationHTTP
			}
			plans = append(plans, publication{slot: slot, rejected: true})
		default:
			return errCollaborationHTTP
		}
	}
	if len(cutByID) != 0 || len(deltaByID) != 0 {
		return errCollaborationHTTP
	}
	var private, public protocol.CommitResult
	if json.Unmarshal(group.pendingPrivate, &private) != nil || json.Unmarshal(group.pendingPublic, &public) != nil {
		return errCollaborationHTTP
	}
	for _, plan := range plans {
		plan.slot.saved, plan.slot.rejected = plan.saved, plan.rejected
		if plan.rejected {
			plan.slot.source.retired = true
		}
	}
	group.privateReceipt, group.receipt = &private, &public
	return nil
}
