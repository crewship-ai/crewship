package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mailer"
)

// stubMailer records what was sent so tests can assert without
// hitting the network. Configured() returns true so the /forgot
// happy path actually attempts a send.
type stubMailer struct {
	configured bool
	sent       []mailer.Message
}

func (s *stubMailer) Send(_ context.Context, msg mailer.Message) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *stubMailer) Configured() bool { return s.configured }

func seedTestUserWithPassword(t *testing.T, db *sql.DB, email, password string) string {
	t.Helper()
	id := "test-user-recovery"
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 4) // low cost for tests
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, err = db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES (?, ?, 'Test User', ?)`,
		id, email, string(hashed))
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func newRecoveryHandler(t *testing.T, db *sql.DB, mail mailer.Mailer, store sessions.Store) *RecoveryHandler {
	t.Helper()
	// Set a known CREWSHIP_PUBLIC_URL for the duration of the test so
	// /forgot can build reset links. Tests that exercise the
	// missing-public-url path (refusal-to-send) should override this
	// with t.Setenv after the helper returns.
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.test")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewRecoveryHandler(db, logger, mail, store)
}

func TestForgot_NoEnumeration_UnknownEmail(t *testing.T) {
	db := setupTestDB(t)
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	body := bytes.NewBufferString(`{"email":"nobody@example.com"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/forgot", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no enumeration)", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d emails for unknown user, want 0", len(mail.sent))
	}
}

func TestForgot_SendsEmail_WhenConfigured(t *testing.T) {
	db := setupTestDB(t)
	_ = seedTestUserWithPassword(t, db, "alice@example.com", "originalpw")
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	body := bytes.NewBufferString(`{"email":"alice@example.com"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/forgot", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(mail.sent))
	}
	if mail.sent[0].To != "alice@example.com" {
		t.Errorf("to = %q, want alice@example.com", mail.sent[0].To)
	}
	if !strings.Contains(mail.sent[0].HTML, "/reset-password?token=") {
		t.Errorf("HTML missing reset-password link")
	}

	// Token must be persisted with purpose=password_reset.
	var purpose string
	if err := db.QueryRow(`SELECT purpose FROM verification_tokens WHERE identifier=?`, "alice@example.com").Scan(&purpose); err != nil {
		t.Fatalf("token not stored: %v", err)
	}
	if purpose != "password_reset" {
		t.Errorf("purpose = %q, want password_reset", purpose)
	}
}

func TestForgot_NoSend_WhenMailerDisabled(t *testing.T) {
	db := setupTestDB(t)
	_ = seedTestUserWithPassword(t, db, "bob@example.com", "originalpw")
	mail := &stubMailer{configured: false}
	h := newRecoveryHandler(t, db, mail, nil)

	body := bytes.NewBufferString(`{"email":"bob@example.com"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/forgot", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d, want 0 when mailer disabled", len(mail.sent))
	}
	// No token should be persisted either when mailer is disabled —
	// otherwise we leak the existence of the account via /reset.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier=?`, "bob@example.com").Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Errorf("tokens stored = %d, want 0 when mailer disabled", count)
	}
}

func TestReset_HappyPath_BurnsToken(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUserWithPassword(t, db, "carol@example.com", "originalpw")
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	// First call /forgot to mint a token, then peel the raw token out
	// of the email link the mailer recorded.
	{
		body := bytes.NewBufferString(`{"email":"carol@example.com"}`)
		req := httptest.NewRequest("POST", "/api/v1/auth/forgot", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.Forgot(rr, req)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("forgot did not send email")
	}
	link := mail.sent[0].HTML
	tokenIdx := strings.Index(link, "?token=")
	if tokenIdx == -1 {
		t.Fatalf("no token in email body")
	}
	rest := link[tokenIdx+len("?token="):]
	endIdx := strings.IndexAny(rest, `"<`)
	if endIdx == -1 {
		t.Fatalf("malformed link in email")
	}
	rawToken := rest[:endIdx]

	// Now call /reset with the raw token + a new password.
	body := bytes.NewBufferString(`{"token":"` + rawToken + `","new_password":"newpassword123"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/reset", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Reset(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Verify new password works (and old one doesn't).
	var hashed string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, userID).Scan(&hashed); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte("newpassword123")); err != nil {
		t.Errorf("new password did not verify: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte("originalpw")); err == nil {
		t.Errorf("old password still works")
	}

	// Token must be burned.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier='carol@example.com'`).Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Errorf("tokens remaining = %d, want 0", count)
	}
}

