package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// auth.go coverage top-up. The handlers live in auth.go: Signup,
// Bootstrap, WsToken, plus the deploy-race window helpers
// (ArmDeployRaceWindow / bootstrapWindowOpen / bootstrapArmingFailed).
// Login / logout are NOT in auth.go — they are CallbackCredentials /
// SignOut in nextauth.go, so they are out of scope for this file and
// covered by lockout_test.go / signout_test.go instead.
//
// These tests target the uncovered error/edge branches the existing
// auth_test.go and auth_extra_test.go leave behind:
//   - ArmDeployRaceWindow: DB-error (fail-closed) and count>0 (consumed)
//   - bootstrapWindowOpen: armed=true + zero deadline (consumed)
//   - Bootstrap: arming-failed → 503, session-pending → 200, count>0 → 410
//   - WsToken: nil validator → 500
//   - setSessionCookies via Signup: nil store rollback already in
//     auth_extra; here we add the HTTPS-secure + happy-cookie variants
//     against the bootstrap path.

// covAuthHandler builds an AuthHandler with a real DB-store and a fresh
// validator, mirroring newAuthHandlerForExtra but returning the db too
// so tests can poke the schema (e.g. drop tables to force arm errors).
func covAuthHandler(t *testing.T, allowSignup bool) (*AuthHandler, *sql.DB, *auth.JWTValidator) {
	t.Helper()
	db := setupTestDB(t)
	v := newTestJWTValidator(t)
	return NewAuthHandler(db, newTestLogger(), v, sessions.NewDBStore(db), allowSignup), db, v
}

// --- ArmDeployRaceWindow ---

// TestCovAuthArmWindow_DBError forces the COUNT(*) probe to fail by
// dropping the users table, then asserts the fail-closed contract:
// arming returns the error, the window is not armed, and
// bootstrapArmingFailed() flips true so Bootstrap can surface a 503.
func TestCovAuthArmWindow_DBError(t *testing.T) {
	t.Parallel()
	h, db, _ := covAuthHandler(t, false)

	if _, err := db.Exec(`DROP TABLE users`); err != nil {
		t.Fatalf("drop users: %v", err)
	}

	err := h.ArmDeployRaceWindow(context.Background(), time.Hour)
	if err == nil {
		t.Fatal("ArmDeployRaceWindow should return error when users table is gone")
	}
	if h.bootstrapWindowOpen() {
		t.Error("window must not be open after arming failure")
	}
	if !h.bootstrapArmingFailed() {
		t.Error("bootstrapArmingFailed() should be true after a DB-error arm")
	}
}

// TestCovAuthArmWindow_AlreadyBootstrapped covers the count>0 branch:
// when a user already exists, arming is a no-op that marks armed=true
// with a zero deadline, so bootstrapWindowOpen() returns false
// ("consumed") rather than the dev "never armed" fall-through.
func TestCovAuthArmWindow_AlreadyBootstrapped(t *testing.T) {
	t.Parallel()
	h, db, _ := covAuthHandler(t, false)
	seedTestUser(t, db)

	if err := h.ArmDeployRaceWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("arm on populated DB should be a no-op, got: %v", err)
	}
	if h.bootstrapWindowOpen() {
		t.Error("window must be closed (consumed) when users already exist")
	}
	if h.bootstrapArmingFailed() {
		t.Error("arming did not fail — bootstrapArmingFailed() should be false")
	}
}

// TestCovAuthArmWindow_NonPositiveDurationDefaults exercises the
// window<=0 clamp to defaultBootstrapWindow on an empty DB — the
// resulting window must be open.
func TestCovAuthArmWindow_NonPositiveDurationDefaults(t *testing.T) {
	t.Parallel()
	h, _, _ := covAuthHandler(t, false)

	if err := h.ArmDeployRaceWindow(context.Background(), 0); err != nil {
		t.Fatalf("arm with zero duration: %v", err)
	}
	if !h.bootstrapWindowOpen() {
		t.Error("window should be open after arming on an empty DB with default duration")
	}
}

// --- bootstrapWindowOpen consumed branch ---

// TestCovAuthBootstrapWindow_ConsumedAfterSuccess drives the
// armed=true + zero-deadline branch directly via closeBootstrapWindow.
func TestCovAuthBootstrapWindow_ConsumedAfterSuccess(t *testing.T) {
	t.Parallel()
	h, _, _ := covAuthHandler(t, false)
	armBootstrapForTest(t, h)
	if !h.bootstrapWindowOpen() {
		t.Fatal("precondition: window should be open right after arming")
	}
	h.closeBootstrapWindow()
	if h.bootstrapWindowOpen() {
		t.Error("window should report closed after closeBootstrapWindow (zero deadline)")
	}
}

// --- Bootstrap ---

