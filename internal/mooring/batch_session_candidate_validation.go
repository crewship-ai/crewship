package mooring

import (
	"context"
	"database/sql"
	"sync"
)

// SessionNativeCandidateValidator validates source/raw/namespace DATA from the
// actual application candidate before SQL can save it. It is not an authority
// override. The captured host integration must retain burned writer identifiers
// even if this writer later rolls back; rollback cannot make an origin fresh.
type SessionNativeCandidateValidator func(context.Context, *sql.Tx, SessionNativeCandidateAccess) error

// SessionNativeCandidateAccess is valid only during this exact callback. No
// scalar origin, copied application candidate or exported constructor replaces
// the actual original store, owner and host-sealed submission binding.
type SessionNativeCandidateAccess struct{ state *sessionNativeCandidateState }
type sessionNativeCandidateState struct {
	mu        sync.Mutex
	active    bool
	store     *BatchStore
	lease     OwnerLease
	candidate SessionPreparedCandidate
}

func (a SessionNativeCandidateAccess) FromStore(s *BatchStore) bool {
	if a.state == nil || s == nil {
		return false
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	return a.state.active && a.state.store == s
}
func (a SessionNativeCandidateAccess) Lease() (OwnerLease, error) {
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
func (a SessionNativeCandidateAccess) Candidate() (SessionPreparedCandidate, error) {
	if a.state == nil {
		return SessionPreparedCandidate{}, ErrInvalid
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if !a.state.active {
		return SessionPreparedCandidate{}, ErrInvalid
	}
	return cloneSessionPreparedCandidate(a.state.candidate), nil
}
func (m *sessionCommitMode) validateNativeCandidate(ctx context.Context, tx *sql.Tx, lease OwnerLease, change BatchChange, revision string, candidate BatchTargetCandidate) error {
	if m == nil || m.fullBasis == nil || m.humanResolver == nil {
		return nil
	}
	if m.nativeValidator == nil || m.originalStore == nil {
		return ErrInvalid
	}
	delta, err := freezeSessionNativeDelta(candidate.NativeDelta, candidate.Effects)
	if err != nil {
		return err
	}
	state := &sessionNativeCandidateState{active: true, store: m.originalStore, lease: lease, candidate: SessionPreparedCandidate{cloneBatchChange(change), revision, canonicalDigest(candidate.Checkpoint), *delta}}
	defer func() { state.mu.Lock(); state.active = false; state.mu.Unlock() }()
	return m.nativeValidator(ctx, tx, SessionNativeCandidateAccess{state})
}
func (s *BatchStore) hasSessionNativeCallbacks() bool {
	return s != nil && s.config.Targets != nil && s.config.Targets.SessionHumanPrepare != nil && s.config.Targets.SessionNativeCandidate != nil
}
