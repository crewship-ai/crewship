package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// StreamCredentials tracks only active streams. It is not an authentication
// store: callers must validate the credential in the database between Epoch
// and Track. No historical revoked identities or cached allow decisions remain.
// The zero value is ready to use.
type StreamCredentials struct {
	mu     sync.Mutex
	epoch  uint64
	active map[*streamCredentialState]struct{}
}

type streamCredentialKey struct{}

type streamCredentialState struct {
	userID, sessionID, tokenID string
	expiresAt                  time.Time
	valid                      atomic.Bool
	cancel                     context.CancelFunc
	timer                      *time.Timer
	stopContext                func() bool
}

// Epoch must be captured before the caller's database validation. Any intervening
// notification conservatively refuses registration, even for another identity.
func (r *StreamCredentials) Epoch() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epoch
}

// Track binds a stream to exactly one validated session or token row ID. These
// are identifiers, never credential values. A zero expiresAt permits a credential
// with no deadline; parent cancellation and revocation still apply. The caller
// must defer the returned cleanup. Rejected admission returns a canceled tracked
// context, so it cannot become an untracked legacy allow.
func (r *StreamCredentials) Track(ctx context.Context, expectedEpoch uint64, userID, sessionID, tokenID string, expiresAt time.Time) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
		return rejectedStreamCredential(ctx)
	}
	r.mu.Lock()
	if expectedEpoch != r.epoch || ctx.Err() != nil || userID == "" ||
		(sessionID == "") == (tokenID == "") || (!expiresAt.IsZero() && !time.Now().Before(expiresAt)) {
		r.mu.Unlock()
		return rejectedStreamCredential(ctx)
	}
	child, cancel := context.WithCancel(ctx)
	s := &streamCredentialState{userID: userID, sessionID: sessionID, tokenID: tokenID, expiresAt: expiresAt, cancel: cancel}
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
	return s != nil && s.valid.Load() && ctx.Err() == nil && (s.expiresAt.IsZero() || time.Now().Before(s.expiresAt))
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

func (r *StreamCredentials) revoke(matches func(*streamCredentialState) bool) {
	r.mu.Lock()
	r.epoch++
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
	r.revoke(func(s *streamCredentialState) bool { return sessionID != "" && s.sessionID == sessionID })
}

// NotifyUserRevoked matches the session-store revoke-all contract: CLI tokens
// remain separate credentials. NotifyAccountRevoked covers both kinds.
func (r *StreamCredentials) NotifyUserRevoked(userID string) {
	r.revoke(func(s *streamCredentialState) bool { return userID != "" && s.userID == userID && s.sessionID != "" })
}

func (r *StreamCredentials) NotifyTokenRevoked(tokenID string) {
	r.revoke(func(s *streamCredentialState) bool { return tokenID != "" && s.tokenID == tokenID })
}

// NotifyTokensRevoked covers a user's CLI tokens without revoking browser
// sessions, including a password change's explicitly retained session.
func (r *StreamCredentials) NotifyTokensRevoked(userID string) {
	r.revoke(func(s *streamCredentialState) bool { return userID != "" && s.userID == userID && s.tokenID != "" })
}

func (r *StreamCredentials) NotifyAccountRevoked(userID string) {
	r.revoke(func(s *streamCredentialState) bool { return userID != "" && s.userID == userID })
}
