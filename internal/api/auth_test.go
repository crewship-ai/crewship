package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// peekSetupTokenForTest exposes the in-memory setup token to tests in this
// package. Production code never reads the token back out — it's logged
// once and consumed by the bootstrap handler. Tests need the value to
// echo back via the X-Setup-Token header.
func (h *AuthHandler) peekSetupTokenForTest() string {
	h.setupTokenMu.Lock()
	defer h.setupTokenMu.Unlock()
	return h.setupToken
}

func newTestJWTValidator(t *testing.T) *auth.JWTValidator {
	t.Helper()
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	return v
}

// ---- Signup ----

func TestAuthSignup_DisabledByDefault(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)

	body := bytes.NewBufferString(`{"full_name":"User","email":"u@e.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	rr := httptest.NewRecorder()
	h.Signup(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestAuthSignup_Validation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"invalid json", `not-json`, http.StatusBadRequest},
		{"name too short", `{"full_name":"X","email":"a@b.com","password":"longenough"}`, http.StatusBadRequest},
		{"bad email", `{"full_name":"Alice","email":"not-email","password":"longenough"}`, http.StatusBadRequest},
		{"short password", `{"full_name":"Alice","email":"a@b.com","password":"short"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/auth/signup", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			h.Signup(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}

func TestAuthSignup_Success(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	body := bytes.NewBufferString(`{"full_name":"Alice Doe","email":"alice@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	rr := httptest.NewRecorder()
	h.Signup(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	// Verify user was created with workspace + membership
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE email = ?", "alice@example.com").Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("users = %d, want 1", count)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM workspaces").Scan(&count); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if count != 1 {
		t.Errorf("workspaces = %d, want 1", count)
	}

	// Verify session cookie set
	gotCookie := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" || c.Name == "__Secure-authjs.session-token" {
			gotCookie = true
			if c.Value == "" {
				t.Error("session cookie value empty")
			}
		}
	}
	if !gotCookie {
		t.Error("expected session cookie to be set")
	}
}

func TestAuthSignup_DuplicateEmail(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	// Insert existing user
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u1', 'taken@example.com', 'Existing')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	body := bytes.NewBufferString(`{"full_name":"New","email":"taken@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	rr := httptest.NewRecorder()
	h.Signup(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

// ---- Bootstrap ----

func TestAuthBootstrap_Success(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)

	// Arm the setup token (post-Patch-C) so the Bootstrap gate accepts
	// the request. In production this happens at server startup against
	// an empty users table; here we drive it explicitly. Grab the token
	// out via the test-only accessor before it's consumed.
	if err := h.MaybeGenerateSetupToken(context.Background()); err != nil {
		t.Fatalf("arm setup token: %v", err)
	}
	tok := h.peekSetupTokenForTest()
	if tok == "" {
		t.Fatalf("expected setup token to be armed on empty DB")
	}

	body := bytes.NewBufferString(`{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	req.Header.Set("X-Setup-Token", tok)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cliTok, _ := resp["cli_token"].(string)
	if !strings.HasPrefix(cliTok, "crewship_cli_") {
		t.Errorf("cli_token = %q, want crewship_cli_*", cliTok)
	}

	// Bootstrap sets session cookies inline (since 2026-05-13) so a
	// fresh-install admin lands on /onboarding authenticated. Verify
	// both the access and refresh cookies came back — without them
	// the frontend would have to chain /api/auth/callback/credentials
	// (which used to race the auth-tier rate limiter and 403).
	cookies := rr.Result().Cookies()
	var hasAccess, hasRefresh bool
	for _, c := range cookies {
		// Accept either prefix — under HTTPS / behind a TLS-terminating
		// proxy the cookie names get the `__Secure-` prefix, so a test
		// that only checked the plain names would silently miss the
		// secure-path behaviour.
		if c.Name == "authjs.session-token" || c.Name == "__Secure-authjs.session-token" {
			hasAccess = true
		}
		if c.Name == "authjs.refresh-token" || c.Name == "__Secure-authjs.refresh-token" {
			hasRefresh = true
		}
	}
	if !hasAccess {
		t.Errorf("missing access cookie 'authjs.session-token' (or __Secure- variant) after bootstrap")
	}
	if !hasRefresh {
		t.Errorf("missing refresh cookie 'authjs.refresh-token' (or __Secure- variant) after bootstrap")
	}
}

// TestAuthBootstrap_LeavesOnboardingPending guards the 2026-05-13
// behaviour change: the bootstrap handler used to set
// onboarding_completed=1 because bootstrap WAS the entire onboarding.
// With the split-screen wizard now responsible for picking a crew
// template + adapter, the flag must stay 0 — otherwise the dashboard
// gate sees "done" and skips the wizard the user just sent themselves
// into. Caught a regression that landed an admin straight on the
// dashboard with zero crews provisioned.
func TestAuthBootstrap_LeavesOnboardingPending(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)

	if err := h.MaybeGenerateSetupToken(context.Background()); err != nil {
		t.Fatalf("arm: %v", err)
	}
	tok := h.peekSetupTokenForTest()
	body := bytes.NewBufferString(`{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	req.Header.Set("X-Setup-Token", tok)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("bootstrap status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// onboarding_completed must be 0 — onboarding wizard runs next.
	var completed int
	if err := db.QueryRow(`SELECT onboarding_completed FROM users WHERE email='admin@example.com'`).Scan(&completed); err != nil {
		t.Fatalf("query: %v", err)
	}
	if completed != 0 {
		t.Fatalf("onboarding_completed = %d after bootstrap, want 0 (wizard must still run)", completed)
	}
}

func TestAuthBootstrap_AlreadyInitialized(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	seedTestUser(t, db) // user exists already
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)

	// Bootstrap on a non-empty DB: no token is armed (MaybeGenerateSetupToken
	// noops when count > 0), so the gate refuses without ever reaching the
	// in-tx COUNT check. Same 403 from the caller's POV.
	if err := h.MaybeGenerateSetupToken(context.Background()); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if h.peekSetupTokenForTest() != "" {
		t.Fatalf("setup token should NOT be armed when users already exist")
	}

	body := bytes.NewBufferString(`{"full_name":"Admin","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// TestAuthBootstrap_SetupTokenRequired pins the Patch C contract: even when
// the database is empty, a Bootstrap call with no X-Setup-Token header gets
// 403. This is the deploy-race defense — the operator who started the
// binary has the token from stderr; a LAN scanner does not.
func TestAuthBootstrap_SetupTokenRequired(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)

	if err := h.MaybeGenerateSetupToken(context.Background()); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if h.peekSetupTokenForTest() == "" {
		t.Fatalf("token must be armed on empty DB")
	}

	t.Run("no_header_refused", func(t *testing.T) {
		body := bytes.NewBufferString(`{"full_name":"Admin","email":"a@b.com","password":"longenough"}`)
		req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
		rr := httptest.NewRecorder()
		h.Bootstrap(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("no token → status=%d, want 403", rr.Code)
		}
	})

	t.Run("wrong_token_refused", func(t *testing.T) {
		body := bytes.NewBufferString(`{"full_name":"Admin","email":"a@b.com","password":"longenough"}`)
		req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
		req.Header.Set("X-Setup-Token", "not-the-real-token")
		rr := httptest.NewRecorder()
		h.Bootstrap(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("wrong token → status=%d, want 403", rr.Code)
		}
		// After wrong-token failure, the real token must still be armed
		// (no mutation on mismatch).
		if h.peekSetupTokenForTest() == "" {
			t.Errorf("real token must remain armed after wrong-token attempt")
		}
	})
}

// TestAuthBootstrap_SetupTokenIsOneShot — a successful bootstrap consumes
// the token; a second call with the same token must be refused even if the
// users table is somehow rolled back (paranoia: defence vs. accidental
// re-arm via process restart against a half-rolled-back DB).
func TestAuthBootstrap_SetupTokenIsOneShot(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)

	if err := h.MaybeGenerateSetupToken(context.Background()); err != nil {
		t.Fatalf("arm: %v", err)
	}
	tok := h.peekSetupTokenForTest()

	// First call with the right token: success.
	body1 := bytes.NewBufferString(`{"full_name":"Admin","email":"a@b.com","password":"longenough"}`)
	req1 := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body1)
	req1.Header.Set("X-Setup-Token", tok)
	rr1 := httptest.NewRecorder()
	h.Bootstrap(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first call status = %d, want 201, body=%s", rr1.Code, rr1.Body.String())
	}
	// Token must be cleared in memory.
	if h.peekSetupTokenForTest() != "" {
		t.Errorf("setup token must be cleared after successful bootstrap")
	}

	// Second call with the same token (now consumed): 403, regardless of
	// the fact that the user already exists.
	body2 := bytes.NewBufferString(`{"full_name":"Admin2","email":"a2@b.com","password":"longenough"}`)
	req2 := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body2)
	req2.Header.Set("X-Setup-Token", tok)
	rr2 := httptest.NewRecorder()
	h.Bootstrap(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Errorf("replay status = %d, want 403", rr2.Code)
	}
}

func TestAuthBootstrap_Validation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"invalid json", `garbage`, http.StatusBadRequest},
		{"name short", `{"full_name":"A","email":"a@b.com","password":"longenough"}`, http.StatusBadRequest},
		{"bad email", `{"full_name":"Admin","email":"x","password":"longenough"}`, http.StatusBadRequest},
		{"short pw", `{"full_name":"Admin","email":"a@b.com","password":"abc"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each subtest gets its own handler so the one-shot token isn't
			// consumed across iterations (it's intentionally consumed only
			// on success; validation errors leave it armed, but we don't
			// want to share token state between cases either way).
			h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), false)
			if err := h.MaybeGenerateSetupToken(context.Background()); err != nil {
				t.Fatalf("arm: %v", err)
			}
			req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", strings.NewReader(tt.body))
			req.Header.Set("X-Setup-Token", h.peekSetupTokenForTest())
			rr := httptest.NewRecorder()
			h.Bootstrap(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status = %d, want %d, body=%s", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}

// ---- WsToken ----

func TestAuthWsToken_Unauthorized(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	req := httptest.NewRequest("POST", "/api/v1/auth/ws-token", nil)
	rr := httptest.NewRecorder()
	h.WsToken(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestAuthWsToken_BrowserSession(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	req := httptest.NewRequest("POST", "/api/v1/auth/ws-token", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com", Name: "Test"}))
	rr := httptest.NewRecorder()
	h.WsToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["token"] == "" {
		t.Error("token should be set")
	}
	// Validate generated WS ticket (kind=ws)
	claims, err := v.ValidateWS(resp["token"])
	if err != nil {
		t.Fatalf("validate WS token: %v", err)
	}
	if claims.ID != userID {
		t.Errorf("token sub = %q, want %q", claims.ID, userID)
	}
}

func TestAuthWsToken_CLIToken(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	req := httptest.NewRequest("POST", "/api/v1/auth/ws-token", nil)
	req.Header.Set("Authorization", "Bearer crewship_cli_someplaintext")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "x@y.com", Name: "n"}))
	rr := httptest.NewRecorder()
	h.WsToken(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}

// ---- setSessionCookie (via Signup) ----

func TestAuthSignup_SecureCookieOnHTTPS(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, sessions.NewDBStore(db), true)

	body := bytes.NewBufferString(`{"full_name":"Bob","email":"bob@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.Signup(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	gotSecure := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "__Secure-authjs.session-token" {
			gotSecure = true
			if !c.Secure {
				t.Error("secure cookie should have Secure flag")
			}
		}
	}
	if !gotSecure {
		t.Error("expected __Secure-authjs.session-token under HTTPS")
	}
}