func TestReset_RejectsInvalidToken(t *testing.T) {
	db := setupTestDB(t)
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	body := bytes.NewBufferString(`{"token":"deadbeef","new_password":"newpassword123"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/reset", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Reset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestReset_RejectsExpiredToken(t *testing.T) {
	db := setupTestDB(t)
	_ = seedTestUserWithPassword(t, db, "dave@example.com", "originalpw")
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	// Manually insert an already-expired token. Use a fixed hash so
	// the matching raw token is predictable.
	rawToken := "rawtoken-for-test"
	tokenHash := hashResetToken(rawToken)
	expiredAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO verification_tokens (identifier, token, expires, purpose) VALUES (?, ?, ?, 'password_reset')`,
		"dave@example.com", tokenHash, expiredAt); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}

	body := bytes.NewBufferString(`{"token":"` + rawToken + `","new_password":"newpassword123"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/reset", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Reset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (expired)", rr.Code)
	}

	// Body should not leak whether the email exists.
	var resp map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "Invalid or expired token" {
		t.Errorf("error = %q, want %q", resp["error"], "Invalid or expired token")
	}
}

func TestReset_RejectsShortPassword(t *testing.T) {
	db := setupTestDB(t)
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)

	body := bytes.NewBufferString(`{"token":"x","new_password":"short"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/reset", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Reset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestForgot_RefusesWhenPublicURLMissing locks in the contract that
// /forgot does not mint a token when CREWSHIP_PUBLIC_URL is unset
// even if a mailer is configured — without a server-controlled origin
// the only way to build a reset URL is r.Host, which an attacker can
// poison via a forged Host header to deliver a working link onto
// their own domain.
func TestForgot_RefusesWhenPublicURLMissing(t *testing.T) {
	db := setupTestDB(t)
	_ = seedTestUserWithPassword(t, db, "eve@example.com", "originalpw")
	mail := &stubMailer{configured: true}
	// Use NewRecoveryHandler directly so we can pass an empty env, not
	// the helper which sets CREWSHIP_PUBLIC_URL by default.
	t.Setenv("CREWSHIP_PUBLIC_URL", "")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewRecoveryHandler(db, logger, mail, nil)

	body := bytes.NewBufferString(`{"email":"eve@example.com"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/forgot", body)
	req.Header.Set("Content-Type", "application/json")
	// Attacker-supplied Host header — must not end up in the email body.
	req.Host = "evil.com"
	rr := httptest.NewRecorder()
	h.Forgot(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no enumeration)", rr.Code)
	}
	if len(mail.sent) != 0 {
		t.Fatalf("sent %d emails; must be 0 when public URL is unset", len(mail.sent))
	}
	// Token must NOT be persisted — otherwise /reset still works and we
	// leak the existence of the account.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_tokens WHERE identifier=?`, "eve@example.com").Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Errorf("tokens stored = %d, want 0 when public URL missing", count)
	}
}

// TestReset_RaceFreeSingleUse simulates two concurrent /reset calls
// with the same token. The first must succeed; the second must see
// the token already burned and return 400 without touching the
// password again.
func TestReset_RaceFreeSingleUse(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUserWithPassword(t, db, "frank@example.com", "originalpw")
	mail := &stubMailer{configured: true}
	h := newRecoveryHandler(t, db, mail, nil)

	// Mint a token directly so we have a known raw value.
	rawToken := "frank-raw-test-token-1234567890abcdef"
	tokenHash := hashResetToken(rawToken)
	expires := time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO verification_tokens (identifier, token, expires, purpose) VALUES (?, ?, ?, 'password_reset')`,
		"frank@example.com", tokenHash, expires); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	// First /reset — should succeed.
	body1 := bytes.NewBufferString(`{"token":"` + rawToken + `","new_password":"firstpass123"}`)
	r1 := httptest.NewRequest("POST", "/api/v1/auth/reset", body1)
	r1.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	h.Reset(rr1, r1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first reset status = %d, body=%s", rr1.Code, rr1.Body.String())
	}

	// Second /reset with the same token — must be rejected.
	body2 := bytes.NewBufferString(`{"token":"` + rawToken + `","new_password":"secondpass123"}`)
	r2 := httptest.NewRequest("POST", "/api/v1/auth/reset", body2)
	r2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	h.Reset(rr2, r2)
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("second reset status = %d, want 400 (token already burned)", rr2.Code)
	}

	// Password must match the FIRST reset, not the second — the loser
	// must not have overwritten the winner's password.
	var hashed string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, userID).Scan(&hashed); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte("firstpass123")); err != nil {
		t.Errorf("first password did not stick: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte("secondpass123")); err == nil {
		t.Errorf("second (rejected) password somehow committed — race protection broken")
	}
}
