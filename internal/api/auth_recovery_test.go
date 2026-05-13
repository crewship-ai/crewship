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
	"sync"
	"sync/atomic"
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

// TestReset_RaceFreeSingleUse fires two /reset calls with the same
// token *concurrently*. Exactly one must win (200), exactly one must
// lose (400), and the surviving password must be the winner's — never
// the loser's, never a mix. A sequential version of this test would
// still pass if the handler read the token twice before either DELETE
// landed, which is exactly the bug the race-fix protects against.
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

	// Two goroutines hammer Reset at the same instant. The barrier
	// channel forces both calls to be in flight before either DELETE
	// can take effect — that's the only way to actually trip a
	// real race in the handler.
	var (
		barrier    = make(chan struct{})
		wg         sync.WaitGroup
		okCount    atomic.Int32
		badCount   atomic.Int32
		winningPwd atomic.Value // string — password sent by the winner
	)
	wg.Add(2)
	attempts := []string{"firstpass123", "secondpass123"}
	for _, pwd := range attempts {
		pwd := pwd
		go func() {
			defer wg.Done()
			body := bytes.NewBufferString(`{"token":"` + rawToken + `","new_password":"` + pwd + `"}`)
			req := httptest.NewRequest("POST", "/api/v1/auth/reset", body)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			<-barrier
			h.Reset(rr, req)
			switch rr.Code {
			case http.StatusOK:
				okCount.Add(1)
				winningPwd.Store(pwd)
			case http.StatusBadRequest:
				badCount.Add(1)
			}
		}()
	}
	close(barrier)
	wg.Wait()

	if got := okCount.Load(); got != 1 {
		t.Fatalf("got %d successful resets, want exactly 1", got)
	}
	if got := badCount.Load(); got != 1 {
		t.Fatalf("got %d rejected resets, want exactly 1", got)
	}

	// Whichever password won should be the one that committed. The
	// loser's password must NOT verify — that would mean both writes
	// hit the row, which is the bug the race protection prevents.
	var hashed string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, userID).Scan(&hashed); err != nil {
		t.Fatalf("read user: %v", err)
	}
	winner, _ := winningPwd.Load().(string)
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(winner)); err != nil {
		t.Errorf("winner password (%q) did not stick: %v", winner, err)
	}
	for _, pwd := range attempts {
		if pwd == winner {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(pwd)); err == nil {
			t.Errorf("loser password (%q) somehow committed — race protection broken", pwd)
		}
	}
}
