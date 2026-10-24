package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// A stale authoritative-store adapter must not let the earlier stream-epoch
// check replace the current SQL original-source snapshot.
type originalSourceCachedSessionStore struct {
	sessions.Store
	row *sessions.Session
}

func (s *originalSourceCachedSessionStore) Get(context.Context, string) (*sessions.Session, error) {
	v := *s.row
	return &v, nil
}

func TestRequireAuthOriginalSourceRemainsCurrentAfterEpochCheck(t *testing.T) {
	h, v, inner, user, sid := newSignOutRig(t)
	row, e := inner.Get(t.Context(), sid)
	if e != nil {
		t.Fatal(e)
	}
	cached := &originalSourceCachedSessionStore{Store: inner, row: row}
	// Do not notify the in-memory registry: only the exact SQL source check can
	// reject this deliberately stale adapter's otherwise-active session snapshot.
	execOrFatal(t, h.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id=?`, sid)
	token, e := v.IssueAccessToken(user, sid, "", "")
	if e != nil {
		t.Fatal(e)
	}
	m := NewAuthMiddleware(v, cached, h.db, newTestLogger())
	reached := false
	handler := m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest("GET", "/api/v1/auth/sessions", nil)
	req.Pattern = "GET /api/v1/auth/sessions"
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || reached {
		t.Fatalf("stale SQL source accepted: HTTP%d handler=%v", rr.Code, reached)
	}
}
