package api

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestExtractToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
		cookie *http.Cookie
		want   string
	}{
		{"bearer", "Bearer abc.def.ghi", nil, "abc.def.ghi"},
		{"empty", "", nil, ""},
		{"non-bearer scheme", "Basic foo", nil, ""},
		{"cookie session", "", &http.Cookie{Name: "authjs.session-token", Value: "cookie-jwe"}, "cookie-jwe"},
		{"cookie secure session", "", &http.Cookie{Name: "__Secure-authjs.session-token", Value: "secure-jwe"}, "secure-jwe"},
		{"unrelated cookie", "", &http.Cookie{Name: "other", Value: "val"}, ""},
		{"bearer wins over cookie", "Bearer header-tok", &http.Cookie{Name: "authjs.session-token", Value: "cookie-tok"}, "header-tok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			got := extractToken(req)
			if got != tt.want {
				t.Errorf("extractToken = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequireAuth_NoToken(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	called := false
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
	if called {
		t.Error("next handler should not have been called")
	}
}

func TestRequireAuth_BadJWT(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestRequireAuth_ValidJWT(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	store := sessions.NewDBStore(db)

	// Mint a real session row so the middleware's revoke-check passes.
	sess, err := store.Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test", "test@example.com")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	mw := NewAuthMiddleware(v, store, db, logger)
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
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotUser == nil || gotUser.ID != userID {
		t.Errorf("user = %+v, want ID=%s", gotUser, userID)
	}
}

func TestRequireAuth_CLIToken(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	// Insert a CLI token whose hash matches a known plaintext
	plaintext := "crewship_cli_aabbccdd11223344556677889900"
	hash := sha256Hex(plaintext)
	if _, err := db.Exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, datetime('now'))`,
		"clt1", userID, "test-cli", hash); err != nil {
		t.Fatalf("seed cli token: %v", err)
	}

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	var gotUser *AuthUser
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	if gotUser == nil || gotUser.ID != userID {
		t.Errorf("got user = %+v, want ID=%s", gotUser, userID)
	}
}

func TestRequireAuth_BadCLIToken(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer crewship_cli_does-not-exist-in-db")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestRequireWorkspace_NoUser(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	handler := mw.RequireWorkspace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))
	req := httptest.NewRequest("GET", "/?workspace_id=ws", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestRequireWorkspace_MissingID(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	handler := mw.RequireWorkspace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestRequireWorkspace_NotMember(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	// Insert workspace WITHOUT membership
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-orphan', 'Other', 'other')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	handler := mw.RequireWorkspace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))
	req := httptest.NewRequest("GET", "/?workspace_id=ws-orphan", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestRequireWorkspace_OK(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")

	mw := NewAuthMiddleware(v, sessions.NewDBStore(db), db, logger)
	var gotRole, gotWS string
	handler := mw.RequireWorkspace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRole = RoleFromContext(r.Context())
		gotWS = WorkspaceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if gotRole != "OWNER" || gotWS != wsID {
		t.Errorf("got role=%s ws=%s, want OWNER ws=%s", gotRole, gotWS, wsID)
	}
}

func TestInternalWsCtx(t *testing.T) {
	t.Parallel()
	called := false
	var gotWS string
	wrapped := internalWsCtx(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotWS = WorkspaceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// Missing workspace_id => 400
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if called {
		t.Error("next must not be called")
	}

	// Provided workspace_id => OK
	req = httptest.NewRequest("GET", "/?workspace_id=ws-x", nil)
	rr = httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if gotWS != "ws-x" {
		t.Errorf("workspace_id = %q, want ws-x", gotWS)
	}
}

func TestUserFromContext_Empty(t *testing.T) {
	t.Parallel()
	if u := UserFromContext(httptest.NewRequest("GET", "/", nil).Context()); u != nil {
		t.Errorf("want nil, got %+v", u)
	}
	if got := WorkspaceIDFromContext(httptest.NewRequest("GET", "/", nil).Context()); got != "" {
		t.Errorf("want empty, got %q", got)
	}
	if got := RoleFromContext(httptest.NewRequest("GET", "/", nil).Context()); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}
