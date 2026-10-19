package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestSignOutOldCookieDoesNotNotifyAgain(t *testing.T) {
	h, v, store, uid, sid := newSignOutRig(t)
	notifier := &recordingNotifier{}
	registry := &auth.StreamCredentials{}
	h.sessions = newNotifyingSessionStore(store, notifier, registry)
	cookie, err := v.IssueAccessToken(uid, sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	signout := func() {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/auth/signout", nil)
		req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: cookie})
		rr := httptest.NewRecorder()
		h.SignOut(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("signout status %d", rr.Code)
		}
	}
	signout()
	revoked, err := store.Get(t.Context(), sid)
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("initial revoke: %v %+v", err, revoked)
	}
	fresh, err := store.Create(t.Context(), uid, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Epoch()
	signout()
	if len(notifier.sessions) != 1 {
		t.Errorf("old-cookie signout notified %d times; want only the first state transition", len(notifier.sessions))
	}
	ctx, release := registry.Track(t.Context(), snapshot, uid, fresh.ID, "", fresh.ExpiresAt)
	defer release()
	if !auth.StreamCredentialCurrent(ctx) {
		t.Error("old-cookie signout invalidated a newer valid session admission")
	}
}

func TestNotifyingSessionStoreEmptyBulkRevokeDoesNotNotify(t *testing.T) {
	_, _, inner, uid, sid := newSignOutRig(t)
	notifier := &recordingNotifier{}
	store := newNotifyingSessionStore(inner, notifier)
	if err := store.Revoke(t.Context(), sid, sessions.ReasonLogout); err != nil {
		t.Fatal(err)
	}
	n, err := store.RevokeAllForUser(t.Context(), uid, sessions.ReasonAdminForce)
	if err != nil || n != 0 {
		t.Fatalf("empty bulk revoke: %d %v", n, err)
	}
	if len(notifier.users) != 0 {
		t.Fatal("empty bulk revoke notified a user epoch change")
	}
}

// Revoke after the real session read, before RequireAuth can register its lease.
// Both users and both session rows are genuine local database fixtures.
type epochRaceSessionStore struct {
	sessions.Store
	afterRead func()
}

func (s *epochRaceSessionStore) Get(ctx context.Context, id string) (*sessions.Session, error) {
	row, err := s.Store.Get(ctx, id)
	if err == nil && s.afterRead != nil {
		f := s.afterRead
		s.afterRead = nil
		f()
	}
	return row, err
}
func TestRequireAuthRevocationAdmissionAudience(t *testing.T) {
	for _, tc := range []struct {
		name            string
		own, restricted bool
	}{
		{"other-user", false, false}, {"own-credential", true, false},
		{"restricted-other-user", false, true}, {"restricted-own-credential", true, true},
	} {
		own := tc.own
		name := tc.name
		want := http.StatusNoContent
		if own {
			want = http.StatusServiceUnavailable
		}
		t.Run(name, func(t *testing.T) {
			h, v, inner, uid, sid := newSignOutRig(t)
			if tc.restricted {
				execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('epoch-ws','Epoch','epoch-ws')`)
				execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('epoch-member','epoch-ws',?,'VIEWER','restricted')`, uid)
			}
			execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('epoch-other','epoch-other@example.invalid','Other')`)
			other, err := inner.Create(t.Context(), "epoch-other", "", "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			readStore := &epochRaceSessionStore{Store: inner}
			m := NewAuthMiddleware(v, readStore, h.db, newTestLogger())
			revoker := newNotifyingSessionStore(inner, m.streams)
			target := other.ID
			if own {
				target = sid
			}
			readStore.afterRead = func() {
				if err := revoker.Revoke(t.Context(), target, sessions.ReasonLogout); err != nil {
					t.Fatal(err)
				}
			}
			token, err := v.IssueAccessToken(uid, sid, "", "")
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			handler := m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if !auth.StreamCredentialCurrent(r.Context()) {
					t.Error("handler received invalid lease")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest("GET", "/api/v1/auth/sessions", nil)
			req.Pattern = "GET /api/v1/auth/sessions"
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != want || reached == own {
				t.Fatalf("HTTP%d reached=%v; want HTTP%d reached=%v", rr.Code, reached, want, !own)
			}
		})
	}
}
