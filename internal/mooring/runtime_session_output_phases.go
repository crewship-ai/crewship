package mooring

import (
	"bytes"
	"encoding/json"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// The caller has already validated both complete private envelopes. This is
// transport bookkeeping only: it authenticates no recipient or Saved outcome.
// Exactly the Pending/terminal-receipt pair may share an original outer ID;
// arbitrary message kinds, recipients or changed membership gain no namespace.
func correlatedParticipantOutputPhases(first, second []byte) bool {
	var a, b protocol.SessionParticipantOutput
	if json.Unmarshal(first, &a) != nil || json.Unmarshal(second, &b) != nil ||
		string(a.Type) != "session_participant_output" || a.Type != b.Type {
		return false
	}
	aMessage, bMessage := a.Message, b.Message
	a.Message, b.Message = nil, nil
	// Compare every outer field, including any future generated wrapper field.
	// This is structural equality, not a hash-selected notification identity.
	aWrapper, errA := json.Marshal(a)
	bWrapper, errB := json.Marshal(b)
	if errA != nil || errB != nil || !bytes.Equal(aWrapper, bWrapper) {
		return false
	}
	var left, right struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(aMessage, &left) != nil || json.Unmarshal(bMessage, &right) != nil {
		return false
	}
	if left.Type == "commit_result" && right.Type == "changes_received" {
		aMessage, bMessage = bMessage, aMessage
		left.Type, right.Type = right.Type, left.Type
	}
	if left.Type != "changes_received" || right.Type != "commit_result" {
		return false
	}
	var pending protocol.ChangesReceived
	var terminal protocol.CommitResult
	if json.Unmarshal(aMessage, &pending) != nil || json.Unmarshal(bMessage, &terminal) != nil ||
		string(pending.State) != "pending" || pending.RequestID != terminal.RequestID ||
		pending.Scope != a.Scope || terminal.Scope != a.Scope || pending.SessionID != a.SessionID ||
		terminal.Generation != a.Fence.Generation || len(pending.ChangeIds) != len(terminal.Changes) {
		return false
	}
	for i, change := range terminal.Changes {
		if pending.ChangeIds[i] != change.ChangeID {
			return false
		}
	}
	return true
}

// Completion/retirement releases only this borrowed token. Its complementary
// phase may still be queued or blocked in physical delivery under the SAME ID.
func (s *SessionRuntime) removeChildIDLocked(entry *sessionControlEntry) {
	ids := s.childIDs[entry.requestID]
	for i, id := range ids {
		if id == entry.id {
			ids = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	if len(ids) == 0 {
		delete(s.childIDs, entry.requestID)
	} else {
		s.childIDs[entry.requestID] = ids
	}
}
