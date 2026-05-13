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
