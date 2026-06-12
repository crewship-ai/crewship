package api

// auth_recovery.go coverage top-up #2 — the DB-failure forks of Forgot
// (lookup error, token swap failures, mailer send failure) and Reset
// (lookup/delete/update failures, race-lost token, orphan token, revoke
// failure), plus the helper edge cases (buildResetURL nil base, empty
// display names).
//
// All tests are prefixed TestCov2AR.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mailer"
)

// cov2ARFailingMailer is configured but always fails to send.
type cov2ARFailingMailer struct{}

func (cov2ARFailingMailer) Send(context.Context, mailer.Message) error {
	return errors.New("cov2: smtp down")
}
func (cov2ARFailingMailer) Configured() bool { return true }

func cov2ARForgotReq(email string) *http.Request {
	return httptest.NewRequest("POST", "/api/v1/auth/forgot", strings.NewReader(`{"email":"`+email+`"}`))
}

func cov2ARResetReq(token, password string) *http.Request {
	return httptest.NewRequest("POST", "/api/v1/auth/reset",
		strings.NewReader(`{"token":"`+token+`","new_password":"`+password+`"}`))
}

// cov2ARSeedToken stores a reset-token row whose token column is the
// hash of rawToken (the same shape Forgot persists).
func cov2ARSeedToken(t *testing.T, db *sql.DB, email, rawToken string, expires time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO verification_tokens (identifier, token, expires, purpose)
		VALUES (?, ?, ?, 'password_reset')`,
		email, hashResetToken(rawToken), expires.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func cov2ARTrigger(t *testing.T, db *sql.DB, name, body string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER ` + name + ` ` + body); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

// --- Forgot: lookup failure still answers the no-enumeration 200 ---

func TestCov2ARForgot_LookupErrorStill200(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE users`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rr := httptest.NewRecorder()
	h.Forgot(rr, cov2ARForgotReq("ghost@example.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no enumeration), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Errorf("body = %s, want generic ok response", rr.Body.String())
	}
}

// --- Forgot: token swap failures still answer 200, no email sent ---

func TestCov2ARForgot_TokenSwapFailuresStill200(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"delete prior tokens blocked", `BEFORE DELETE ON verification_tokens BEGIN SELECT RAISE(ABORT,'blocked'); END`},
		{"insert token blocked", `BEFORE INSERT ON verification_tokens BEGIN SELECT RAISE(ABORT,'blocked'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			seedTestUserWithPassword(t, db, "swap@example.com", "originalpw")
			mail := &stubMailer{configured: true}
			h := newRecoveryHandler(t, db, mail, nil)
			// Pre-existing token so the cleanup DELETE actually visits a
			// row (BEFORE DELETE triggers are per-row).
			cov2ARSeedToken(t, db, "swap@example.com", "prior-token", time.Now().Add(time.Hour))
			cov2ARTrigger(t, db, "cov2ar_swap", tc.trigger)

			rr := httptest.NewRecorder()
			h.Forgot(rr, cov2ARForgotReq("swap@example.com"))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
			}
			if len(mail.sent) != 0 {
				t.Errorf("sent %d emails, want 0 (token swap failed before send)", len(mail.sent))
			}
		})
	}
}

// --- Forgot: mailer send failure is logged, token stays usable ---

func TestCov2ARForgot_SendFailureKeepsToken(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "send@example.com", "originalpw")
	h := newRecoveryHandler(t, db, cov2ARFailingMailer{}, nil)

	rr := httptest.NewRecorder()
	h.Forgot(rr, cov2ARForgotReq("send@example.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	// The token row was deliberately NOT rolled back.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier = 'send@example.com'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("token rows = %d, want 1 (kept after send failure)", n)
	}
}

// --- Reset: token lookup error → 500 ---

