package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func newSessionsRig(t *testing.T) (*SessionsHandler, sessions.Store, string, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	uid := seedTestUser(t, db)

	// Two sessions, deterministically older + newer. RFC3339 has only
	// second precision so we step the store's clock by a second
	// between the two Creates rather than relying on wall time —
	// otherwise the test is flaky on fast hardware.
	base := time.Now().UTC().Truncate(time.Second)
	store.SetClock(func() time.Time { return base })
	_, _ = store.Create(context.Background(), uid, "iOS", "1.1.1.1", auth.RefreshTokenTTL)
	store.SetClock(func() time.Time { return base.Add(2 * time.Second) })
	newer, _ := store.Create(context.Background(), uid, "Web", "2.2.2.2", auth.RefreshTokenTTL)
	store.SetClock(time.Now) // restore for handler use

	h := NewSessionsHandler(db, logger, store)
	return h, store, uid, newer.ID
}

// requestWithUser injects a user-context AuthUser equivalent to what
// RequireAuth would set, so handler tests can run without going through
// the middleware chain.
func requestWithUser(method, target, userID, sessionID string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	user := &AuthUser{ID: userID, Email: "u@e.com", Name: "User", SessionID: sessionID}
	ctx := context.WithValue(req.Context(), ctxUser, user)
	return req.WithContext(ctx)
}

func TestSessionsList_HappyPath(t *testing.T) {
	h, _, uid, currentSid := newSessionsRig(t)

	req := requestWithUser("GET", "/api/v1/auth/sessions", uid, currentSid)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(rows))
	}

	// Newest first (last_used_at DESC), so the current session
	// should be the head row.
	if rows[0]["id"] != currentSid {
		t.Errorf("expected current session first; got %v", rows[0]["id"])
	}
	if rows[0]["is_current"] != true {
		t.Errorf("is_current should be true on caller's row")
	}
	if rows[1]["is_current"] != false {
		t.Errorf("is_current should be false on the other row")
	}
}

func TestSessionsList_OnlyOwnSessions(t *testing.T) {
	h, store, uid, currentSid := newSessionsRig(t)

	// Add a session belonging to a DIFFERENT user.
	_, err := store.Create(context.Background(), "other-user-id", "evil", "9.9.9.9", auth.RefreshTokenTTL)
	if err != nil {
		// other-user-id has no users row → FK fails. Insert one.
		// (the seedTestUser helper hardcodes test-user-id; we need a
		// second user for this case.)
		_, err2 := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@x.com', 'Other')`, "other-user-id")
		if err2 != nil {
			t.Fatalf("insert other user: %v", err2)
		}
		_, err = store.Create(context.Background(), "other-user-id", "evil", "9.9.9.9", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatalf("create other session: %v", err)
		}
	}

	req := requestWithUser("GET", "/api/v1/auth/sessions", uid, currentSid)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var rows []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &rows)
	for _, r := range rows {
		// Verify no foreign session leaked. Since List doesn't echo
		// user_id, we check that none of the returned UA strings is
		// the foreign one.
		if r["user_agent"] == "evil" {
			t.Errorf("foreign session leaked into list: %v", r)
		}
	}
}

func TestSessionsList_ExcludesRevoked(t *testing.T) {
	h, store, uid, currentSid := newSessionsRig(t)

	// Revoke the older session.
	all, _ := store.ListActiveForUser(context.Background(), uid)
	if len(all) != 2 {
		t.Fatalf("expected 2 active to start, got %d", len(all))
	}
	for _, s := range all {
		if s.ID != currentSid {
			_ = store.Revoke(context.Background(), s.ID, sessions.ReasonAdminForce)
		}
	}

	req := requestWithUser("GET", "/api/v1/auth/sessions", uid, currentSid)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var rows []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &rows)
	if len(rows) != 1 {
		t.Errorf("expected 1 active after revoke, got %d", len(rows))
	}
}

func TestSessionsList_RequiresAuth(t *testing.T) {
	h, _, _, _ := newSessionsRig(t)
	// No user in context.
	req := httptest.NewRequest("GET", "/api/v1/auth/sessions", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestSessionsRevoke_OwnSession(t *testing.T) {
	h, store, uid, currentSid := newSessionsRig(t)

	req := requestWithUser("POST", "/api/v1/auth/sessions/"+currentSid+"/revoke", uid, currentSid)
	req.SetPathValue("id", currentSid)
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["is_current"] != true {
		t.Error("is_current should be true when revoking own session")
	}

	sess, _ := store.Get(context.Background(), currentSid)
	if sess.RevokedAt == nil {
		t.Error("session should be revoked")
	}
}

func TestSessionsRevoke_AnotherUsersReturns404(t *testing.T) {
	h, store, _, _ := newSessionsRig(t)

	// Create a session for a different user.
	_, _ = h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'mallory@x.com', 'Mallory')`, "mallory-id")
	other, err := store.Create(context.Background(), "mallory-id", "", "", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create mallory's session: %v", err)
	}

	req := requestWithUser("POST", "/api/v1/auth/sessions/"+other.ID+"/revoke", "test-user-id", "irrelevant")
	req.SetPathValue("id", other.ID)
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("revoking other user's session: status = %d, want 404 (not 403 — id enumeration defense)", rr.Code)
	}

	// Mallory's session must still be active. The 404 is a smoke
	// screen, not actual authorization.
	got, _ := store.Get(context.Background(), other.ID)
	if got.RevokedAt != nil {
		t.Error("foreign session was actually revoked — RBAC bypass")
	}
}

func TestSessionsRevoke_UnknownSessionReturns404(t *testing.T) {
	h, _, uid, currentSid := newSessionsRig(t)

	req := requestWithUser("POST", "/api/v1/auth/sessions/s_nope/revoke", uid, currentSid)
	req.SetPathValue("id", "s_nope")
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestSessionsRevoke_BlankIdReturns400(t *testing.T) {
	h, _, uid, currentSid := newSessionsRig(t)
	req := requestWithUser("POST", "/api/v1/auth/sessions//revoke", uid, currentSid)
	// PathValue empty — handler short-circuits.
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSessionsRevoke_RequiresAuth(t *testing.T) {
	h, _, _, currentSid := newSessionsRig(t)

	req := httptest.NewRequest("POST", "/api/v1/auth/sessions/"+currentSid+"/revoke", nil)
	req.SetPathValue("id", currentSid)
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}
