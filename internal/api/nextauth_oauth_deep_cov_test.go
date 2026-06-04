package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// covNAONextAuth builds a NextAuthHandler bound to a fresh DB + sessions
// store, mirroring newNextAuthHandlerWithStore but returning the DB so
// the deep-coverage tests can seed users/sessions directly.
func covNAONextAuth(t *testing.T) (*NextAuthHandler, *auth.JWTValidator, sessions.Store, *sql.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	return NewNextAuthHandler(db, logger, v, store), v, store, db
}

// covNAOseedSession seeds a user + active session row and mints an
// access+refresh pair anchored at that row. Returns the ids and tokens.
func covNAOseedSession(t *testing.T, v *auth.JWTValidator, store sessions.Store, db *sql.DB) (uid, sid, access, refresh string) {
	t.Helper()
	uid = seedTestUser(t, db)
	sess, err := store.Create(context.Background(), uid, "ua", "1.2.3.4", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	access, err = v.IssueAccessToken(uid, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	refresh, err = v.IssueRefreshToken(uid, sess.ID)
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}
	return uid, sess.ID, access, refresh
}

// --- nextauth.go: Session edge branches ------------------------------------

// covNAO: session row revoked between requests → ErrNotFound is not the
// only "logged-out" signal; a revoked-but-present row (sess.Active==false)
// must also clear cookies and return empty {}.
func TestCovNAO_Session_RevokedRowClearsCookies(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, sid, access, _ := covNAOseedSession(t, v, store, db)
	if err := store.Revoke(context.Background(), sid, sessions.ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: access})
	rr := httptest.NewRecorder()
	h.Session(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("revoked session should return empty object, got %q", rr.Body.String())
	}
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie should be cleared when row is revoked")
	}
}

// covNAO: a valid access cookie whose session row was deleted (ErrNotFound)
// is treated as logged-out — cookies cleared, empty {}.
func TestCovNAO_Session_MissingRowClearsCookies(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, sid, access, _ := covNAOseedSession(t, v, store, db)
	// Hard-delete the row so sessions.Get returns ErrNotFound.
	if _, err := db.Exec("DELETE FROM user_sessions WHERE id = ?", sid); err != nil {
		t.Fatalf("delete session row: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: access})
	rr := httptest.NewRecorder()
	h.Session(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("deleted session should return empty object, got %q", rr.Body.String())
	}
}

// covNAO: under HTTPS the Session handler reads the __Secure- cookie name.
// A valid token in that cookie returns the user payload; the same token in
// the non-secure name would be invisible on a TLS request.
func TestCovNAO_Session_HTTPSCookieName(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, _, access, _ := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("GET", "https://crewship.test/api/auth/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(&http.Cookie{Name: "__Secure-authjs.session-token", Value: access})
	rr := httptest.NewRecorder()
	h.Session(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "test@example.com") {
		t.Errorf("HTTPS branch should read __Secure- cookie and return user, got %q", rr.Body.String())
	}
}

// --- nextauth.go: RefreshToken edge branches -------------------------------

// covNAO: sessions store unconfigured → the refresh handler returns 500
// (it cannot rotate without a store). Origin/cookie are valid so the
// failure isolates the nil-store branch.
func TestCovNAO_Refresh_NilStore500(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	// nil sessions store
	h := NewNextAuthHandler(db, logger, v, nil)

	refresh, err := v.IssueRefreshToken("u-x", "s-x")
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}
	req := httptest.NewRequest("POST", "http://crewship.test/api/auth/token/refresh", nil)
	req.Host = "crewship.test"
	req.Header.Set("Origin", "http://crewship.test")
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("nil store status = %d, want 500", rr.Code)
	}
}

// covNAO: the session row referenced by the refresh JTI is gone → 401
// session_revoked (ErrNotFound branch on store.Get).
func TestCovNAO_Refresh_SessionRowGone(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, sid, _, refresh := covNAOseedSession(t, v, store, db)
	if _, err := db.Exec("DELETE FROM user_sessions WHERE id = ?", sid); err != nil {
		t.Fatalf("delete session row: %v", err)
	}

	req := httptest.NewRequest("POST", "http://crewship.test/api/auth/token/refresh", nil)
	req.Host = "crewship.test"
	req.Header.Set("Origin", "http://crewship.test")
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "session_revoked") {
		t.Errorf("body should signal session_revoked, got %q", rr.Body.String())
	}
}

