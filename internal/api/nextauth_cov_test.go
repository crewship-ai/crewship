package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// covNANewHandler builds a NextAuthHandler over a fresh test DB and
// returns the handler, validator, and store. The handler keeps the *sql.DB
// on h.db, so callers seed users / sessions through that. Mirrors the
// inline construction in nextauth_test.go / signout_test.go.
func covNANewHandler(t *testing.T) (*NextAuthHandler, *auth.JWTValidator, sessions.Store) {
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

// covNASeedUserWithPassword inserts a user with a known bcrypt hash and
// returns the id. Distinct name from the package-level seed helpers so
// it never collides on redefinition.
func covNASeedUserWithPassword(t *testing.T, h *NextAuthHandler, id, email, password string) {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES (?, ?, ?, ?)`,
		id, email, "Cov User", string(hashed),
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// TestCovNA_Session_RevokedRow covers the sess.Active()==false branch:
// a valid token whose backing user_sessions row has been revoked must
// be treated as logged-out (empty {}) and clear the cookie.
func TestCovNA_Session_RevokedRow(t *testing.T) {
	t.Parallel()
	h, v, store := covNANewHandler(t)
	userID := seedTestUser(t, h.db)

	sess, err := store.Create(context.Background(), userID, "ua", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Rev", "rev@example.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	// Revoke the row out from under the still-valid token.
	if err := store.Revoke(context.Background(), sess.ID, sessions.ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.Session(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("revoked session should return empty {}, got %s", rr.Body.String())
	}
	// Cookie cleared (MaxAge<0).
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("revoked session must clear the access cookie")
	}
}

// TestCovNA_Session_SessionRowGone covers the sessions.ErrNotFound
// branch: token references a sid whose row never existed (or was hard-
// deleted). Same logged-out + clear-cookie outcome as revoked.
func TestCovNA_Session_SessionRowGone(t *testing.T) {
	t.Parallel()
	h, v, _ := covNANewHandler(t)
	userID := seedTestUser(t, h.db)

	// sid that has no user_sessions row → store.Get returns ErrNotFound.
	tok, err := v.IssueAccessToken(userID, "s_does_not_exist", "Gone", "gone@example.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.Session(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("missing session row should return empty {}, got %s", rr.Body.String())
	}
}

// TestCovNA_CallbackCredentials_InvalidJSON covers the readJSON-error
// 400 branch on the JSON content-type path.
func TestCovNA_CallbackCredentials_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)

	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON body)", rr.Code)
	}
}

// TestCovNA_CallbackCredentials_EmptyCreds_FormRedirect covers the
// non-JSON respondCredentialsError path: missing email/password with a
// form request redirects (302) to /login?error=CredentialsSignin.
func TestCovNA_CallbackCredentials_EmptyCreds_FormRedirect(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)

	// Only csrfToken, no email/password, no json/redirect flags → form
	// redirect branch of respondCredentialsError.
	body := url.Values{"csrfToken": {"csrf-x"}}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (form error redirect)", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "error=CredentialsSignin") {
		t.Errorf("location = %q, want /login?error=CredentialsSignin", loc)
	}
}

// TestCovNA_CallbackCredentials_Lockout covers the ErrAccountLocked
// branch: a pre-locked account returns the generic CredentialsSignin
// (200 JSON), never leaking that the account is locked.
func TestCovNA_CallbackCredentials_Lockout(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)
	covNASeedUserWithPassword(t, h, "u_locked", "locked@example.com", "rightpassword")

	// Force the account into a locked state: locked_until in the future.
	lockedUntil := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.db.Exec(
		`UPDATE users SET locked_until = ?, failed_login_count = ? WHERE id = 'u_locked'`,
		lockedUntil, LockoutThreshold,
	); err != nil {
		t.Fatalf("lock account: %v", err)
	}

	// Even with the CORRECT password, the lockout check fires first.
	body := url.Values{
		"email":     {"locked@example.com"},
		"password":  {"rightpassword"},
		"csrfToken": {"csrf-x"},
		"json":      {"true"},
	}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "CredentialsSignin") {
		t.Errorf("locked account must respond generic CredentialsSignin, got %s", rr.Body.String())
	}
	// No auth cookie should have been minted on the lockout path.
	for _, c := range rr.Result().Cookies() {
		if (c.Name == "authjs.session-token" || c.Name == "__Secure-authjs.session-token") && c.Value != "" {
			t.Errorf("lockout path must not mint a session cookie, got %q=%q", c.Name, c.Value)
		}
	}
}

// TestCovNA_CallbackCredentials_Success_RedirectFalseJSON covers the
// wantJSON branch reached via redirect=false (not via Content-Type
// json) plus the cookie-minting + setAuthCookies success path.
func TestCovNA_CallbackCredentials_Success_RedirectFalseJSON(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)
	covNASeedUserWithPassword(t, h, "u_ok", "ok@example.com", "rightpassword")

	body := url.Values{
		"email":     {"ok@example.com"},
		"password":  {"rightpassword"},
		"csrfToken": {"csrf-x"},
		"redirect":  {"false"},
	}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
	// callbackUrl empty → defaults to "/".
	if resp["url"] != "/" {
		t.Errorf("url = %v, want /", resp["url"])
	}
	// Both cookies minted by setAuthCookies.
	var hasAccess, hasRefresh bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" && c.Value != "" {
			hasAccess = true
		}
		if c.Name == "authjs.refresh-token" && c.Value != "" {
			hasRefresh = true
		}
	}
	if !hasAccess || !hasRefresh {
		t.Errorf("successful login must set both cookies (access=%v refresh=%v)", hasAccess, hasRefresh)
	}
}

// TestCovNA_CallbackCredentials_Success_HTTPSSecureCookies covers the
// setAuthCookies HTTPS branch: under X-Forwarded-Proto=https the cookies
// take the __Secure- prefix and the Secure flag.
func TestCovNA_CallbackCredentials_Success_HTTPSSecureCookies(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)
	covNASeedUserWithPassword(t, h, "u_https", "https@example.com", "rightpassword")

	body := url.Values{
		"email":     {"https@example.com"},
		"password":  {"rightpassword"},
		"csrfToken": {"csrf-x"},
		"json":      {"true"},
	}.Encode()
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	// Under HTTPS the CSRF cookie name carries the __Host- prefix, so
	// h.csrfCookieName(r) looks that name up — seed it accordingly.
	req.AddCookie(&http.Cookie{Name: "__Host-authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var secureAccess bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "__Secure-authjs.session-token" {
			secureAccess = true
			if !c.Secure {
				t.Error("HTTPS access cookie must carry Secure flag")
			}
		}
	}
	if !secureAccess {
		t.Error("expected __Secure-authjs.session-token under HTTPS")
	}
}

// TestCovNA_SignOut_JSONAcceptGET covers the JSON-response branch of
// SignOut reached via the Accept: application/json header on a GET
// request (the r.Method=="POST" path is exercised elsewhere).
func TestCovNA_SignOut_JSONAcceptGET(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)

	req := httptest.NewRequest("GET", "/api/auth/signout", nil)
	req.Header.Set("Accept", "application/json")
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (JSON accept)", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["url"] != "/login" {
		t.Errorf("url = %v, want /login", resp["url"])
	}
}

// TestCovNA_Providers_Shape pins the credentials provider descriptor
// returned by Providers — asserts the signin/callback URLs are present.
func TestCovNA_Providers_Shape(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)

	req := httptest.NewRequest("GET", "/api/auth/providers", nil)
	rr := httptest.NewRecorder()
	h.Providers(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp map[string]map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cred := resp["credentials"]
	if cred == nil || cred["callbackUrl"] != "/api/auth/callback/credentials" {
		t.Errorf("credentials provider descriptor malformed: %+v", resp)
	}
}

// TestCovNA_CSRF_PlainHTTPCookie pins the non-HTTPS CSRF cookie path:
// plain HTTP gets the un-prefixed authjs.csrf-token without Secure.
func TestCovNA_CSRF_PlainHTTPCookie(t *testing.T) {
	t.Parallel()
	h, _, _ := covNANewHandler(t)

	req := httptest.NewRequest("GET", "/api/auth/csrf", nil)
	rr := httptest.NewRecorder()
	h.CSRF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.csrf-token" {
			got = true
			if c.Secure {
				t.Error("plain-HTTP csrf cookie must not be Secure")
			}
		}
	}
	if !got {
		t.Error("expected authjs.csrf-token under plain HTTP")
	}
}
