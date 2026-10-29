package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// SessionHumanPrepareResolver is an immutable host integration hook, never
// participant authority. Only SessionCommitFull invokes it under its original
// SQL writer after sealed-operation and current write checks. A human original
// without a qualified host slot must return an error, never a command fallback.
type SessionHumanPrepareResolver func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error)

// SessionNativeTargetBatchAdapter is an explicit session-only application
// capability. The actual runtime factory must verify its human capability
// negotiation; this interface alone grants no source or write authority.
type SessionNativeTargetBatchAdapter interface {
	CausalTargetBatchAdapter
	PrepareSessionTargets(context.Context, []byte, BatchChange, BatchCausalPrepareContext, *protocol.SessionHumanPrepareContext) (BatchTargetCandidate, *PreparedFailure, error)
}

// SessionHumanPrepareAccess borrows one actual writer invocation. Copies retain
// the same lifetime and cannot be used after the resolver returns. Returned
// checkpoint data is owned; it is not an external read or delivery capability.
type SessionHumanPrepareAccess struct{ state *sessionHumanPrepareState }
type sessionHumanPrepareContextKey struct{}
type sessionHumanPrepareState struct {
	mu         sync.Mutex
	active     bool
	store      *BatchStore
	tx         *sql.Tx
	lease      OwnerLease
	change     BatchChange
	checkpoint []byte
	revision   string
	loaded     map[string]struct{}
	prepared   SessionPreparedCandidates
}

func (a SessionHumanPrepareAccess) FromStore(store *BatchStore) bool {
	if a.state == nil || store == nil {
		return false
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	return a.state.active && a.state.store == store
}
func (a SessionHumanPrepareAccess) Lease() (OwnerLease, error) {
	if a.state == nil {
		return OwnerLease{}, ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if !a.state.active {
		return OwnerLease{}, ErrInvalid
	}
	return a.state.lease, nil
}
func (a SessionHumanPrepareAccess) Change() (BatchChange, error) {
	if a.state == nil {
		return BatchChange{}, ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if !a.state.active {
		return BatchChange{}, ErrInvalid
	}
	return cloneBatchChange(a.state.change), nil
}
func (a SessionHumanPrepareAccess) CurrentCheckpoint() ([]byte, error) {
	if a.state == nil {
		return nil, ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if !a.state.active {
		return nil, ErrInvalid
	}
	return append([]byte(nil), a.state.checkpoint...), nil
}
func (a SessionHumanPrepareAccess) DocumentRevision() (string, error) {
	if a.state == nil {
		return "", ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if !a.state.active {
		return "", ErrInvalid
	}
	return a.state.revision, nil
}

// LoadCheckpoint resolves ONLY a revision already selected by the host's
// immutable observed-basis record. The resolver must compare the returned full
// identity or raw-state digest with that record. It may load at most two
// distinct historical checkpoints: original B basis and optional common replay
// basis. No arbitrary external historical-read endpoint is introduced.
func (a SessionHumanPrepareAccess) LoadCheckpoint(ctx context.Context, revision string) (protocol.Checkpoint, string, error) {
	var empty protocol.Checkpoint
	if a.state == nil || ctx == nil {
		return empty, "", ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	s := a.state
	if !s.active || ctx.Err() != nil || ctx.Value(sessionHumanPrepareContextKey{}) != s {
		return empty, "", ErrInvalid
	}
	n, err := batchCounter(revision)
	head, headErr := batchCounter(s.revision)
	if s.prepared.TransactionRevision != "" {
		durable, e := batchCounter(s.prepared.TransactionRevision)
		if e != nil || n > durable {
			return empty, "", ErrInvalid
		}
	}
	if err != nil || headErr != nil || n > head {
		return empty, "", ErrInvalid
	}
	if _, found := s.loaded[revision]; !found && len(s.loaded) >= 2 {
		return empty, "", ErrBatchCapacity
	}
	if err = s.store.config.Owners.Check(ctx, s.tx, s.lease); err != nil {
		return empty, "", err
	}
	cp, identity, err := s.store.loadRetainedFullCheckpoint(ctx, s.tx, s.lease.Key, revision, false)
	if err != nil {
		return empty, "", err
	}
	s.loaded[revision] = struct{}{}
	cp.Assets = append([]protocol.Asset{}, cp.Assets...)
	return cp, canonicalDigest(identity.State), nil
}

func (m *sessionCommitMode) resolveHumanPrepare(ctx context.Context, tx *sql.Tx, lease OwnerLease, change BatchChange, checkpoint []byte, revision string) (*protocol.SessionHumanPrepareContext, error) {
	if m == nil || m.fullBasis == nil || m.originalStore == nil || m.humanResolver == nil {
		return nil, ErrInvalid
	}
	state := &sessionHumanPrepareState{active: true, store: m.originalStore, tx: tx, lease: lease, change: cloneBatchChange(change), checkpoint: append([]byte(nil), checkpoint...), revision: revision, loaded: make(map[string]struct{}), prepared: m.prepared}
	closeAccess := func() { state.mu.Lock(); state.active = false; state.mu.Unlock() }
	defer closeAccess()
	bound := context.WithValue(ctx, sessionHumanPrepareContextKey{}, state)
	human, err := m.humanResolver(bound, tx, SessionHumanPrepareAccess{state})
	closeAccess()
	if err != nil || human == nil {
		return nil, err
	}
	// Freeze nested source/basis/history DATA before application preparation;
	// the trusted resolver cannot later mutate the request through aliases.
	raw, err := json.Marshal(human)
	if err != nil || len(raw) > MaxPrivatePrepareRPCFrame {
		return nil, ErrBatchCapacity
	}
	if _, err = canonicalPrivateControlJSON(raw); err != nil {
		return nil, err
	}
	var frozen protocol.SessionHumanPrepareContext
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, ErrInvalid
	}
	return &frozen, nil
}
