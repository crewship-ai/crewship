package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// recordingNotifier captures which notifications fired.
type recordingNotifier struct {
	mu       sync.Mutex
	sessions []string
	users    []string
}

func (n *recordingNotifier) NotifySessionRevoked(sid string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sessions = append(n.sessions, sid)
}

func (n *recordingNotifier) NotifyUserRevoked(userID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.users = append(n.users, userID)
}

// revokeStubStore scripts Revoke / RevokeAllForUser outcomes; everything
// else is a no-op.
type revokeStubStore struct {
	revokeErr    error
	revokeAllErr error
}

func (s *revokeStubStore) Create(context.Context, string, string, string, time.Duration) (*sessions.Session, error) {
	return nil, errors.New("not implemented")
}
func (s *revokeStubStore) Get(context.Context, string) (*sessions.Session, error) {
	return nil, sessions.ErrNotFound
}
func (s *revokeStubStore) ListActiveForUser(context.Context, string) ([]*sessions.Session, error) {
	return nil, nil
}
func (s *revokeStubStore) Revoke(context.Context, string, string) error { return s.revokeErr }
func (s *revokeStubStore) RevokeAllForUser(context.Context, string, string) (int64, error) {
	if s.revokeAllErr != nil {
		return 0, s.revokeAllErr
	}
	return 2, nil
}
func (s *revokeStubStore) TouchLastUsed(context.Context, string) error                    { return nil }
func (s *revokeStubStore) RotateRefreshJti(context.Context, string, string, string) error { return nil }
func (s *revokeStubStore) SetClock(func() time.Time)                                      {}

func TestNotifyingSessionStore_RevokeNotifiesOnSuccess(t *testing.T) {
	t.Parallel()
	n1, n2 := &recordingNotifier{}, &recordingNotifier{}
	store := newNotifyingSessionStore(&revokeStubStore{}, n1, nil, n2) // nil entries ignored

	if err := store.Revoke(context.Background(), "sid-1", sessions.ReasonLogout); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	for i, n := range []*recordingNotifier{n1, n2} {
		if len(n.sessions) != 1 || n.sessions[0] != "sid-1" {
			t.Errorf("notifier %d: sessions = %v, want [sid-1]", i, n.sessions)
		}
		if len(n.users) != 0 {
			t.Errorf("notifier %d: users = %v, want none for a single-session revoke", i, n.users)
		}
	}
}

func TestNotifyingSessionStore_RevokeAllNotifiesUser(t *testing.T) {
	t.Parallel()
	n := &recordingNotifier{}
	store := newNotifyingSessionStore(&revokeStubStore{}, n)

	count, err := store.RevokeAllForUser(context.Background(), "u-1", sessions.ReasonPasswordChange)
	if err != nil || count != 2 {
		t.Fatalf("RevokeAllForUser = (%d, %v), want (2, nil)", count, err)
	}
	if len(n.users) != 1 || n.users[0] != "u-1" {
		t.Errorf("users = %v, want [u-1]", n.users)
	}
	if len(n.sessions) != 0 {
		t.Errorf("sessions = %v, want none for a bulk revoke", n.sessions)
	}
}

func TestNotifyingSessionStore_NoNotifyOnFailure(t *testing.T) {
	t.Parallel()
	n := &recordingNotifier{}
	store := newNotifyingSessionStore(
		&revokeStubStore{revokeErr: errors.New("db down"), revokeAllErr: errors.New("db down")}, n)

	if err := store.Revoke(context.Background(), "sid-1", sessions.ReasonLogout); err == nil {
		t.Fatal("Revoke: expected error")
	}
	if _, err := store.RevokeAllForUser(context.Background(), "u-1", sessions.ReasonPasswordChange); err == nil {
		t.Fatal("RevokeAllForUser: expected error")
	}
	if len(n.sessions) != 0 || len(n.users) != 0 {
		t.Errorf("failed revokes must not notify; got sessions=%v users=%v", n.sessions, n.users)
	}
}

func TestNotifyingSessionStore_NoNotifiersReturnsInner(t *testing.T) {
	t.Parallel()
	inner := &revokeStubStore{}
	if got := newNotifyingSessionStore(inner); got != sessions.Store(inner) {
		t.Error("with no notifiers the inner store must be returned undecorated")
	}
	if got := newNotifyingSessionStore(inner, nil); got != sessions.Store(inner) {
		t.Error("nil-only notifier list must also return the inner store undecorated")
	}
}