// covNAO: legacy non-path-scoped cookie name is accepted for one release
// cycle. With the new refreshCookieName absent but "authjs.refresh-token"
// present, the handler falls back to it and rotates successfully.
//
// Note: under HTTP the new and legacy names are identical
// ("authjs.refresh-token"), so to exercise the fallback arm we run over
// HTTPS where refreshCookieName == "__Secure-authjs.refresh-token" and
// only the legacy name is supplied.
func TestCovNAO_Refresh_LegacyCookieFallback(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, _, _, refresh := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("POST", "https://crewship.test/api/auth/token/refresh", nil)
	req.Host = "crewship.test"
	req.Header.Set("Origin", "https://crewship.test")
	req.Header.Set("X-Forwarded-Proto", "https")
	// Only the legacy name — the new __Secure- name is absent.
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("legacy-cookie fallback status = %d (body %s), want 200", rr.Code, rr.Body.String())
	}
}

// covNAO: empty Host on the refresh request fails sameOriginRefresh → 403.
func TestCovNAO_Refresh_EmptyHostForbidden(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, _, _, refresh := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("POST", "http://crewship.test/api/auth/token/refresh", nil)
	req.Host = "" // force sameOriginRefresh's host=="" early return
	req.Header.Set("Origin", "http://crewship.test")
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("empty-host status = %d, want 403", rr.Code)
	}
}

// covNAO: a malformed Origin header (control bytes) fails url.Parse inside
// sameOriginRefresh → 403.
func TestCovNAO_Refresh_MalformedOriginForbidden(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, _, _, refresh := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("POST", "http://crewship.test/api/auth/token/refresh", nil)
	req.Host = "crewship.test"
	req.Header.Set("Origin", "http://exa\x7fmple.com\n") // unparsable
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("malformed-origin status = %d, want 403", rr.Code)
	}
}

// covNAO: malformed Referer (when Origin absent) fails url.Parse → 403.
func TestCovNAO_Refresh_MalformedRefererForbidden(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, _, _, refresh := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("POST", "http://crewship.test/api/auth/token/refresh", nil)
	req.Host = "crewship.test"
	req.Header.Del("Origin")
	req.Header.Set("Referer", "http://exa\x7fmple.com\n")
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("malformed-referer status = %d, want 403", rr.Code)
	}
}

// covNAO: sameOriginRefresh strips the request-Host port before comparing
// against the Origin host. A request to host:port with a port-less Origin
// of the same host must pass.
func TestCovNAO_Refresh_HostPortStrippedMatch(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, _, _, refresh := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("POST", "http://crewship.test:8443/api/auth/token/refresh", nil)
	req.Host = "crewship.test:8443"
	req.Header.Set("Origin", "http://crewship.test") // no port
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("host-port-stripped match status = %d (body %s), want 200", rr.Code, rr.Body.String())
	}
}

// --- nextauth.go: SignOut + findSessionID branches -------------------------

// covNAO: SignOut with a valid access cookie locates the session via
// findSessionID's access-cookie arm and revokes the row server-side.
func TestCovNAO_SignOut_RevokesActiveSession(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, sid, access, _ := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: access})
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	sess, err := store.Get(context.Background(), sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.RevokedAt == nil {
		t.Error("signout should revoke the session row")
	}
}

