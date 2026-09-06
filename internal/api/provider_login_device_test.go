package api

// Device-code sign-in (#2428, PRD provider-logins §10.3). Every test here
// talks to codexauthtest's fake issuer; nothing dials auth.openai.com.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/codexauth/codexauthtest"
	"github.com/crewship-ai/crewship/internal/encryption"
)

type deviceLoginFixture struct {
	issuer *codexauthtest.FakeIssuer
	h      *ProviderLoginHandler
	userID string
	wsID   string
}

func newDeviceLoginFixture(t *testing.T) deviceLoginFixture {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	issuer := codexauthtest.NewFakeIssuer(t, "plus", "jana@unify.cz")
	creds := NewCredentialHandler(db, quietLogger())
	h := NewProviderLoginHandler(db, quietLogger(), creds, codexauth.NewDeviceClient(issuer.URL(), nil))
	h.pollInterval = 10 * time.Millisecond
	t.Cleanup(h.Stop)
	return deviceLoginFixture{issuer: issuer, h: h, userID: userID, wsID: wsID}
}

func (f deviceLoginFixture) start(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-logins/device", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: f.userID, Email: "test@example.com"})
	ctx = withWorkspace(ctx, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.Start(rr, req.WithContext(ctx))
	return rr
}

func (f deviceLoginFixture) status(t *testing.T, deviceID, asUser string) deviceLoginStatusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/provider-logins/device/"+deviceID, nil)
	req.SetPathValue("deviceId", deviceID)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: asUser}))
	rr := httptest.NewRecorder()
	f.h.Status(rr, req)
	if rr.Code == http.StatusNotFound {
		return deviceLoginStatusResponse{Status: "404"}
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp deviceLoginStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// waitFor polls Status until it leaves pending or the deadline passes.
func (f deviceLoginFixture) waitFor(t *testing.T, deviceID string) deviceLoginStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp := f.status(t, deviceID, f.userID); resp.Status != deviceLoginStatusPending {
			return resp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("sign-in stayed pending")
	return deviceLoginStatusResponse{}
}

func TestDeviceLogin_StartReturnsCodeAndPersists(t *testing.T) {
	f := newDeviceLoginFixture(t)
	rr := f.start(t, `{"provider":"openai"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp deviceLoginStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DeviceID == "" || resp.UserCode != "ABCD-EFGHJ" || resp.IntervalS != 1 ||
		resp.VerificationURL != f.issuer.URL()+"/codex/device" {
		t.Errorf("start response = %+v", resp)
	}
	expires, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil || time.Until(expires) < 14*time.Minute || time.Until(expires) > 16*time.Minute {
		t.Errorf("expires_at = %q, want ~15 min out", resp.ExpiresAt)
	}
	// Persisted, pending, owned by the caller, no token anywhere in the row.
	var status, owner, ws string
	if err := f.h.db.QueryRow(`SELECT status, user_id, workspace_id FROM provider_device_logins WHERE id = ?`, resp.DeviceID).
		Scan(&status, &owner, &ws); err != nil {
		t.Fatalf("row: %v", err)
	}
	if status != "pending" || owner != f.userID || ws != f.wsID {
		t.Errorf("row = %s/%s/%s", status, owner, ws)
	}
	if got := f.status(t, resp.DeviceID, f.userID); got.Status != "pending" || got.UserCode != "ABCD-EFGHJ" {
		t.Errorf("status = %+v", got)
	}
}

func TestDeviceLogin_StartValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
		msg  string
	}{
		{"unknown provider", `{"provider":"ANTHROPIC"}`, http.StatusBadRequest, "OPENAI only"},
		{"missing provider", `{}`, http.StatusBadRequest, "OPENAI only"},
		{"api_key mode", `{"provider":"OPENAI","mode":"api_key"}`, http.StatusBadRequest, "subscription"},
		{"bad json", `{`, http.StatusBadRequest, "Invalid JSON"},
		{"subscription mode ok", `{"provider":"OPENAI","mode":"subscription"}`, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeviceLoginFixture(t)
			rr := f.start(t, tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d — body=%s", rr.Code, tc.want, rr.Body.String())
			}
			if tc.msg != "" && !strings.Contains(rr.Body.String(), tc.msg) {
				t.Errorf("body = %s, want %q", rr.Body.String(), tc.msg)
			}
		})
	}
}

func TestDeviceLogin_StartUnauthorizedAndForbidden(t *testing.T) {
	f := newDeviceLoginFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-logins/device", bytes.NewBufferString(`{"provider":"OPENAI"}`))
	rr := httptest.NewRecorder()
	f.h.Start(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no user: status = %d, want 401", rr.Code)
	}

	// A plain MEMBER without credential.create cannot start one — the
	// same gate as POST /api/v1/credentials. The gate reads the
	// membership row, so the member has to be a real one.
	if _, err := f.h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('member', 'member@example.com', 'M')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-member', ?, 'member', 'MEMBER')`, f.wsID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/provider-logins/device", bytes.NewBufferString(`{"provider":"OPENAI"}`))
	ctx := withUser(req.Context(), &AuthUser{ID: "member"})
	ctx = withWorkspace(ctx, f.wsID, "MEMBER")
	rr = httptest.NewRecorder()
	f.h.Start(rr, req.WithContext(ctx))
	if rr.Code != http.StatusForbidden {
		t.Errorf("member: status = %d, want 403 — body=%s", rr.Code, rr.Body.String())
	}
	if start, _, _ := f.issuer.Calls(); start != 0 {
		t.Errorf("the provider was asked for a code %d times before the gate; want 0", start)
	}
}

func TestDeviceLogin_RateLimitedPerUser(t *testing.T) {
	f := newDeviceLoginFixture(t)
	for i := 0; i < deviceLoginStartsPer10Min; i++ {
		if rr := f.start(t, `{"provider":"OPENAI"}`); rr.Code != http.StatusCreated {
			t.Fatalf("start %d: status = %d — body=%s", i, rr.Code, rr.Body.String())
		}
	}
	rr := f.start(t, `{"provider":"OPENAI"}`)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d (Retry-After=%q), want 429 with Retry-After", rr.Code, rr.Header().Get("Retry-After"))
	}
	if start, _, _ := f.issuer.Calls(); start != deviceLoginStartsPer10Min {
		t.Errorf("provider start calls = %d, want %d (the refused start never reached it)", start, deviceLoginStartsPer10Min)
	}
	// Another user has their own budget.
	if _, err := f.h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u2', 'u2@example.com', 'U2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m2', ?, 'u2', 'OWNER')`, f.wsID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-logins/device", bytes.NewBufferString(`{"provider":"OPENAI"}`))
	ctx := withUser(req.Context(), &AuthUser{ID: "u2"})
	ctx = withWorkspace(ctx, f.wsID, "OWNER")
	rr = httptest.NewRecorder()
	f.h.Start(rr, req.WithContext(ctx))
	if rr.Code != http.StatusCreated {
		t.Errorf("second user: status = %d, want 201", rr.Code)
	}
}

func TestDeviceLogin_ProviderStartFailure(t *testing.T) {
	f := newDeviceLoginFixture(t)
	f.issuer.SetStartStatus(http.StatusNotFound)
	rr := f.start(t, `{"provider":"OPENAI"}`)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "auth.json") {
		t.Fatalf("status = %d, body=%s; want 502 pointing at the paste path", rr.Code, rr.Body.String())
	}
	var n int
	_ = f.h.db.QueryRow(`SELECT COUNT(*) FROM provider_device_logins`).Scan(&n)
	if n != 0 {
		t.Errorf("a failed start left %d rows", n)
	}
}

func TestDeviceLogin_CompletesIntoCredential(t *testing.T) {
	f := newDeviceLoginFixture(t)
	rr := f.start(t, `{"provider":"OPENAI"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var started deviceLoginStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &started)

	// Still pending while the person has not finished the browser step —
	// and the poller has been asking.
	time.Sleep(50 * time.Millisecond)
	if got := f.status(t, started.DeviceID, f.userID); got.Status != "pending" {
		t.Fatalf("status before authorise = %+v", got)
	}
	if _, polls, exchanges := f.issuer.Calls(); polls == 0 || exchanges != 0 {
		t.Errorf("polls=%d exchanges=%d before authorise; want polling, no exchange", polls, exchanges)
	}

	f.issuer.Authorize()
	done := f.waitFor(t, started.DeviceID)
	if done.Status != "complete" || done.CredentialID == nil || *done.CredentialID == "" {
		t.Fatalf("final status = %+v", done)
	}
	if _, _, exchanges := f.issuer.Calls(); exchanges != 1 {
		t.Errorf("exchange calls = %d, want exactly 1", exchanges)
	}

	// The credential is the row a pasted login would be: the caller's
	// workspace, the caller as creator, AI_CLI_TOKEN/OPENAI, named by plan
	// and email, the whole auth.json (real refresh token included) as the
	// encrypted value — the run-time renderer is what strips it.
	var name, typ, provider, ws, createdBy, enc, status string
	var accountEmail *string
	if err := f.h.db.QueryRow(`SELECT name, type, provider, workspace_id, created_by, encrypted_value, status, account_email
		FROM credentials WHERE id = ?`, *done.CredentialID).
		Scan(&name, &typ, &provider, &ws, &createdBy, &enc, &status, &accountEmail); err != nil {
		t.Fatalf("credential row: %v", err)
	}
	if name != "ChatGPT Plus · jana@unify.cz" || typ != "AI_CLI_TOKEN" || provider != "OPENAI" ||
		ws != f.wsID || createdBy != f.userID || status != "ACTIVE" || accountEmail == nil || *accountEmail != "jana@unify.cz" {
		t.Errorf("credential = name=%q type=%q provider=%q ws=%q by=%q status=%q email=%v", name, typ, provider, ws, createdBy, status, accountEmail)
	}
	plain, err := encryption.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	file, err := codexauth.Parse(plain)
	if err != nil {
		t.Fatalf("stored value is not a Codex login: %v", err)
	}
	if file.Tokens.RefreshToken != "rt.FAKE-REFRESH-SECRET" || file.Tokens.AccountID != "acct-device-1" || file.AuthMode != "chatgpt" {
		t.Errorf("stored login = %+v", file.Tokens)
	}
	// The CREATED audit event rode the create path too.
	var audits int
	_ = f.h.db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = ?`, *done.CredentialID).Scan(&audits)
	if audits == 0 {
		t.Errorf("no credential_audit row for the created credential")
	}
	// The device row is closed and points at the credential.
	var rowStatus, rowCred string
	_ = f.h.db.QueryRow(`SELECT status, COALESCE(credential_id, '') FROM provider_device_logins WHERE id = ?`, started.DeviceID).Scan(&rowStatus, &rowCred)
	if rowStatus != "complete" || rowCred != *done.CredentialID {
		t.Errorf("device row = %s/%s", rowStatus, rowCred)
	}
}

// A second sign-in with the same account must not collide on the name.
func TestDeviceLogin_SecondLoginSameAccountKeepsBoth(t *testing.T) {
	f := newDeviceLoginFixture(t)
	f.issuer.Authorize()
	var ids []string
	for i := 0; i < 2; i++ {
		rr := f.start(t, `{"provider":"OPENAI"}`)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var started deviceLoginStartResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &started)
		done := f.waitFor(t, started.DeviceID)
		if done.Status != "complete" || done.CredentialID == nil {
			t.Fatalf("sign-in %d: %+v", i, done)
		}
		ids = append(ids, *done.CredentialID)
	}
	if ids[0] == ids[1] {
		t.Fatalf("both sign-ins point at the same credential %s", ids[0])
	}
	var names []string
	rows, _ := f.h.db.Query(`SELECT name FROM credentials WHERE id IN (?, ?) ORDER BY created_at`, ids[0], ids[1])
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	if len(names) != 2 || !strings.HasPrefix(names[1], "ChatGPT Plus · jana@unify.cz · ") {
		t.Errorf("names = %v; the second must be disambiguated, not refused", names)
	}
}

