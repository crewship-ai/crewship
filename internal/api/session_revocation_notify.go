package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// SessionRevocationNotifier receives an immediate, in-process signal when
// a user session is revoked, so live transports (the WS hub's chat/event
// sockets, the terminal handler's container shells) can tear the
// corresponding connections down in milliseconds instead of waiting for
// their periodic 30s backstop sweep. *ws.Hub and *terminal.Handler both
// implement it.
type SessionRevocationNotifier interface {
	// NotifySessionRevoked is called after a single session (by
	// user_sessions ID) has been successfully revoked.
	NotifySessionRevoked(sessionID string)
	// NotifyUserRevoked is called after ALL of a user's sessions have
	// been revoked at once (password change, forced re-auth).
	NotifyUserRevoked(userID string)
}

// notifyingSessionStore decorates a sessions.Store so that every
// successful Revoke / RevokeAllForUser — wherever it is called from
// (logout, admin revoke, password change, refresh-token-reuse detection,
// forced Google re-auth, and any revoke site added later) — fans out to
// the registered notifiers. Decorating the store is the chokepoint: no
// handler has to remember to notify, and a future revoke call site gets
// the push behaviour for free.
//
// Notification is deliberately post-commit and best-effort: the DB row is
// already flipped when the notifiers run, so a notifier that finds no
// matching live connection is a no-op, and the transports' periodic
// sweeps remain as the backstop for anything that bypasses this store.
type notifyingSessionStore struct {
	sessions.Store
	notifiers []SessionRevocationNotifier
}

// newNotifyingSessionStore wraps inner so successful revocations notify
// every non-nil notifier. Returns inner unchanged when no notifiers are
// supplied.
func newNotifyingSessionStore(inner sessions.Store, notifiers ...SessionRevocationNotifier) sessions.Store {
	live := make([]SessionRevocationNotifier, 0, len(notifiers))
	for _, n := range notifiers {
		if n != nil {
			live = append(live, n)
		}
	}
	if len(live) == 0 {
		return inner
	}
	return &notifyingSessionStore{Store: inner, notifiers: live}
}

func (s *notifyingSessionStore) Revoke(ctx context.Context, id, reason string) error {
	err := s.Store.Revoke(ctx, id, reason)
	if err != nil {
		return err
	}
	for _, n := range s.notifiers {
		n.NotifySessionRevoked(id)
	}
	return nil
}

func (s *notifyingSessionStore) RevokeAllForUser(ctx context.Context, userID, reason string) (int64, error) {
	count, err := s.Store.RevokeAllForUser(ctx, userID, reason)
	if err != nil {
		return count, err
	}
	for _, n := range s.notifiers {
		n.NotifyUserRevoked(userID)
	}
	return count, nil
}
