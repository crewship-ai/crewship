package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// ---------------------------------------------------------------------------
// auth_recovery_cov_test.go — branch coverage for the password-recovery
// surface (auth_recovery.go) plus the still-uncovered error/fault branches
// in auth.go and nextauth.go that the existing auth_test.go / auth_extra_test.go
// / nextauth_test.go / refresh_test.go do not reach.
//
// Helpers prefixed covAR*; test funcs prefixed TestCovAR*. Reuses the
// existing harness: setupTestDB, seedTestUser, seedTestUserWithPassword,
// seedLockoutUser, newRecoveryHandler, newTestJWTValidator, newTestLogger,
// sessions.NewDBStore.
//
// SKIPPED (documented, not covered here):
//   - Real SMTP / network email delivery — the stubMailer records messages
//     in-process; we assert token-mint + DB rows, never an actual send.
//   - Google-OAuth network round-trips (token exchange, userinfo fetch) —
//     covered structurally in auth_extra_test.go without hitting Google.
// ---------------------------------------------------------------------------

// covARFailRevokeStore wraps a real sessions.Store but forces
// RevokeAllForUser to return an error so Reset's "couldn't sign out
// existing sessions" 500 branch becomes reachable without tearing down
// the DB (the password UPDATE has to commit first).
type covARFailRevokeStore struct {
	sessions.Store
}

func (s covARFailRevokeStore) RevokeAllForUser(_ context.Context, _ string, _ string) (int64, error) {
	return 0, context.DeadlineExceeded
}

// covARMintToken inserts a live password_reset token for email and
// returns the raw token the client would carry. Mirrors what Forgot
// persists (SHA256 hash stored, raw token mailed).
func covARMintToken(t *testing.T, h *RecoveryHandler, email, rawToken string) {
	t.Helper()
	hash := hashResetToken(rawToken)
	expires := time.Now().UTC().Add(resetTokenTTL).Format(time.RFC3339)
	if _, err := h.db.Exec(
		`INSERT INTO verification_tokens (identifier, token, expires, purpose) VALUES (?, ?, ?, 'password_reset')`,
		email, hash, expires); err != nil {
		t.Fatalf("mint token: %v", err)
	}
}

func covARPost(method, path, body string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req, httptest.NewRecorder()
}

// ---- Forgot: invalid JSON / bad email always return the no-enumeration 200 -

func TestCovARForgot_InvalidJSON_Returns200NoEnumeration(t *testing.T) {
	db := setupTestDB(t)
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	req, rr := covARPost("POST", "/api/v1/auth/forgot", `{not-json`)
	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (malformed JSON must not be a side channel)", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d emails on malformed JSON, want 0", len(mail.sent))
	}
}

func TestCovARForgot_BadEmailFormat_Returns200(t *testing.T) {
	db := setupTestDB(t)
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	req, rr := covARPost("POST", "/api/v1/auth/forgot", `{"email":"not-an-email"}`)
	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for malformed email", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d emails for malformed email, want 0", len(mail.sent))
	}
}

// Forgot's user-lookup runs a DB query; closing the DB first drives the
// "lookup failed" branch which still returns 200 (operational errors must
// not leak account existence via 500 vs 200).
func TestCovARForgot_DBClosed_StillReturns200(t *testing.T) {
	db := setupTestDB(t)
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)
	db.Close() // fault injection: lookup query now errors

	req, rr := covARPost("POST", "/api/v1/auth/forgot", `{"email":"someone@example.com"}`)
	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when DB lookup errors (no 500 enumeration channel)", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d emails after DB error, want 0", len(mail.sent))
	}
}

