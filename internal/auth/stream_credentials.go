package auth

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// StreamCredentials tracks only active streams. It is not an authentication
// store: callers must validate the credential in the database between Epoch
// and Track. Scoped counters exist only while a concurrent admission can need them.
// No historical revoked identities or cached allow decisions remain.
// The zero value is ready to use.
type StreamCredentials struct {
	mu           sync.Mutex
	epoch        uint64
	pending      map[uint64]*list.Element
	pendingOrder list.List
	revocations  map[streamRevocationKey]uint64
	active       map[*streamCredentialState]struct{}
}

type streamCredentialKey struct{}

type streamRevocationKey struct {
	kind string
	id   string
}

type streamCredentialState struct {
	userID, sessionID, tokenID string
	expiresAt                  time.Time
	valid                      atomic.Bool
	cancel                     context.CancelFunc
	timer                      *time.Timer
	stopContext                func() bool
	validate                   func(context.Context) bool
}

// Epoch captures an admission snapshot before database validation. The caller
// must defer ReleaseEpoch on all paths; Track also releases it. Only revocations
// of the eventual credential or its user can invalidate this snapshot.
func (r *StreamCredentials) Epoch() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epoch++
	if r.pending == nil {
		r.pending = make(map[uint64]*list.Element)
	}
	r.pending[r.epoch] = r.pendingOrder.PushBack(r.epoch)
	return r.epoch
}

// ReleaseEpoch is idempotent and must also cover failed/canceled validation.
func (r *StreamCredentials) ReleaseEpoch(epoch uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseEpochLocked(epoch)
}

func (r *StreamCredentials) releaseEpochLocked(epoch uint64) {
	node, exists := r.pending[epoch]
	if !exists {
		return
	}
	oldestChanged := r.pendingOrder.Front() == node
	delete(r.pending, epoch)
	r.pendingOrder.Remove(node)
	if !oldestChanged {
		return
	}
	if len(r.pending) == 0 {
		clear(r.revocations)
		return
	}
	oldest := r.pendingOrder.Front().Value.(uint64)

	for key, revoked := range r.revocations {
		if revoked <= oldest {
			delete(r.revocations, key)
		}
	}
}

// Track binds a stream to exactly one validated session or token row ID. These
// are identifiers, never credential values. A zero expiresAt permits a credential
// with no deadline; parent cancellation and revocation still apply. The caller
// must defer the returned cleanup. Rejected admission returns a canceled tracked
// context, so it cannot become an untracked legacy allow.
// An optional fresh validator runs at each delivery fence, outside registry locks;
// its failure cancels only this tracked context. It must use its supplied context.
func (r *StreamCredentials) Track(ctx context.Context, expectedEpoch uint64, userID, sessionID, tokenID string, expiresAt time.Time, validate ...func(context.Context) bool) (context.Context, func()) {
	if ctx == nil {
		r.ReleaseEpoch(expectedEpoch)
		ctx = context.Background()
		return rejectedStreamCredential(ctx)
	}
	r.mu.Lock()
	_, pending := r.pending[expectedEpoch]
	kind, credential := "session", sessionID
	if tokenID != "" {
		kind, credential = "token", tokenID
	}
	stale := r.revocations[streamRevocationKey{kind, credential}] > expectedEpoch ||
		r.revocations[streamRevocationKey{"user-" + kind, userID}] > expectedEpoch ||
		r.revocations[streamRevocationKey{"account", userID}] > expectedEpoch
	r.releaseEpochLocked(expectedEpoch)
	if !pending || stale || ctx.Err() != nil || userID == "" || len(validate) > 1 ||
		(sessionID == "") == (tokenID == "") || (!expiresAt.IsZero() && !time.Now().Before(expiresAt)) {
		r.mu.Unlock()
		return rejectedStreamCredential(ctx)
	}
	child, cancel := context.WithCancel(ctx)
	s := &streamCredentialState{userID: userID, sessionID: sessionID, tokenID: tokenID, expiresAt: expiresAt, cancel: cancel}
	if len(validate) == 1 {
		s.validate = validate[0]
	}
	s.valid.Store(true)
	if r.active == nil {
		r.active = make(map[*streamCredentialState]struct{})
	}
	r.active[s] = struct{}{}
	// Callbacks take mu too; all callback handles are initialized before unlock.
	if !expiresAt.IsZero() {
		s.timer = time.AfterFunc(time.Until(expiresAt), func() { r.remove(s) })
	}
	s.stopContext = context.AfterFunc(child, func() { r.remove(s) })
	r.mu.Unlock()
	return context.WithValue(child, streamCredentialKey{}, s), func() { r.remove(s) }
}