// TestCovAuthBootstrap_ArmingFailed503 covers the fail-closed 503: an
// arming failure (DB unreachable at startup) makes Bootstrap refuse
// with 503 rather than falling through to the dev "never armed" allow.
// We DROP and recreate users so the pre-tx existing-user probe (which
// runs first) finds an empty table and falls through to the
// arming-failed check.
func TestCovAuthBootstrap_ArmingFailed503(t *testing.T) {
	t.Parallel()
	h, db, _ := covAuthHandler(t, false)

	// Drop users to force the arm-time COUNT(*) to error, setting
	// the fail-closed armingErr state.
	if _, err := db.Exec(`DROP TABLE users`); err != nil {
		t.Fatalf("drop users: %v", err)
	}
	if err := h.ArmDeployRaceWindow(context.Background(), time.Hour); err == nil {
		t.Fatal("expected arm error after dropping users")
	}
	// Recreate users so Bootstrap's pre-tx existing-user probe succeeds
	// (empty table) and reaches the bootstrapArmingFailed() gate.
	if _, err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		full_name TEXT,
		email TEXT UNIQUE,
		hashed_password TEXT,
		onboarding_completed INTEGER DEFAULT 0,
		created_at TEXT,
		updated_at TEXT
	)`); err != nil {
		t.Fatalf("recreate users: %v", err)
	}

	body := bytes.NewBufferString(`{"full_name":"Admin","email":"a@b.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (arming failed), body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovAuthBootstrap_SessionPending exercises the partial-success
// path: a nil sessions store makes setSessionCookies fail AFTER the
// admin row is committed. Bootstrap must NOT roll back (bootstrap is
// one-shot) and instead returns 200 with session_pending=true so the
// frontend routes the user to /login.
func TestCovAuthBootstrap_SessionPending(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	// nil sessions store → setSessionCookies returns errSessionsStoreUnconfigured.
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), nil, false)
	armBootstrapForTest(t, h)

	body := bytes.NewBufferString(`{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (session pending), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "session_pending") {
		t.Errorf("body should advertise session_pending, got %s", rr.Body.String())
	}
	// The admin row must still be present — bootstrap is one-shot and
	// must not roll back on a cookie-write failure.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='admin@example.com'`).Scan(&n); err != nil {
		t.Fatalf("count admin: %v", err)
	}
	if n != 1 {
		t.Errorf("admin user count = %d, want 1 (must survive cookie failure)", n)
	}
}

// TestCovAuthBootstrap_AlreadyInitialized410 hits the pre-tx
// existing-user probe (users.count>0 → 410) before any window logic.
func TestCovAuthBootstrap_AlreadyInitialized410(t *testing.T) {
	t.Parallel()
	h, db, _ := covAuthHandler(t, false)
	seedTestUser(t, db)

	body := bytes.NewBufferString(`{"full_name":"Admin","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (already initialized), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "log in") {
		t.Errorf("body should point the operator at login, got %s", rr.Body.String())
	}
}

// TestCovAuthBootstrap_NeverArmedFallsThrough documents the dev/test
// fall-through: a handler that was never armed (bootstrapArmed=false,
// no armingErr) does NOT refuse — production always arms, so an
// unarmed state in tests must let Bootstrap succeed.
func TestCovAuthBootstrap_NeverArmedFallsThrough(t *testing.T) {
	t.Parallel()
	h, _, _ := covAuthHandler(t, false)
	// Deliberately do NOT arm.
	if h.bootstrapArmingFailed() {
		t.Fatal("precondition: never-armed handler should not report arming failure")
	}

	body := bytes.NewBufferString(`{"full_name":"Admin User","email":"admin@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", body)
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (never-armed dev fall-through), body=%s", rr.Code, rr.Body.String())
	}
}

// --- Signup ---

// TestCovAuthSignup_DBErrorOnExistingCheck forces the existing-email
// SELECT to fail (table dropped) so Signup takes the
// err != sql.ErrNoRows → 500 branch rather than the conflict or
// happy path.
func TestCovAuthSignup_DBErrorOnExistingCheck(t *testing.T) {
	t.Parallel()
	h, db, _ := covAuthHandler(t, true)
	if _, err := db.Exec(`DROP TABLE users`); err != nil {
		t.Fatalf("drop users: %v", err)
	}

	body := bytes.NewBufferString(`{"full_name":"Alice Doe","email":"alice@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	rr := httptest.NewRecorder()
	h.Signup(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (DB error on existing-email check), body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovAuthSignup_SecureCookieAndBody verifies the full happy path
// returns 201 with the created id/email body AND, under HTTPS, the
// __Secure- prefixed cookies — touching the setSessionCookies success
// branch end-to-end.
func TestCovAuthSignup_SecureCookieAndBody(t *testing.T) {
	t.Parallel()
	h, _, _ := covAuthHandler(t, true)

	body := bytes.NewBufferString(`{"full_name":"Carol Smith","email":"carol@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.Signup(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "carol@example.com") {
		t.Errorf("response body should echo the created email, got %s", rr.Body.String())
	}
	var gotSecure bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "__Secure-authjs.session-token" && c.Value != "" {
			gotSecure = true
		}
	}
	if !gotSecure {
		t.Error("expected __Secure-authjs.session-token under HTTPS")
	}
}

// --- WsToken ---

// TestCovAuthWsToken_NilValidator covers the defensive nil-validator
// guard (Audit H7): an authed user with a misconfigured handler that
// has no validator must get 500, not a panic.
func TestCovAuthWsToken_NilValidator(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	// validator=nil on purpose to hit the fail-closed branch.
	h := NewAuthHandler(db, newTestLogger(), nil, sessions.NewDBStore(db), true)

	req := httptest.NewRequest("POST", "/api/v1/auth/ws-token", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "x@y.com", Name: "n"}))
	rr := httptest.NewRecorder()
	h.WsToken(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (nil validator), body=%s", rr.Code, rr.Body.String())
	}
}