func TestCov2ARReset_TokenLookupError500(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	if _, err := db.Exec(`DROP TABLE verification_tokens`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("sometoken", "newpassword1"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// --- Reset: expired token sweep failure is warn-only (still 400) ---

func TestCov2ARReset_ExpiredTokenSweepBlockedStill400(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "exp@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	cov2ARSeedToken(t, db, "exp@example.com", "expired-token", time.Now().Add(-time.Hour))
	cov2ARTrigger(t, db, "cov2ar_sweep", `BEFORE DELETE ON verification_tokens BEGIN SELECT RAISE(ABORT,'blocked'); END`)

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("expired-token", "newpassword1"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (expired), body=%s", rr.Code, rr.Body.String())
	}
}

// --- Reset: token burn delete blocked → 500 ---

func TestCov2ARReset_BurnDeleteBlocked500(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "burn@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	cov2ARSeedToken(t, db, "burn@example.com", "valid-token", time.Now().Add(time.Hour))
	cov2ARTrigger(t, db, "cov2ar_burn", `BEFORE DELETE ON verification_tokens BEGIN SELECT RAISE(ABORT,'blocked'); END`)

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("valid-token", "newpassword1"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// --- Reset: race-lost token (delete affects 0 rows) → 400 ---

func TestCov2ARReset_RaceLostToken400(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "race@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	cov2ARSeedToken(t, db, "race@example.com", "race-token", time.Now().Add(time.Hour))
	// RAISE(IGNORE) makes the burn DELETE silently skip → 0 rows affected.
	cov2ARTrigger(t, db, "cov2ar_race", `BEFORE DELETE ON verification_tokens BEGIN SELECT RAISE(IGNORE); END`)

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("race-token", "newpassword1"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (race lost), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Invalid or expired token") {
		t.Errorf("body = %s, want generic token error", rr.Body.String())
	}
}

// --- Reset: orphan token (no user behind email) → 400 ---

func TestCov2ARReset_OrphanToken400(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	cov2ARSeedToken(t, db, "nobody@example.com", "orphan-token", time.Now().Add(time.Hour))

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("orphan-token", "newpassword1"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no user), body=%s", rr.Code, rr.Body.String())
	}
}

// --- Reset: bcrypt failure on >72-byte password → 500 ---

func TestCov2ARReset_PasswordTooLong500(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "long@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	cov2ARSeedToken(t, db, "long@example.com", "long-token", time.Now().Add(time.Hour))

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("long-token", strings.Repeat("p", 80)))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (bcrypt >72 bytes), body=%s", rr.Code, rr.Body.String())
	}
}

// --- Reset: user update blocked → 500 ---

func TestCov2ARReset_UserUpdateBlocked500(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "upd@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)
	cov2ARSeedToken(t, db, "upd@example.com", "upd-token", time.Now().Add(time.Hour))
	cov2ARTrigger(t, db, "cov2ar_upd", `BEFORE UPDATE ON users BEGIN SELECT RAISE(ABORT,'blocked'); END`)

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("upd-token", "newpassword1"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// --- Reset: session revoke failure → 500 with partial-success message ---

func TestCov2ARReset_RevokeFailure500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUserWithPassword(t, db, "rev@example.com", "originalpw")
	store := sessions.NewDBStore(db)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, store)
	cov2ARSeedToken(t, db, "rev@example.com", "rev-token", time.Now().Add(time.Hour))
	if _, err := db.Exec(`DROP TABLE user_sessions`); err != nil {
		t.Fatalf("drop user_sessions: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Reset(rr, cov2ARResetReq("rev-token", "newpassword1"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (revoke failed), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Password was updated") {
		t.Errorf("body = %s, want partial-success message", rr.Body.String())
	}
	// Password change itself committed before the revoke failure.
	var hashed string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE id = ?`, userID).Scan(&hashed); err != nil {
		t.Fatalf("read: %v", err)
	}
	if hashed == "" {
		t.Error("hashed_password empty")
	}
}

// --- helpers: buildResetURL nil base, display-name fallbacks ---

func TestCov2ARHelpers(t *testing.T) {
	h := &RecoveryHandler{}
	if got := h.buildResetURL("tok"); got != "" {
		t.Errorf("buildResetURL with nil base = %q, want empty", got)
	}
	if html := resetEmailHTML("", "https://x/reset"); !strings.Contains(html, "Hi there,") {
		t.Errorf("HTML fallback name missing: %s", html[:120])
	}
	if txt := resetEmailText("", "https://x/reset"); !strings.Contains(txt, "Hi there,") {
		t.Errorf("text fallback name missing: %s", txt[:60])
	}
}