// Malformed CREWSHIP_PUBLIC_URL → publicBase stays nil → Forgot refuses to
// mint a token even with a real user + configured mailer, still 200.
func TestCovARForgot_MalformedPublicURL_RefusesMint(t *testing.T) {
	db := setupTestDB(t)
	_ = seedTestUserWithPassword(t, db, "badurl@example.com", "originalpw")
	mail := &stubMailer{configured: true}

	// Bypass the helper so we control the env value: a value with no
	// scheme/host is rejected by NewRecoveryHandler and publicBase is nil.
	t.Setenv("CREWSHIP_PUBLIC_URL", "::::not-a-url")
	h := NewRecoveryHandler(db, newTestLogger(), mail, nil)

	req, rr := covARPost("POST", "/api/v1/auth/forgot", `{"email":"badurl@example.com"}`)
	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d emails with malformed public URL, want 0", len(mail.sent))
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier=?`, "badurl@example.com").Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Errorf("tokens stored = %d, want 0 when public URL malformed", count)
	}
}

// ---- Reset: validation + fault branches -----------------------------------

func TestCovARReset_InvalidJSON_Returns400(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)

	req, rr := covARPost("POST", "/api/v1/auth/reset", `}{bad`)
	h.Reset(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

// Password long enough but token empty → the explicit "Token is required"
// 400 branch (distinct from short-password and invalid-token).
func TestCovARReset_MissingToken_Returns400(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)

	req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"","new_password":"longenough12"}`)
	h.Reset(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (token required)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Token is required") {
		t.Errorf("body = %q, want mention of 'Token is required'", rr.Body.String())
	}
}

// Unknown token (valid shape, no DB row) → 400 "Invalid or expired token".
func TestCovARReset_UnknownToken_Returns400(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)

	req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"never-issued-token","new_password":"longenough12"}`)
	h.Reset(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown token", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Invalid or expired token") {
		t.Errorf("body = %q, want 'Invalid or expired token'", rr.Body.String())
	}
}

// Closing the DB before Reset drives BeginTx failure → 500.
func TestCovARReset_DBClosed_Returns500(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	db.Close() // fault injection: BeginTx now fails

	req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"sometoken","new_password":"longenough12"}`)
	h.Reset(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when DB is unavailable", rr.Code)
	}
}

// Token row points at an identifier with no matching users row → the
// "resolve user" lookup inside the tx misses → 400 (generic, no leak).
func TestCovARReset_TokenForMissingUser_Returns400(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)

	// Mint a live token for an email that has NO users row.
	covARMintToken(t, h, "ghost@example.com", "ghost-raw-token-abc123")

	req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"ghost-raw-token-abc123","new_password":"longenough12"}`)
	h.Reset(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when token resolves to no user", rr.Code)
	}
}

// Happy path with a real sessions store: password updates AND every active
// session is revoked. Covers the sessions.RevokeAllForUser success branch
// that the existing TestReset_HappyPath (nil store) skips.
func TestCovARReset_HappyPath_RevokesSessions(t *testing.T) {
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	userID := seedTestUserWithPassword(t, db, "revoke@example.com", "originalpw")
	// Give the user a live session so RevokeAllForUser has something to do.
	if _, err := store.Create(context.Background(), userID, "ua", "1.2.3.4", auth.RefreshTokenTTL); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, store)
	covARMintToken(t, h, "revoke@example.com", "revoke-raw-token-xyz789")

	req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"revoke-raw-token-xyz789","new_password":"brandnewpw12"}`)
	h.Reset(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200", rr.Code, rr.Body.String())
	}

	active, err := store.ListActiveForUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("active sessions after reset = %d, want 0 (RevokeAllForUser must fire)", len(active))
	}
}

// RevokeAllForUser failure after the password commit must surface as a
// hard 500 — a stolen cookie can't be allowed to outlive the reset.
func TestCovARReset_RevokeFailure_Returns500(t *testing.T) {
	db := setupTestDB(t)
	realStore := sessions.NewDBStore(db)
	userID := seedTestUserWithPassword(t, db, "stuckrevoke@example.com", "originalpw")
	if _, err := realStore.Create(context.Background(), userID, "ua", "1.2.3.4", auth.RefreshTokenTTL); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, covARFailRevokeStore{realStore})
	covARMintToken(t, h, "stuckrevoke@example.com", "stuck-raw-token-456def")

	req, rr := covARPost("POST", "/api/v1/auth/reset", `{"token":"stuck-raw-token-456def","new_password":"brandnewpw12"}`)
	h.Reset(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when session revoke fails post-commit", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "sign out existing sessions") {
		t.Errorf("body = %q, want the partial-success revoke-failure message", rr.Body.String())
	}

	// Password must still have committed (the message tells the user so).
	var hashed string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, userID).Scan(&hashed); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if hashed == "" {
		t.Error("password should have committed before the revoke step failed")
	}
}

// ---- auth.go: WsToken + Signup + Bootstrap fault branches ------------------

