package mooring

import (
	"encoding/json"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// SessionPreparedCandidate is DATA borrowed from an earlier concrete application
// candidate in this SAME writer. Revision is prospective, never durable Saved.
// Original source/ref/order must still be matched to the host's sealed slot.
type SessionPreparedCandidate struct {
	Change                BatchChange
	Revision, StateDigest string
	NativeDelta           protocol.SessionNativeDelta
}

// SessionPreparedCandidates separates the durable transaction-start boundary
// from its ordered provisional candidates. No checkpoint copy per entry exists.
type SessionPreparedCandidates struct {
	TransactionRevision, TransactionStateDigest string
	Candidates                                  []SessionPreparedCandidate
}

// SessionCommittedNativeDelta is released only after the encompassing COMMIT.
// It is application DATA bound to an exact original atomic membership and cut.
type SessionCommittedNativeDelta struct {
	ChangeID, MembershipHash, Revision, StateDigest string
	NativeDelta                                     protocol.SessionNativeDelta
}

func cloneSessionPreparedCandidate(entry SessionPreparedCandidate) SessionPreparedCandidate {
	entry.Change = cloneBatchChange(entry.Change)
	entry.NativeDelta.AllocationOrigins = append([]protocol.PositiveCounter{}, entry.NativeDelta.AllocationOrigins...)
	entry.NativeDelta.Targets = append([]protocol.Identifier{}, entry.NativeDelta.Targets...)
	return entry
}
func (a SessionHumanPrepareAccess) PreparedCandidates() (SessionPreparedCandidates, error) {
	if a.state == nil {
		return SessionPreparedCandidates{}, ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if !a.state.active {
		return SessionPreparedCandidates{}, ErrInvalid
	}
	p := a.state.prepared
	p.Candidates = make([]SessionPreparedCandidate, len(a.state.prepared.Candidates))
	for i, e := range a.state.prepared.Candidates {
		p.Candidates[i] = cloneSessionPreparedCandidate(e)
	}
	return p, nil
}
func (m *sessionCommitMode) beginPreparedCandidates(revision string, checkpoint []byte) {
	m.prepared = SessionPreparedCandidates{TransactionRevision: revision, TransactionStateDigest: canonicalDigest(checkpoint), Candidates: []SessionPreparedCandidate{}}
}
func (m *sessionCommitMode) stagePreparedCandidate(change BatchChange, revision string, candidate BatchTargetCandidate) error {
	if m.fullBasis == nil || m.humanResolver == nil {
		return nil
	}
	delta, err := freezeSessionNativeDelta(candidate.NativeDelta, candidate.Effects)
	if err != nil {
		return err
	}
	raw, err := checkpointBase64(delta.Update, 262144)
	if err != nil {
		return err
	}
	if len(m.prepared.Candidates) >= 256 || len(raw) > 262144-m.preparedRawBytes {
		return ErrBatchCapacity
	}
	// Bound metadata too; neither raw update charging nor an entry-count cap alone
	// bounds arbitrary original membership. The original request remains immutable.
	entry := SessionPreparedCandidate{cloneBatchChange(change), revision, canonicalDigest(candidate.Checkpoint), *delta}
	encoded, err := json.Marshal(entry)
	if err != nil || len(encoded) > MaxPrivatePrepareRPCFrame-m.preparedBytes {
		return ErrBatchCapacity
	}
	m.preparedRawBytes += len(raw)
	m.preparedBytes += len(encoded)
	m.prepared.Candidates = append(m.prepared.Candidates, entry)
	return nil
}
func (m *sessionCommitMode) nativeDeltaData() []SessionCommittedNativeDelta {
	if len(m.prepared.Candidates) == 0 {
		return nil
	}
	result := make([]SessionCommittedNativeDelta, len(m.prepared.Candidates))
	for i, entry := range m.prepared.Candidates {
		owned := cloneSessionPreparedCandidate(entry)
		result[i] = SessionCommittedNativeDelta{owned.Change.ID, owned.Change.MembershipHash, owned.Revision, owned.StateDigest, owned.NativeDelta}
	}
	return result
}

// Marshal application DATA into the candidate output before COMMIT. The output
// callback never receives these provisional cuts and cannot supply substitutes.
func (m *sessionCommitMode) injectNativeDeltaData(raw []byte) ([]byte, error) {
	canonical, err := canonicalPrivateControlJSON(raw)
	if err != nil {
		return nil, err
	}
	raw = canonical
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil, ErrPrivateControl
	}
	if _, present := value["native_deltas"]; present {
		return nil, ErrPrivateControl
	}
	if m.humanResolver == nil {
		return raw, nil
	}
	for _, entry := range m.prepared.Candidates {
		matched := false
		for _, cut := range m.savedCuts {
			if cut.ChangeID == entry.Change.ID {
				matched = cut.MembershipHash == entry.Change.MembershipHash && cut.Revision == entry.Revision && cut.StateDigest == entry.StateDigest
			}
		}
		if !matched {
			return nil, ErrCheckpointIdentity
		}
	}

	data, err := json.Marshal(wireSessionNativeDeltas(m.nativeDeltaData()))
	if err != nil {
		return nil, err
	}
	value["native_deltas"] = data
	return json.Marshal(value)
}
func wireSessionNativeDeltas(entries []SessionCommittedNativeDelta) []protocol.SessionCommittedNativeDelta {
	out := make([]protocol.SessionCommittedNativeDelta, 0, len(entries))
	for _, e := range entries {
		out = append(out, protocol.SessionCommittedNativeDelta{ChangeID: protocol.Identifier(e.ChangeID), MembershipHash: protocol.Digest(e.MembershipHash), Revision: protocol.PositiveCounter(e.Revision), CheckpointStateDigest: protocol.Digest(e.StateDigest), NativeDelta: e.NativeDelta})
	}
	return out
}