func TestDeviceLogin_DeniedByProvider(t *testing.T) {
	f := newDeviceLoginFixture(t)
	f.issuer.SetPollStatus(http.StatusBadRequest, "")
	rr := f.start(t, `{"provider":"OPENAI"}`)
	var started deviceLoginStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &started)
	done := f.waitFor(t, started.DeviceID)
	if done.Status != "denied" || done.Error == nil || done.CredentialID != nil {
		t.Fatalf("final status = %+v, want denied with an error and no credential", done)
	}
	var n int
	_ = f.h.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE deleted_at IS NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("a denied sign-in created %d credentials", n)
	}
}

// A 429 from the provider slows the poller down rather than ending the
// flow; once the person authorises, it still completes.
func TestDeviceLogin_SlowDownIsHonoured(t *testing.T) {
	f := newDeviceLoginFixture(t)
	f.issuer.SetPollStatus(http.StatusTooManyRequests, "1")
	rr := f.start(t, `{"provider":"OPENAI"}`)
	var started deviceLoginStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &started)

	time.Sleep(150 * time.Millisecond)
	_, polls, _ := f.issuer.Calls()
	if polls > 2 {
		t.Fatalf("polls = %d within 150 ms under a Retry-After of 1 s; the back-off was not honoured", polls)
	}
	if got := f.status(t, started.DeviceID, f.userID); got.Status != "pending" {
		t.Fatalf("status = %+v, want still pending", got)
	}
	f.issuer.Authorize()
	if done := f.waitFor(t, started.DeviceID); done.Status != "complete" {
		t.Fatalf("final status = %+v", done)
	}
}