// WsToken with a nil validator must fail closed with 500 rather than
// panic on the IssueWSTicket call.
func TestCovARWsToken_NilValidator_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	// Construct with a nil validator directly (NewAuthHandler stores it
	// as-is). The handler's defensive nil check should 500.
	h := NewAuthHandler(db, newTestLogger(), nil, sessions.NewDBStore(db), true)

	req := httptest.NewRequest("POST", "/api/v1/auth/ws-token", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "x@y.com", Name: "n"}))
	rr := httptest.NewRecorder()
	h.WsToken(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when validator is nil", rr.Code)
	}
}

// Signup with the DB closed drives the existing-email probe error → 500.
func TestCovARSignup_DBClosed_Returns500(t *testing.T) {
	db := setupTestDB(t)
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, newTestLogger(), v, sessions.NewDBStore(db), true)
	db.Close() // fault injection: the SELECT id FROM users probe errors

	req, rr := covARPost("POST", "/api/v1/auth/signup", `{"full_name":"Dead DB","email":"deaddb@example.com","password":"longenough"}`)
	h.Signup(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the existing-email probe errors", rr.Code)
	}
}

// Bootstrap on a closed DB: the pre-tx COUNT(*) errors. The handler treats
// a non-nil error there as "not already initialized" (err==nil guard) and
// falls through; the BeginTx then fails → 500. Covers the bootstrap tx
// begin error branch.
func TestCovARBootstrap_DBClosed_Returns500(t *testing.T) {
	db := setupTestDB(t)
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, newTestLogger(), v, sessions.NewDBStore(db), false)
	// Never armed (test harness) → window check falls through; with no
	// users the pre-tx COUNT would normally pass. Close the DB so the tx
	// path errors.
	db.Close()

	req, rr := covARPost("POST", "/api/v1/auth/bootstrap", `{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when DB is unavailable during bootstrap", rr.Code)
	}
}

// Bootstrap arming-failed (DB error at arm time) must surface a 503 with the
// "arming failed" message — distinct from the generic 410 window-expired.
func TestCovARBootstrap_ArmingFailed_Returns503(t *testing.T) {
	db := setupTestDB(t)
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, newTestLogger(), v, sessions.NewDBStore(db), false)

	// Drive the fail-closed state: ArmDeployRaceWindow against a closed DB
	// records the error and leaves armed=false. We close a *separate*
	// throwaway handle so the main db stays usable for the pre-tx COUNT.
	failDB := setupTestDB(t)
	failDB.Close()
	failH := NewAuthHandler(failDB, newTestLogger(), v, sessions.NewDBStore(failDB), false)
	if err := failH.ArmDeployRaceWindow(context.Background(), time.Hour); err == nil {
		t.Fatal("ArmDeployRaceWindow against closed DB should have errored")
	}
	if !failH.bootstrapArmingFailed() {
		t.Fatal("precondition: arming should be marked failed")
	}

	// Re-point failH at the live db so the pre-tx COUNT(*) succeeds (0 users)
	// and the handler reaches the arming-failed gate. failH already carries
	// bootstrapArmed=false + bootstrapArmingErr set.
	failH.db = db
	_ = h // keep the healthy handler reference for symmetry / clarity

	req, rr := covARPost("POST", "/api/v1/auth/bootstrap", `{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	failH.Bootstrap(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when bootstrap arming failed", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "arming failed") {
		t.Errorf("body = %q, want mention of 'arming failed'", rr.Body.String())
	}
}

// Bootstrap happy path with a nil sessions store: the user/workspace/token
// commit succeeds but setSessionCookies fails (errSessionsStoreUnconfigured),
// so the handler returns 200 with session_pending=true rather than rolling
// back. Covers the session-pending branch the existing tests skip.
func TestCovARBootstrap_NilStore_SessionPending200(t *testing.T) {
	db := setupTestDB(t)
	v := newTestJWTValidator(t)
	h := NewAuthHandler(db, newTestLogger(), v, nil, false) // nil store
	armBootstrapForTest(t, h)

	req, rr := covARPost("POST", "/api/v1/auth/bootstrap", `{"full_name":"Pending Admin","email":"pending@example.com","password":"longenough"}`)
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200 session_pending", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "session_pending") {
		t.Errorf("body = %q, want session_pending:true", rr.Body.String())
	}
	// The admin row must NOT be rolled back — bootstrap is one-shot.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='pending@example.com'`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("admin row count = %d, want 1 (must survive a cookie-set failure)", count)
	}
}

// ---- nextauth.go: Session + CallbackCredentials + helper branches ----------