func rejectedStreamCredential(ctx context.Context) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	cancel()
	return context.WithValue(child, streamCredentialKey{}, &streamCredentialState{}), func() {}
}

// StreamCredentialCurrent is a per-delivery fence, including a direct clock
// check even when an expiry callback is delayed. Absence remains compatible
// with legacy direct-handler tests; production consumers must call Track.
func StreamCredentialCurrent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	s, ok := ctx.Value(streamCredentialKey{}).(*streamCredentialState)
	if !ok {
		return ctx.Err() == nil
	}
	current := func() bool {
		return s != nil && s.valid.Load() && ctx.Err() == nil && (s.expiresAt.IsZero() || time.Now().Before(s.expiresAt))
	}
	if !current() {
		return false
	}
	// Never hold the registry mutex across external I/O. Notify/expiry may
	// invalidate this same state during the check; recheck it before delivery.
	if s.validate != nil && !s.validate(ctx) {
		s.valid.Store(false)
		s.cancel()
		return false
	}
	return current()
}

func (r *StreamCredentials) remove(s *streamCredentialState) {
	r.mu.Lock()
	if _, exists := r.active[s]; !exists {
		r.mu.Unlock()
		return
	}
	delete(r.active, s)
	s.valid.Store(false)
	r.mu.Unlock()
	s.finish()
}

func (s *streamCredentialState) finish() {
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.stopContext != nil {
		s.stopContext()
	}
	s.cancel()
}

func (r *StreamCredentials) revoke(key streamRevocationKey, matches func(*streamCredentialState) bool) {
	if key.id == "" {
		return
	}
	r.mu.Lock()
	r.epoch++
	if len(r.pending) > 0 {
		if r.revocations == nil {
			r.revocations = make(map[streamRevocationKey]uint64)
		}
		r.revocations[key] = r.epoch
	}
	var revoked []*streamCredentialState
	for s := range r.active {
		if matches(s) {
			delete(r.active, s)
			s.valid.Store(false)
			revoked = append(revoked, s)
		}
	}
	r.mu.Unlock()
	// Invalidate under the lock, but invoke cancellation outside it.
	for _, s := range revoked {
		s.finish()
	}
}

func (r *StreamCredentials) NotifySessionRevoked(sessionID string) {
	r.revoke(streamRevocationKey{"session", sessionID}, func(s *streamCredentialState) bool { return sessionID != "" && s.sessionID == sessionID })
}

// NotifyUserRevoked matches the session-store revoke-all contract: CLI tokens
// remain separate credentials. NotifyAccountRevoked covers both kinds.
func (r *StreamCredentials) NotifyUserRevoked(userID string) {
	r.revoke(streamRevocationKey{"user-session", userID}, func(s *streamCredentialState) bool { return userID != "" && s.userID == userID && s.sessionID != "" })
}

func (r *StreamCredentials) NotifyTokenRevoked(tokenID string) {
	r.revoke(streamRevocationKey{"token", tokenID}, func(s *streamCredentialState) bool { return tokenID != "" && s.tokenID == tokenID })
}

// NotifyTokensRevoked covers a user's CLI tokens without revoking browser
// sessions, including a password change's explicitly retained session.
func (r *StreamCredentials) NotifyTokensRevoked(userID string) {
	r.revoke(streamRevocationKey{"user-token", userID}, func(s *streamCredentialState) bool { return userID != "" && s.userID == userID && s.tokenID != "" })
}

func (r *StreamCredentials) NotifyAccountRevoked(userID string) {
	r.revoke(streamRevocationKey{"account", userID}, func(s *streamCredentialState) bool { return userID != "" && s.userID == userID })
}
