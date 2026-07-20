package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mailer"
)

// Signup must not tell an unauthenticated caller whether an address
// already has an account. The two directions are pinned against each
// other: the response of a signup that CREATED an account and the
// response of a signup that hit an existing address have to be
// byte-identical (status, body, headers) or the endpoint is an
// enumeration oracle regardless of how generic the message reads.

func signupForEnumTest(t *testing.T, h *AuthHandler, email string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"full_name":"Enum Probe","email":"` + email + `","password":"longenough1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/signup", body)
	rr := httptest.NewRecorder()
	h.Signup(rr, req)
	return rr
}

func TestSignup_NewEmail_CreatesAccountAndReturnsGenericAccepted(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)

	rr := signupForEnumTest(t, h, "fresh@example.com")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	if got["ok"] != true {
		t.Errorf("body = %v, want ok:true", got)
	}
	// The account id must NOT be echoed: it exists only for real
	// signups, so its presence is itself the oracle.
	if _, leaked := got["id"]; leaked {
		t.Errorf("response leaks the created account id: %v", got)
	}

	var users, workspaces, members int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'fresh@example.com'`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Fatalf("users = %d, want 1 — a genuinely new signup must still get an account", users)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaces != 1 {
		t.Errorf("workspaces = %d, want 1", workspaces)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members`).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if members != 1 {
		t.Errorf("workspace_members = %d, want 1", members)
	}
}

func TestSignup_ExistingEmail_IsIndistinguishableFromNewEmail(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)

	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'taken@example.com', 'Existing', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	taken := signupForEnumTest(t, h, "taken@example.com")
	fresh := signupForEnumTest(t, h, "unused@example.com")

	if taken.Code != fresh.Code {
		t.Errorf("status: existing=%d new=%d — must match", taken.Code, fresh.Code)
	}
	if taken.Body.String() != fresh.Body.String() {
		t.Errorf("body differs:\n existing=%s\n new     =%s", taken.Body.String(), fresh.Body.String())
	}
	// Set-Cookie is part of the response too: a session handed out only
	// on the created path leaks existence just as loudly as a 409.
	if got, want := len(taken.Result().Cookies()), len(fresh.Result().Cookies()); got != want {
		t.Errorf("cookie count: existing=%d new=%d — must match", got, want)
	}
	for _, c := range taken.Result().Cookies() {
		if strings.Contains(c.Name, "session-token") && c.Value != "" {
			t.Errorf("existing-email signup handed out a session cookie %q", c.Name)
		}
	}

	// The collision must be a no-op on the database.
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'taken@example.com'`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("users with taken@example.com = %d, want 1 (no duplicate)", users)
	}
	var members int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id = 'u1'`).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if members != 0 {
		t.Errorf("collision created %d membership rows for the existing user, want 0", members)
	}
}

// The CREWSHIP_ALLOW_SIGNUP=false gate is upstream of the whole
// de-enumeration change and must keep answering 403 — self-hosted
// installs rely on that exact status to hide the signup UI.
func TestSignup_DisabledGateUnchangedByDeEnumeration(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), false)

	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'taken@example.com', 'Existing', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, email := range []string{"taken@example.com", "unused@example.com"} {
		rr := signupForEnumTest(t, h, email)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", email, rr.Code)
		}
	}
}

// slowSignupMailer blocks in Send for a fixed delay. A signup that
// dispatches the "you already have an account" notice inline shows
// that delay in its response latency — which re-opens the enumeration
// oracle in the time domain no matter how identical the bytes are.
type slowSignupMailer struct {
	delay time.Duration
	mu    sync.Mutex
	sent  []mailer.Message
}

func (m *slowSignupMailer) Send(_ context.Context, msg mailer.Message) error {
	time.Sleep(m.delay)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *slowSignupMailer) Configured() bool { return true }

func (m *slowSignupMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// The collision notice is an outbound HTTPS POST to a third party. Held
// on the request path it makes the taken-address response measurably
// slower than the fresh-address one on every instance that has a mailer
// configured — the loudest possible signal, and one the identical body
// cannot mask. The send must be detached from the response.
func TestSignup_ExistingEmail_MailerDoesNotDelayTheResponse(t *testing.T) {
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	mail := &slowSignupMailer{delay: 1500 * time.Millisecond}
	h.mail = mail

	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'taken@example.com', 'Existing', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// The equaliser hash is a sync.OnceValue: whoever calls it first
	// pays an extra cost-12 generate. NewRouter warms it in a goroutine
	// at server construction (see lockout.go) — this test builds the
	// handler directly, so warm it here or the very first collision
	// measures two bcrypts against the fresh path's one.
	dummyBcryptHash()

	start := time.Now()
	signupForEnumTest(t, h, "unused@example.com")
	fresh := time.Since(start)

	start = time.Now()
	rr := signupForEnumTest(t, h, "taken@example.com")
	taken := time.Since(start)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}
	// Half the mailer delay is a wide margin: the two paths differ only
	// by bcrypt-generate vs bcrypt-compare plus a small transaction, so
	// anything approaching 750ms of extra latency can only be the send.
	if margin := mail.delay / 2; taken > fresh+margin {
		t.Fatalf("taken-address signup took %v vs %v for a fresh address (margin %v) — the mailer is on the request path",
			taken, fresh, margin)
	}

	// Detached, not dropped: the notice still has to reach the owner.
	h.WaitForPendingMail()
	if got := mail.count(); got != 1 {
		t.Fatalf("sent %d notices, want 1", got)
	}
	if to := mail.sent[0].To; to != "taken@example.com" {
		t.Errorf("notice went to %q", to)
	}
}
