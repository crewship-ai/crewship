package api

// Coverage tests for middleware.go: RequireAuth branches (CLI token
// scopes, sid-less JWT, nil/erroring sessions store, expired session,
// TouchLastUsed failure), reasonForJWTErr mapping, and the PR-F24
// bound-token DB assertions (crew / chat foreign-ID closure).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// fakeSessionsCovStore is a canned sessions.Store so the middleware
// tests can simulate store outcomes (transient error, expired session,
// touch failure) without racing a real DB.
type fakeSessionsCovStore struct {
	getSession *sessions.Session
	getErr     error
	touchErr   error
	touched    int
}

func (s *fakeSessionsCovStore) Create(context.Context, string, string, string, time.Duration) (*sessions.Session, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeSessionsCovStore) Get(context.Context, string) (*sessions.Session, error) {
	return s.getSession, s.getErr
}
func (s *fakeSessionsCovStore) ListActiveForUser(context.Context, string) ([]*sessions.Session, error) {
	return nil, nil
}
func (s *fakeSessionsCovStore) Revoke(context.Context, string, string) error { return nil }
func (s *fakeSessionsCovStore) RevokeAllForUser(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (s *fakeSessionsCovStore) TouchLastUsed(context.Context, string) error {
	s.touched++
	return s.touchErr
}
func (s *fakeSessionsCovStore) RotateRefreshJti(context.Context, string, string, string) error {
	return nil
}
func (s *fakeSessionsCovStore) SetClock(func() time.Time) {}

func newCovValidator(t *testing.T) *auth.JWTValidator {
	t.Helper()
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return v
}

func TestRequireAuth_CLITokenScopes_StashedInContext(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	v := newCovValidator(t)

	plaintext := "crewship_cli_scoped00112233445566778899"
	if _, err := db.Exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, scopes, created_at)
		VALUES ('clt-scoped', ?, 'scoped-cli', ?, '["agents:read","crews:read"]', datetime('now'))`,
		userID, sha256Hex(plaintext)); err != nil {
		t.Fatalf("seed scoped cli token: %v", err)
	}

	mw := NewAuthMiddleware(v, &fakeSessionsCovStore{}, db, newTestLogger())
	var gotScopes stringSet
	var gotUser *AuthUser
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScopes, _ = r.Context().Value(ctxTokenScopes).(stringSet)
		gotUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if gotUser == nil || gotUser.ID != userID {
		t.Errorf("user = %+v, want ID=%s", gotUser, userID)
	}
	if gotScopes == nil {
		t.Fatal("ctxTokenScopes missing — scoped CLI token must stash its scope set")
	}
	if _, ok := gotScopes["agents:read"]; !ok {
		t.Errorf("scopes = %v, want to contain agents:read", gotScopes)
	}
	if _, ok := gotScopes["crews:read"]; !ok {
		t.Errorf("scopes = %v, want to contain crews:read", gotScopes)
	}
}

func TestRequireAuth_NilSessionsStore_Returns500(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	v := newCovValidator(t)

	tok, err := v.IssueAccessToken(userID, "sess-x", "Test", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	mw := NewAuthMiddleware(v, nil, db, newTestLogger())
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("misconfigured middleware must not reach the handler")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// 500, not 401 — a config error must not force a frontend logout.
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestRequireAuth_SessionStoreTransientError_Returns500Not401(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	v := newCovValidator(t)

	tok, err := v.IssueAccessToken(userID, "sess-x", "Test", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	store := &fakeSessionsCovStore{getErr: errors.New("db timeout")}
	mw := NewAuthMiddleware(v, store, db, newTestLogger())
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run on store outage")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (transient store error must NOT 401)", rr.Code)
	}
}

func TestRequireAuth_ExpiredSession_401SessionExpired(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	v := newCovValidator(t)

	tok, err := v.IssueAccessToken(userID, "sess-x", "Test", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	// Session exists, is not revoked, but expired — reason must be
	// session_expired so the frontend attempts a refresh.
	store := &fakeSessionsCovStore{getSession: &sessions.Session{
		ID:        "sess-x",
		UserID:    userID,
		ExpiresAt: time.Now().Add(-time.Hour),
	}}
	mw := NewAuthMiddleware(v, store, db, newTestLogger())
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("expired session must not reach handler")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != `Bearer error="session_expired"` {
		t.Errorf("WWW-Authenticate = %q, want session_expired (RevokedAt is nil)", got)
	}
}

func TestRequireAuth_TouchLastUsedFailure_RequestStillSucceeds(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	v := newCovValidator(t)

	tok, err := v.IssueAccessToken(userID, "sess-x", "Test", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	store := &fakeSessionsCovStore{
		getSession: &sessions.Session{
			ID:        "sess-x",
			UserID:    userID,
			ExpiresAt: time.Now().Add(time.Hour),
		},
		touchErr: errors.New("sqlite busy"),
	}
	mw := NewAuthMiddleware(v, store, db, newTestLogger())
	var gotUser *AuthUser
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (touch failure must be best-effort)", rr.Code)
	}
	if store.touched != 1 {
		t.Errorf("TouchLastUsed called %d times, want 1", store.touched)
	}
	if gotUser == nil || gotUser.SessionID != "sess-x" {
		t.Errorf("user = %+v, want SessionID=sess-x", gotUser)
	}
}

func TestReasonForJWTErr_Mapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"expired", auth.ErrTokenExpired, reasonSessionExpired},
		{"invalid", auth.ErrInvalidToken, reasonSessionInvalid},
		{"wrong kind", auth.ErrWrongKind, reasonSessionInvalid},
		{"unknown", errors.New("something else"), reasonSessionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reasonForJWTErr(tt.err); got != tt.want {
				t.Errorf("reasonForJWTErr(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestAssertBoundCrewWorkspaceDB_EmptyIDSkippedAndDBError(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-bound", wsID, "Bound Crew", "bound-crew")

	t.Run("empty ids are optional fields and skipped", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
		rr := httptest.NewRecorder()
		if !assertBoundCrewWorkspaceDB(rr, req, db, newTestLogger(), "", crewID, "") {
			t.Fatalf("expected true for (empty, own-crew, empty); body=%s", rr.Body.String())
		}
	})

	t.Run("db error rejects with 403 and logs", func(t *testing.T) {
		brokenDB := setupTestDB(t)
		brokenDB.Close()
		req := httptest.NewRequest("POST", "/x", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
		rr := httptest.NewRecorder()
		if assertBoundCrewWorkspaceDB(rr, req, brokenDB, newTestLogger(), crewID) {
			t.Fatal("expected false on DB failure")
		}
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (fail closed)", rr.Code)
		}
	})
}

func TestAssertBoundChatWorkspaceDB_ForeignAndErrorPaths(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// A second workspace with its own agent + chat.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-foreign', 'Foreign', 'foreign')`); err != nil {
		t.Fatalf("seed foreign ws: %v", err)
	}
	seedAgentRow(t, db, "agent-foreign", "ws-foreign", "", "Foreign Agent", "foreign-agent", "AGENT")
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat-foreign', 'agent-foreign', 'ws-foreign')`); err != nil {
		t.Fatalf("seed foreign chat: %v", err)
	}

	// A chat in the bound workspace for the happy path.
	seedAgentRow(t, db, "agent-own", wsID, "", "Own Agent", "own-agent", "AGENT")
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat-own', 'agent-own', ?)`, wsID); err != nil {
		t.Fatalf("seed own chat: %v", err)
	}

	boundReq := func() *http.Request {
		req := httptest.NewRequest("POST", "/x", nil)
		return req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	}

	t.Run("own chat passes", func(t *testing.T) {
		rr := httptest.NewRecorder()
		if !assertBoundChatWorkspaceDB(rr, boundReq(), db, newTestLogger(), "chat-own") {
			t.Fatalf("expected true for own-workspace chat; body=%s", rr.Body.String())
		}
	})

	t.Run("foreign chat 403", func(t *testing.T) {
		rr := httptest.NewRecorder()
		if assertBoundChatWorkspaceDB(rr, boundReq(), db, newTestLogger(), "chat-foreign") {
			t.Fatal("expected false for foreign-workspace chat")
		}
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("unknown chat 403 (no existence oracle)", func(t *testing.T) {
		rr := httptest.NewRecorder()
		if assertBoundChatWorkspaceDB(rr, boundReq(), db, newTestLogger(), "chat-ghost") {
			t.Fatal("expected false for unknown chat id")
		}
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("db error 403 and logs", func(t *testing.T) {
		brokenDB := setupTestDB(t)
		brokenDB.Close()
		rr := httptest.NewRecorder()
		if assertBoundChatWorkspaceDB(rr, boundReq(), brokenDB, newTestLogger(), "chat-own") {
			t.Fatal("expected false on DB failure")
		}
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (fail closed)", rr.Code)
		}
	})
}