// Session with a revoked session row: the access JWE still validates but the
// sessions.Get → !Active branch clears cookies and returns empty {}.
func TestCovARSession_RevokedSession_ReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	v := newTestJWTValidator(t)
	h := NewNextAuthHandler(db, newTestLogger(), v, store)

	userID := seedTestUser(t, db)
	sess, err := store.Create(context.Background(), userID, "ua", "1.2.3.4", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Revoke(context.Background(), sess.ID, sessions.ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test", "t@e.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.Session(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "{}") {
		t.Errorf("body = %q, want empty {} for revoked session", rr.Body.String())
	}
	// Stale cookie should have been cleared (MaxAge<0).
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("revoked-session path must clear the stale access cookie")
	}
}

// Session with a valid JWE but the DB closed under the sessions.Get lookup:
// a transient store error must NOT clear cookies — it returns 500 and leaves
// the cookies intact (the false-logout trap the middleware fixed).
func TestCovARSession_StoreError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	v := newTestJWTValidator(t)
	h := NewNextAuthHandler(db, newTestLogger(), v, store)

	userID := seedTestUser(t, db)
	sess, err := store.Create(context.Background(), userID, "ua", "1.2.3.4", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test", "t@e.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	db.Close() // fault injection: sessions.Get now errors (not ErrNotFound)

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.Session(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on transient store error", rr.Code)
	}
	// Cookies must NOT be cleared on a transient error.
	for _, c := range rr.Result().Cookies() {
		if c.Name == "authjs.session-token" && c.MaxAge < 0 {
			t.Error("transient store error must not clear cookies (false-logout trap)")
		}
	}
}

// CallbackCredentials with valid credentials but a nil sessions store:
// issueSession returns "sessions store not configured" → 500. Covers the
// issueSession nil-store branch end-to-end through the handler.
func TestCovARCallbackCredentials_NilStore_Returns500(t *testing.T) {
	db := setupTestDB(t)
	v := newTestJWTValidator(t)
	// nil store → issueSession fails after the credentials check passes.
	h := NewNextAuthHandler(db, newTestLogger(), v, nil)
	_ = seedLockoutUser(t, db, "nilstore@example.com", "rightpassword")

	body := "email=nilstore@example.com&password=rightpassword&csrfToken=csrf-x&json=true"
	req := httptest.NewRequest("POST", "/api/auth/callback/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "authjs.csrf-token", Value: "csrf-x"})
	rr := httptest.NewRecorder()
	h.CallbackCredentials(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d (body %s), want 500 when issueSession has no store", rr.Code, rr.Body.String())
	}
}

// SignOut with a valid access cookie exercises findSessionID's access-cookie
// branch (the existing TestNextAuth_SignOut sends no cookie, so that branch
// returns "" immediately). Here the session id is recovered and the row is
// revoked.
func TestCovARSignOut_RevokesSessionFromAccessCookie(t *testing.T) {
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	v := newTestJWTValidator(t)
	h := NewNextAuthHandler(db, newTestLogger(), v, store)

	userID := seedTestUser(t, db)
	sess, err := store.Create(context.Background(), userID, "ua", "1.2.3.4", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test", "t@e.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	got, err := store.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.RevokedAt == nil {
		t.Error("SignOut must revoke the session resolved from the access cookie")
	}
}

// clientIP: X-Forwarded-For wins (first hop), and bracketed IPv6 RemoteAddr
// is parsed correctly rather than truncated. Two cheap branch hits the
// existing suite doesn't cover.
func TestCovARClientIP_XFFAndIPv6(t *testing.T) {
	// X-Forwarded-For with multiple hops → first entry, trimmed.
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := clientIP(r1); got != "203.0.113.7" {
		t.Errorf("clientIP(XFF) = %q, want 203.0.113.7", got)
	}

	// No XFF, bracketed IPv6 RemoteAddr → host without brackets/port.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Del("X-Forwarded-For")
	r2.RemoteAddr = "[::1]:8080"
	if got := clientIP(r2); got != "::1" {
		t.Errorf("clientIP(IPv6) = %q, want ::1", got)
	}

	// Malformed RemoteAddr (no port) → returned verbatim, not dropped.
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.Header.Del("X-Forwarded-For")
	r3.RemoteAddr = "weird-no-port"
	if got := clientIP(r3); got != "weird-no-port" {
		t.Errorf("clientIP(malformed) = %q, want verbatim 'weird-no-port'", got)
	}
}
