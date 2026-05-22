package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func newNextAuthHandler(t *testing.T) (*NextAuthHandler, *auth.JWTValidator) {
	t.Helper()
	h, v, _ := newNextAuthHandlerWithStore(t)
	return h, v
}

func newNextAuthHandlerWithStore(t *testing.T) (*NextAuthHandler, *auth.JWTValidator, sessions.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	return NewNextAuthHandler(db, logger, v, store), v, store
}

func TestNextAuth_CSRF(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)

	req := httptest.NewRequest("GET", "/api/auth/csrf", nil)
	rr := httptest.NewRecorder()
	h.CSRF(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["csrfToken"] == "" {
		t.Error("csrfToken empty")
	}
	gotCookie := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.csrf-token" {
			gotCookie = true
			if c.Value != body["csrfToken"] {
				t.Errorf("cookie value = %q, want %q", c.Value, body["csrfToken"])
			}
		}
	}
	if !gotCookie {
		t.Error("csrf cookie not set")
	}
}

func TestNextAuth_CSRF_HTTPS(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)

	req := httptest.NewRequest("GET", "/api/auth/csrf", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.CSRF(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	gotHost := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "__Host-authjs.csrf-token" {
			gotHost = true
			if !c.Secure {
				t.Error("expected Secure flag")
			}
		}
	}
	if !gotHost {
		t.Error("expected __Host- cookie under HTTPS")
	}
}

func TestNextAuth_Providers(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/auth/providers", nil)
	rr := httptest.NewRecorder()
	h.Providers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "credentials") {
		t.Error("response should mention credentials provider")
	}
}

func TestNextAuth_Session_NoCookie(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	rr := httptest.NewRecorder()
	h.Session(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("expected empty object, got %s", rr.Body.String())
	}
}

func TestNextAuth_Session_BadCookie(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: "garbage"})
	rr := httptest.NewRecorder()
	h.Session(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Body.String() != "{}\n" && !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("want empty session, got %q", rr.Body.String())
	}
}

func TestNextAuth_Session_ValidCookie(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	h := NewNextAuthHandler(db, logger, v, store)

	// Seed user + session row so the Session handler's revoke-check
	// passes and returns the user payload. Without these the handler
	// (correctly) treats the cookie as stale and returns empty {}.
	userID := seedTestUser(t, db)
	sess, err := store.Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Alice", "a@b.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.Session(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	user, _ := body["user"].(map[string]interface{})
	if user == nil || user["email"] != "a@b.com" {
		t.Errorf("session user wrong: %+v", body)
	}
}

func TestNextAuth_CallbackCredentials_MissingCSRF(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(""))
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestNextAuth_CallbackCredentials_BadCSRFMatch(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	body := url.Values{"email": {"a@b.com"}, "password": {"x"}, "csrfToken": {"wrong"}}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "real-csrf"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// TestNextAuth_CallbackCredentials_OversizeBody asserts the body cap
// rejects a >16 KiB POST. Previously r.ParseForm() ran unbounded against
// the request body, letting a single client allocate arbitrary memory
// per request -- an easy DoS lever on a public auth endpoint. The fix
// (http.MaxBytesReader on r.Body) surfaces as a ParseForm() error, which
// the handler maps to 400 "Invalid request". Both branches of the
// handler (form and JSON) read through the same capped Body, so a
// single test on either path verifies the bound.
func TestNextAuth_CallbackCredentials_OversizeBody(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	// 16 KiB + 1 byte of urlencoded data exceeds the cap defined by
	// callbackCredentialsMaxBodyBytes. The body is otherwise valid
	// form syntax so only the size triggers the rejection.
	body := "email=" + strings.Repeat("x", callbackCredentialsMaxBodyBytes+1)
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// CSRF cookie must be present to pass the early-return cookie check
	// (the body cap runs after the cookie presence check, before the
	// token-value comparison).
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body exceeded MaxBytesReader cap)", rr.Code)
	}
}

func TestNextAuth_CallbackCredentials_EmptyCreds_JSON(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	jsonBody, _ := json.Marshal(map[string]string{"csrfToken": "csrf-x"})
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "CredentialsSignin") {
		t.Errorf("want CredentialsSignin, got %s", rr.Body.String())
	}
}

