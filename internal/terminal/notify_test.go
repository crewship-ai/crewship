package terminal

import (
	"context"
	"io"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

// The push half of terminal revocation: the API layer's notifying session
// store calls NotifySessionRevoked / NotifyUserRevoked the moment a
// revoke commits, and any live shell backed by that session must be
// cancelled immediately — no waiting for the 30s watchSessionRevocation
// backstop poll.

// trackedSession registers a synthetic live session on the handler and
// returns its ctx so the test can observe the cancel.
func trackedSession(h *Handler, id, userID, authSid string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	h.sessions.Store(id, &Session{
		id:      id,
		userID:  userID,
		authSid: authSid,
		cancel:  cancel,
	})
	return ctx
}

func newNotifyTestHandler() *Handler {
	return New(nil, nil, nil, logging.New("error", "json", io.Discard), nil)
}

func TestNotifySessionRevoked_CancelsMatchingShells(t *testing.T) {
	t.Parallel()
	h := newNotifyTestHandler()

	revoked1 := trackedSession(h, "term-1", "u1", "sid-revoked")
	revoked2 := trackedSession(h, "term-2", "u1", "sid-revoked")
	otherSid := trackedSession(h, "term-3", "u1", "sid-other")
	cliShell := trackedSession(h, "term-4", "u1", "")

	h.NotifySessionRevoked("sid-revoked")

	if revoked1.Err() == nil || revoked2.Err() == nil {
		t.Error("shells under the revoked session must be cancelled immediately")
	}
	if otherSid.Err() != nil {
		t.Error("shell under a different session must survive")
	}
	if cliShell.Err() != nil {
		t.Error("CLI shell (no sid) must survive a session revoke")
	}
}

func TestNotifyUserRevoked_CancelsBrowserShellsOnly(t *testing.T) {
	t.Parallel()
	h := newNotifyTestHandler()

	browser := trackedSession(h, "term-1", "u1", "sid-a")
	cliShell := trackedSession(h, "term-2", "u1", "")
	otherUser := trackedSession(h, "term-3", "u2", "sid-b")

	h.NotifyUserRevoked("u1")

	if browser.Err() == nil {
		t.Error("browser shell must be cancelled on user-wide revoke")
	}
	if cliShell.Err() != nil {
		t.Error("CLI shell (auth artifact is the CLI token) must survive RevokeAllForUser")
	}
	if otherUser.Err() != nil {
		t.Error("another user's shell must survive")
	}
}

func TestNotify_NilAndEmptyAreSafe(t *testing.T) {
	t.Parallel()
	var nilH *Handler
	nilH.NotifySessionRevoked("sid") // must not panic
	nilH.NotifyUserRevoked("u1")

	h := newNotifyTestHandler()
	h.NotifySessionRevoked("") // empty — no-op
	h.NotifyUserRevoked("")
}