// covNAO: SignOut falls back to the refresh cookie for the session id when
// the access cookie is absent (idled-past-access-expiry case). Exercises
// findSessionID's refresh arm + the JSON Accept-header response branch.
func TestCovNAO_SignOut_RefreshFallbackAndJSON(t *testing.T) {
	t.Parallel()
	h, v, store, db := covNAONextAuth(t)
	_, sid, _, refresh := covNAOseedSession(t, v, store, db)

	req := httptest.NewRequest("GET", "/api/auth/signout", nil) // GET so only Accept drives JSON
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (JSON Accept branch)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/login") {
		t.Errorf("body should carry /login url, got %q", rr.Body.String())
	}
	sess, err := store.Get(context.Background(), sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.RevokedAt == nil {
		t.Error("signout should revoke via refresh-cookie fallback")
	}
}

// covNAO: clearAuthCookies under HTTPS sets Secure=true on the expiring
// cookies. Drive it via SignOut on a TLS-flagged request.
func TestCovNAO_SignOut_HTTPSClearsSecureCookies(t *testing.T) {
	t.Parallel()
	h, _, _, _ := covNAONextAuth(t)

	req := httptest.NewRequest("POST", "https://crewship.test/api/auth/signout", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	sawSecureCleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "__Secure-authjs.session-token" && c.MaxAge < 0 && c.Secure {
			sawSecureCleared = true
		}
	}
	if !sawSecureCleared {
		t.Error("HTTPS signout should clear __Secure- session cookie with Secure=true")
	}
}

// --- oauth_token.go: request-building arms ---------------------------------

// covNAO: exchangeOAuthCode sets client_secret + code_verifier in the form
// body when supplied. The network dial fails (unroutable RFC 5737 host),
// but the request-building arms (clientSecret != "" and codeVerifier != "")
// are exercised before the failure.
func TestCovNAO_ExchangeOAuthCode_WithSecretAndVerifier(t *testing.T) {
	t.Parallel()
	_, err := exchangeOAuthCode(context.Background(),
		"http://192.0.2.1:1/token", "cid", "the-secret", "the-code", "https://app/cb", "the-verifier")
	if err == nil {
		t.Error("expected connection error against unroutable host")
	}
}

// covNAO: refreshOAuthToken sets client_secret when supplied. Same
// unroutable-host failure, but the clientSecret != "" arm is covered.
func TestCovNAO_RefreshOAuthToken_WithSecret(t *testing.T) {
	t.Parallel()
	_, err := refreshOAuthToken(context.Background(),
		"http://192.0.2.1:1/token", "cid", "the-secret", "the-refresh-token")
	if err == nil {
		t.Error("expected connection error against unroutable host")
	}
}

// --- oauth_token.go: storeOAuthTokens (oauth_creds.go) branches ------------

// covNAO: storeOAuthTokens with no refresh token and no expiry — the
// CASE-WHEN keeps any existing refresh row and expires_at stays "".
func TestCovNAO_StoreOAuthTokens_NoRefreshNoExpiry(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedOAuthCredential(t, db, wsID, "cred-norefresh", "c", "s", "https://p/auth", "https://p/token")

	// Pre-seed an existing refresh enc so we can assert it survives.
	preEnc, _ := encryption.Encrypt("pre-existing-rt")
	if _, err := db.Exec("UPDATE credentials SET oauth_refresh_token_enc = ? WHERE id = 'cred-norefresh'", preEnc); err != nil {
		t.Fatalf("seed existing refresh: %v", err)
	}

	resp := &tokenResponse{AccessToken: "new-at", RefreshToken: "", ExpiresIn: 0, TokenType: "Bearer"}
	if err := h.storeOAuthTokens(context.Background(), "cred-norefresh", resp); err != nil {
		t.Fatalf("store: %v", err)
	}

	var encAccess, encRefresh, expiresAt, status string
	if err := db.QueryRow(
		"SELECT encrypted_value, oauth_refresh_token_enc, oauth_token_expires_at, status FROM credentials WHERE id = 'cred-norefresh'",
	).Scan(&encAccess, &encRefresh, &expiresAt, &status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", status)
	}
	if expiresAt != "" {
		t.Errorf("expires_at = %q, want empty (ExpiresIn==0)", expiresAt)
	}
	at, _ := encryption.Decrypt(encAccess)
	if at != "new-at" {
		t.Errorf("access = %q, want new-at", at)
	}
	rt, _ := encryption.Decrypt(encRefresh)
	if rt != "pre-existing-rt" {
		t.Errorf("existing refresh should survive empty-refresh update, got %q", rt)
	}
}