func TestNextAuth_CallbackCredentials_InvalidPassword(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	// Get DB through internal call: we need to insert a user with a hashed password.
	// Instead, use a new handler with our own db handle.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	hashed, _ := bcrypt.GenerateFromPassword([]byte("realpassword"), 4)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'login@example.com', 'Login', ?)`, string(hashed)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h2 := NewNextAuthHandler(db, logger, v, sessions.NewDBStore(db))
	_ = h

	body := url.Values{"email": {"login@example.com"}, "password": {"wrong-pass"}, "csrfToken": {"csrf-x"}, "json": {"true"}}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h2.CallbackCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "CredentialsSignin") {
		t.Errorf("expected CredentialsSignin, got %s", rr.Body.String())
	}
}

func TestNextAuth_CallbackCredentials_Success_FormRedirect(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	hashed, _ := bcrypt.GenerateFromPassword([]byte("rightpassword"), 4)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'good@example.com', 'Good', ?)`, string(hashed)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := NewNextAuthHandler(db, logger, v, sessions.NewDBStore(db))

	body := url.Values{"email": {"good@example.com"}, "password": {"rightpassword"}, "csrfToken": {"csrf-x"}, "callbackUrl": {"/dashboard"}}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("location = %q, want /dashboard", loc)
	}
}

func TestNextAuth_CallbackCredentials_Success_OpenRedirectBlocked(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	hashed, _ := bcrypt.GenerateFromPassword([]byte("rightpassword"), 4)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'r@example.com', 'R', ?)`, string(hashed)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := NewNextAuthHandler(db, logger, v, sessions.NewDBStore(db))

	tests := []struct {
		name        string
		callbackURL string
	}{
		{"absolute https", "https://evil.com"},
		{"protocol-relative", "//evil.com"},
		{"missing leading slash", "evil.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := url.Values{"email": {"r@example.com"}, "password": {"rightpassword"}, "csrfToken": {"csrf"}, "callbackUrl": {tt.callbackURL}}.Encode()
			req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf"})
			rr := httptest.NewRecorder()
			h.CallbackCredentials(rr, req)
			loc := rr.Header().Get("Location")
			if loc != "/" {
				t.Errorf("location = %q, want / (open redirect blocked)", loc)
			}
		})
	}
}

func TestNextAuth_CallbackCredentials_JSONResponse(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	hashed, _ := bcrypt.GenerateFromPassword([]byte("rightpassword"), 4)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'j@example.com', 'J', ?)`, string(hashed)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := NewNextAuthHandler(db, logger, v, sessions.NewDBStore(db))

	jsonBody, _ := json.Marshal(map[string]string{
		"email":     "j@example.com",
		"password":  "rightpassword",
		"csrfToken": "csrf-j",
	})
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-j"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
}

func TestNextAuth_SignOut(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie should be cleared (MaxAge<0)")
	}
}

func TestNextAuth_SignOut_GETRedirect(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/auth/signout", nil)
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
}

func TestNextAuth_SignIn(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)

	// Default
	req := httptest.NewRequest("GET", "/api/auth/signin", nil)
	rr := httptest.NewRecorder()
	h.SignIn(rr, req)
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}

	// Open redirect blocked
	req = httptest.NewRequest("GET", "/api/auth/signin?callbackUrl=https://evil.com", nil)
	rr = httptest.NewRecorder()
	h.SignIn(rr, req)
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "callbackUrl=%2F") && !strings.HasSuffix(loc, "callbackUrl=%2F") {
		// When open redirect is rejected, callbackUrl should fall back to "/"
		if !strings.Contains(loc, "callbackUrl=") || strings.Contains(loc, "evil.com") {
			t.Errorf("location should rewrite open redirect to /, got %q", loc)
		}
	}
}

func TestNextAuth_Error(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)

	req := httptest.NewRequest("GET", "/api/auth/error?error=Verification", nil)
	rr := httptest.NewRecorder()
	h.Error(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Verification") {
		t.Errorf("body should mention error type: %s", rr.Body.String())
	}

	// Default
	req = httptest.NewRequest("GET", "/api/auth/error", nil)
	rr = httptest.NewRecorder()
	h.Error(rr, req)
	if !strings.Contains(rr.Body.String(), "Default") {
		t.Errorf("default error case missing: %s", rr.Body.String())
	}
}
