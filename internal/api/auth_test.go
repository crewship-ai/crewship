package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
)

func newTestJWTValidator(t *testing.T) *auth.JWTValidator {
	t.Helper()
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!", "")
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
	h := NewAuthHandler(db, logger, v, false)

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
	h := NewAuthHandler(db, logger, v, true)

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
	h := NewAuthHandler(db, logger, v, true)

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
	h := NewAuthHandler(db, logger, v, true)

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
	h := NewAuthHandler(db, logger, v, false)

	body := bytes.NewBufferString(`{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tok, _ := resp["cli_token"].(string)
	if !strings.HasPrefix(tok, "crewship_cli_") {
		t.Errorf("cli_token = %q, want crewship_cli_*", tok)
	}
}

func TestAuthBootstrap_AlreadyInitialized(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	seedTestUser(t, db) // user exists already
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, false)

	body := bytes.NewBufferString(`{"full_name":"Admin","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestAuthBootstrap_Validation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, logger, v, false)

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
			req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			h.Bootstrap(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status = %d, want %d", rr.Code, tt.want)
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
	h := NewAuthHandler(db, logger, v, true)

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
	h := NewAuthHandler(db, logger, v, true)

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
	// Validate generated JWT
	claims, err := v.Validate(resp["token"])
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
	h := NewAuthHandler(db, logger, v, true)

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
	h := NewAuthHandler(db, logger, v, true)

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
