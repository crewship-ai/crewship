package api

import (
	"bytes"
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
)

func newPairHandler(t *testing.T, db *sql.DB) *CliPairHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewCliPairHandler(db, logger)
}

func TestPairStart_IssuesCodeForAuthedUser(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := newPairHandler(t, db)

	req := httptest.NewRequest("POST", "/api/v1/auth/pair/start", bytes.NewBufferString(`{"adapter_hint":"CLAUDE_CODE"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()

	h.Start(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var resp pairStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 4-4 split, 9 chars total including dash.
	if !strings.Contains(resp.Code, "-") || len(resp.Code) != 9 {
		t.Errorf("code = %q, want XXXX-XXXX shape", resp.Code)
	}
	// Code persisted with status='pending' for the right user.
	var status, storedUser string
	if err := db.QueryRow(`SELECT status, user_id FROM cli_pairings WHERE code=?`, resp.Code).Scan(&status, &storedUser); err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending", status)
	}
	if storedUser != userID {
		t.Errorf("user = %q, want %q", storedUser, userID)
	}
}

func TestPairStart_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := newPairHandler(t, db)

	req := httptest.NewRequest("POST", "/api/v1/auth/pair/start", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()

	h.Start(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestPairRedeem_SingleUse(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := newPairHandler(t, db)

	// Issue a code via Start so the test exercises the real generator
	// path (not a hand-rolled SQL insert).
	startReq := httptest.NewRequest("POST", "/api/v1/auth/pair/start", bytes.NewBufferString(`{}`))
	startReq = startReq.WithContext(withUser(startReq.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rrStart := httptest.NewRecorder()
	h.Start(rrStart, startReq)
	var start pairStartResponse
	_ = json.Unmarshal(rrStart.Body.Bytes(), &start)
	if start.Code == "" {
		t.Fatalf("no code from /start")
	}

	// Fire two redeems concurrently to actually exercise the
	// double-redeem race. Both goroutines wait on a barrier so they
	// hit Redeem at essentially the same instant — the sequential
	// version of this test passed even when the code path was racy.
	var (
		barrier   = make(chan struct{})
		wg        sync.WaitGroup
		okCount   atomic.Int32
		badCount  atomic.Int32
		seenToken atomic.Value // string — the cli_token from the winning call
	)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			body := bytes.NewBufferString(`{"code":"` + start.Code + `","adapter_hint":"CLAUDE_CODE"}`)
			req := httptest.NewRequest("POST", "/api/v1/auth/pair/redeem", body)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			<-barrier
			h.Redeem(rr, req)
			switch rr.Code {
			case http.StatusOK:
				okCount.Add(1)
				var redeem pairRedeemResponse
				_ = json.Unmarshal(rr.Body.Bytes(), &redeem)
				seenToken.Store(redeem.CliToken)
			case http.StatusBadRequest:
				badCount.Add(1)
			}
		}()
	}
	close(barrier)
	wg.Wait()

	if got := okCount.Load(); got != 1 {
		t.Fatalf("got %d successful redeems, want exactly 1 (race protection broken)", got)
	}
	if got := badCount.Load(); got != 1 {
		t.Fatalf("got %d rejected redeems, want exactly 1", got)
	}

	if tok, _ := seenToken.Load().(string); !strings.HasPrefix(tok, cliTokenPrefix) {
		t.Errorf("winning cli_token = %q, want %s* prefix", tok, cliTokenPrefix)
	}

	// cli_tokens table should have exactly one row for this user.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id=?`, userID).Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 1 {
		t.Errorf("cli_tokens rows = %d, want 1", count)
	}
}

func TestPairRedeem_RejectsExpired(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := newPairHandler(t, db)

	// Seed a manually-expired pairing — bypasses /start to avoid
	// flaky clock-dependent assertions.
	expiredAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	createdAt := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO cli_pairings (id, user_id, code, status, created_at, expires_at) VALUES ('p1', ?, 'TEST-EXPI', 'pending', ?, ?)`,
		userID, createdAt, expiredAt)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := bytes.NewBufferString(`{"code":"TEST-EXPI"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/pair/redeem", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (expired)", rr.Code)
	}
}

func TestPairPoll_DoesNotLeakOtherUserCodes(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	// Seed a second user. seedTestUser uses a fixed ID so we need a
	// manual insert for the second one.
	intruderID := "intruder-id"
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES (?, 'intruder@example.com')`, intruderID); err != nil {
		t.Fatalf("intruder: %v", err)
	}

	// owner issues a code.
	startReq := httptest.NewRequest("POST", "/api/v1/auth/pair/start", bytes.NewBufferString(`{}`))
	startReq = startReq.WithContext(withUser(startReq.Context(), &AuthUser{ID: owner, Email: "test@example.com"}))
	rrStart := httptest.NewRecorder()
	h := newPairHandler(t, db)
	h.Start(rrStart, startReq)
	var start pairStartResponse
	_ = json.Unmarshal(rrStart.Body.Bytes(), &start)

	// intruder polls with owner's code — should see 'expired', not 'pending'.
	pollReq := httptest.NewRequest("GET", "/api/v1/auth/pair/poll?code="+start.Code, nil)
	pollReq = pollReq.WithContext(withUser(pollReq.Context(), &AuthUser{ID: intruderID, Email: "intruder@example.com"}))
	rrPoll := httptest.NewRecorder()
	h.Poll(rrPoll, pollReq)
	if rrPoll.Code != http.StatusOK {
		t.Fatalf("poll status = %d", rrPoll.Code)
	}
	var pollResp pairPollResponse
	_ = json.Unmarshal(rrPoll.Body.Bytes(), &pollResp)
	// The contract for a foreign code is exactly "expired" — checking
	// !="pending" would let a regression that returned "consumed" or
	// some other wrong sentinel slip through. We want to lock in the
	// concrete value the Poll handler ships.
	if pollResp.Status != "expired" {
		t.Errorf("foreign-user poll status = %q, want %q", pollResp.Status, "expired")
	}
}

func TestNormalizePairingCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"K3F9-X2NM", "K3F9-X2NM"},
		{"k3f9-x2nm", "K3F9-X2NM"},
		{"K3F9X2NM", "K3F9-X2NM"},
		{"k3f9 x2nm", "K3F9-X2NM"},
		{" K3F9-X2NM ", "K3F9-X2NM"},
		{"K3F9X2", ""}, // too short
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizePairingCode(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeAdapterHint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CLAUDE_CODE", "CLAUDE_CODE"},
		{"claude_code", "CLAUDE_CODE"},
		{"GEMINI_CLI", "GEMINI_CLI"},
		{"", ""},
		{"hax;DROP TABLE", ""},
		{"injection\nattack", ""},
	}
	for _, c := range cases {
		if got := sanitizeAdapterHint(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