func TestDeviceLogin_ExpiresWhenNeverEntered(t *testing.T) {
	f := newDeviceLoginFixture(t)
	rr := f.start(t, `{"provider":"OPENAI"}`)
	var started deviceLoginStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &started)
	// Move the clock past the provider's window.
	f.h.now = func() time.Time { return time.Now().UTC().Add(codexauth.DeviceFlowTimeout + time.Minute) }
	done := f.waitFor(t, started.DeviceID)
	if done.Status != "expired" || done.Error == nil {
		t.Fatalf("final status = %+v, want expired", done)
	}
}

func TestDeviceLogin_StatusIsOwnerOnly(t *testing.T) {
	f := newDeviceLoginFixture(t)
	rr := f.start(t, `{"provider":"OPENAI"}`)
	var started deviceLoginStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &started)
	if got := f.status(t, started.DeviceID, "someone-else"); got.Status != "404" {
		t.Errorf("another user's read = %+v, want 404", got)
	}
	if got := f.status(t, "no-such-id", f.userID); got.Status != "404" {
		t.Errorf("unknown id = %+v, want 404", got)
	}
}

// A restart in the middle of a sign-in: the row is pending in the DB and
// no poller runs. ResumePending on a fresh handler picks it up and finishes
// it; a row whose window closed while the server was down is expired
// without a single call to the provider.
func TestDeviceLogin_ResumePendingAfterRestart(t *testing.T) {
	f := newDeviceLoginFixture(t)
	rr := f.start(t, `{"provider":"OPENAI"}`)
	var started deviceLoginStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &started)
	f.h.Stop() // the "old process" goes away mid-flow

	// A stale row from an earlier life, past its window.
	if _, err := f.h.db.Exec(`INSERT INTO provider_device_logins
		(id, workspace_id, user_id, provider, mode, device_auth_id, user_code, verification_url, interval_s, status, created_at, expires_at)
		VALUES ('stale', ?, ?, 'OPENAI', 'subscription', 'dev-auth-123', 'ABCD-EFGHJ', 'x', 1, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:15:00Z')`,
		f.wsID, f.userID); err != nil {
		t.Fatal(err)
	}

	f.issuer.Authorize()
	fresh := NewProviderLoginHandler(f.h.db, quietLogger(), f.h.creds, codexauth.NewDeviceClient(f.issuer.URL(), nil))
	fresh.pollInterval = 10 * time.Millisecond
	t.Cleanup(fresh.Stop)
	fresh.ResumePending(context.Background())

	f2 := deviceLoginFixture{issuer: f.issuer, h: fresh, userID: f.userID, wsID: f.wsID}
	done := f2.waitFor(t, started.DeviceID)
	if done.Status != "complete" || done.CredentialID == nil {
		t.Fatalf("resumed sign-in = %+v", done)
	}
	if stale := f2.status(t, "stale", f.userID); stale.Status != "expired" {
		t.Errorf("stale row = %+v, want expired", stale)
	}
	// Resuming again must not start a second poller for a finished row.
	fresh.ResumePending(context.Background())
	if _, _, exchanges := f.issuer.Calls(); exchanges != 1 {
		t.Errorf("exchange calls = %d, want 1", exchanges)
	}
}

func TestDeviceLoginCredentialName(t *testing.T) {
	cases := []struct{ plan, email, want string }{
		{"ChatGPT Plus", "jana@unify.cz", "ChatGPT Plus · jana@unify.cz"},
		{"ChatGPT", "", "ChatGPT"},
	}
	for _, tc := range cases {
		if got := deviceLoginCredentialName(tc.plan, tc.email); got != tc.want {
			t.Errorf("deviceLoginCredentialName(%q, %q) = %q, want %q", tc.plan, tc.email, got, tc.want)
		}
	}
}