// --- oauth_token.go: refreshExpiringTokens DB branches ---------------------

// covNAO: a row whose oauth_refresh_token_enc cannot be decrypted (garbage
// ciphertext) must be marked EXPIRED and skipped — the decrypt-error arm.
func TestCovNAO_RefreshExpiringTokens_UndecryptableRefreshMarksExpired(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	encAccess, _ := encryption.Encrypt("at-old")
	expiresSoon := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	// oauth_refresh_token_enc is non-empty but not a valid ciphertext, so
	// encryption.Decrypt fails inside refreshExpiringTokens.
	// oauth_client_secret_enc must be '' (not NULL) — the worker scans it
	// into a string and a NULL would error → silent row skip.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status,
			oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc, oauth_token_expires_at,
			created_by, created_at, updated_at, scope, provider)
		VALUES ('exp-baddec', ?, 'bad-dec', ?, 'OAUTH2', 'ACTIVE',
			'client', '', 'https://p/token', 'not-a-valid-ciphertext', ?, ?, datetime('now'), datetime('now'), 'WORKSPACE', 'NONE')`,
		wsID, encAccess, expiresSoon, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	refreshExpiringTokens(context.Background(), db, nil, logger)

	var status string
	if err := db.QueryRow("SELECT status FROM credentials WHERE id = 'exp-baddec'").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "EXPIRED" {
		t.Errorf("status = %q, want EXPIRED (undecryptable refresh token)", status)
	}
}

// covNAO: a row whose decrypted refresh token is the empty string is
// skipped (continue) without touching status — the empty-refresh arm.
func TestCovNAO_RefreshExpiringTokens_EmptyDecryptedRefreshSkipped(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	encAccess, _ := encryption.Encrypt("at-old")
	encEmptyRefresh, _ := encryption.Encrypt("") // decrypts to ""
	expiresSoon := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	// oauth_client_secret_enc = '' so the worker's Scan doesn't hit a NULL.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status,
			oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc, oauth_token_expires_at,
			created_by, created_at, updated_at, scope, provider)
		VALUES ('exp-emptyrt', ?, 'empty-rt', ?, 'OAUTH2', 'ACTIVE',
			'client', '', 'https://p/token', ?, ?, ?, datetime('now'), datetime('now'), 'WORKSPACE', 'NONE')`,
		wsID, encAccess, encEmptyRefresh, expiresSoon, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	refreshExpiringTokens(context.Background(), db, nil, logger)

	// Empty refresh → skipped, status untouched (still ACTIVE).
	var status string
	if err := db.QueryRow("SELECT status FROM credentials WHERE id = 'exp-emptyrt'").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE (empty refresh token skipped, not expired)", status)
	}
}

// covNAO: a row with an undecryptable client secret is skipped at the
// secret-decrypt arm (continue) before any refresh attempt — status stays
// ACTIVE because the row never reaches the refresh-failure path.
func TestCovNAO_RefreshExpiringTokens_UndecryptableClientSecretSkipped(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	encAccess, _ := encryption.Encrypt("at-old")
	encRefresh, _ := encryption.Encrypt("rt-good")
	expiresSoon := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status,
			oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc, oauth_token_expires_at,
			created_by, created_at, updated_at, scope, provider)
		VALUES ('exp-badsecret', ?, 'bad-secret', ?, 'OAUTH2', 'ACTIVE',
			'client', 'garbage-secret-ciphertext', 'https://p/token', ?, ?, ?, datetime('now'), datetime('now'), 'WORKSPACE', 'NONE')`,
		wsID, encAccess, encRefresh, expiresSoon, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	refreshExpiringTokens(context.Background(), db, nil, logger)

	var status string
	if err := db.QueryRow("SELECT status FROM credentials WHERE id = 'exp-badsecret'").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE (client-secret decrypt failure skips before refresh)", status)
	}
}
