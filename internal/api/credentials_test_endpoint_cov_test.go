package api

// Coverage for credentials_test_endpoint.go — probeProvider's per-provider
// HTTP branches, the Test wizard endpoint, and TestStored's decrypt /
// success / audit paths.
//
// probeProvider talks to hard-coded provider URLs through
// http.DefaultClient, so these tests swap http.DefaultClient.Transport
// for a canned RoundTripper. The swap is process-global state, therefore
// every test that performs it stays SERIAL (no t.Parallel) and restores
// the original transport via t.Cleanup. Go runs parallel-marked tests
// only after all serial tests in the package finish, so the swap cannot
// race against the rest of the suite.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covCredsRoundTripper returns canned responses and records the request.
type covCredsRoundTripper struct {
	status  int
	err     error
	lastReq *http.Request
}

func (rt *covCredsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.lastReq = req
	if rt.err != nil {
		return nil, rt.err
	}
	return &http.Response{
		StatusCode: rt.status,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// swapDefaultTransport installs rt as http.DefaultClient's transport and
// restores the original when the test ends. SERIAL tests only.
func swapDefaultTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	orig := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	t.Cleanup(func() { http.DefaultClient.Transport = orig })
}

func TestProbeProvider_StatusMatrix(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		status    int
		wantValid bool
		wantErr   string // substring of result.Error; "" = no error expected
	}{
		{"anthropic 200", "ANTHROPIC", 200, true, ""},
		{"anthropic 401", "ANTHROPIC", 401, false, "Invalid API key"},
		{"anthropic 403", "ANTHROPIC", 403, false, "Access revoked"},
		{"anthropic 429 still valid", "ANTHROPIC", 429, true, "Rate limited"},
		{"anthropic 500", "ANTHROPIC", 500, false, "Unexpected response: 500"},
		{"openai 200", "OPENAI", 200, true, ""},
		{"openai 401", "OPENAI", 401, false, "Invalid API key"},
		{"openai 503", "OPENAI", 503, false, "Unexpected response: 503"},
		{"google 200", "GOOGLE", 200, true, ""},
		{"google 400", "GOOGLE", 400, false, "Unexpected response: 400"},
		{"cursor 200", "CURSOR", 200, true, ""},
		{"cursor 401", "CURSOR", 401, false, "Invalid Cursor API key"},
		{"cursor 403 subscription", "CURSOR", 403, false, "cursor.com/account"},
		{"cursor 500", "CURSOR", 500, false, "Unexpected response: 500"},
		{"factory 200", "FACTORY", 200, true, ""},
		{"factory 401", "FACTORY", 401, false, "Invalid Factory API key"},
		{"factory 502", "FACTORY", 502, false, "Unexpected response: 502"},
		{"github 200", "GITHUB", 200, true, ""},
		{"github 401", "GITHUB", 401, false, "Invalid token"},
		{"github 403 scopes", "GITHUB", 403, false, "Token lacks required scopes"},
		{"github 500", "GITHUB", 500, false, "Unexpected response: 500"},
		{"gitlab 200", "GITLAB", 200, true, ""},
		{"gitlab 401", "GITLAB", 401, false, "Invalid token"},
		{"gitlab 403 scopes", "GITLAB", 403, false, "Token lacks required scopes"},
		{"gitlab 500", "GITLAB", 500, false, "Unexpected response: 500"},
		{"vercel 200", "VERCEL", 200, true, ""},
		{"vercel 401", "VERCEL", 401, false, "Invalid token"},
		{"vercel 403", "VERCEL", 403, false, "Invalid token"},
		{"vercel 500", "VERCEL", 500, false, "Unexpected response: 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &covCredsRoundTripper{status: tc.status}
			swapDefaultTransport(t, rt)
			res := probeProvider(context.Background(), tc.provider, "API_KEY", "secret-value")
			if res.Valid != tc.wantValid {
				t.Errorf("valid = %v, want %v (res=%+v)", res.Valid, tc.wantValid, res)
			}
			if res.Status != tc.status {
				t.Errorf("status = %d, want %d", res.Status, tc.status)
			}
			if tc.wantErr == "" && res.Error != "" {
				t.Errorf("unexpected error %q", res.Error)
			}
			if tc.wantErr != "" && !strings.Contains(res.Error, tc.wantErr) {
				t.Errorf("error = %q, want substring %q", res.Error, tc.wantErr)
			}
			if rt.lastReq == nil {
				t.Fatal("no HTTP request issued")
			}
		})
	}
}

func TestProbeProvider_RequestShape(t *testing.T) {
	t.Run("anthropic headers", func(t *testing.T) {
		rt := &covCredsRoundTripper{status: 200}
		swapDefaultTransport(t, rt)
		probeProvider(context.Background(), "ANTHROPIC", "API_KEY", "sk-ant-real")
		if got := rt.lastReq.Header.Get("x-api-key"); got != "sk-ant-real" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := rt.lastReq.Header.Get("anthropic-version"); got == "" {
			t.Error("anthropic-version header missing")
		}
		if rt.lastReq.URL.Host != "api.anthropic.com" {
			t.Errorf("host = %q", rt.lastReq.URL.Host)
		}
	})
	t.Run("github bearer + UA", func(t *testing.T) {
		rt := &covCredsRoundTripper{status: 200}
		swapDefaultTransport(t, rt)
		probeProvider(context.Background(), "GITHUB", "API_KEY", "ghp_x")
		if got := rt.lastReq.Header.Get("Authorization"); got != "Bearer ghp_x" {
			t.Errorf("Authorization = %q", got)
		}
		if got := rt.lastReq.Header.Get("User-Agent"); got != "Crewship/1.0" {
			t.Errorf("User-Agent = %q", got)
		}
	})
	t.Run("gitlab private token", func(t *testing.T) {
		rt := &covCredsRoundTripper{status: 200}
		swapDefaultTransport(t, rt)
		probeProvider(context.Background(), "GITLAB", "API_KEY", "glpat-x")
		if got := rt.lastReq.Header.Get("PRIVATE-TOKEN"); got != "glpat-x" {
			t.Errorf("PRIVATE-TOKEN = %q", got)
		}
	})
	t.Run("google key in query", func(t *testing.T) {
		rt := &covCredsRoundTripper{status: 200}
		swapDefaultTransport(t, rt)
		probeProvider(context.Background(), "GOOGLE", "API_KEY", "AIza-x")
		if got := rt.lastReq.URL.Query().Get("key"); got != "AIza-x" {
			t.Errorf("key query param = %q", got)
		}
	})
}

func TestProbeProvider_ConnectionErrors(t *testing.T) {
	providers := []string{"ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "GITHUB", "GITLAB", "VERCEL"}
	for _, p := range providers {
		t.Run(p, func(t *testing.T) {
			swapDefaultTransport(t, &covCredsRoundTripper{err: context.DeadlineExceeded})
			res := probeProvider(context.Background(), p, "API_KEY", "v")
			if res.Valid {
				t.Error("transport error must not report valid")
			}
			if !strings.Contains(res.Error, "Connection failed") {
				t.Errorf("error = %q, want Connection failed", res.Error)
			}
		})
	}
}

func TestProbeProvider_AnthropicOAuthAndDefault(t *testing.T) {
	// No transport swap on purpose: neither branch may touch the network.
	t.Run("AI_CLI_TOKEN type accepted without probing", func(t *testing.T) {
		res := probeProvider(context.Background(), "ANTHROPIC", "AI_CLI_TOKEN", "whatever")
		if !res.Valid {
			t.Error("AI_CLI_TOKEN should be accepted as valid")
		}
		if !strings.Contains(res.Error, "OAuth token accepted") {
			t.Errorf("error = %q", res.Error)
		}
	})
	t.Run("sk-ant-oat prefix accepted without probing", func(t *testing.T) {
		res := probeProvider(context.Background(), "ANTHROPIC", "API_KEY", "sk-ant-oat01-xyz")
		if !res.Valid {
			t.Error("setup token should be accepted as valid")
		}
	})
	t.Run("unknown provider has no validation", func(t *testing.T) {
		res := probeProvider(context.Background(), "SOME_RANDOM_TOOL", "API_KEY", "v")
		if !res.Valid {
			t.Error("default branch should report valid")
		}
		if !strings.Contains(res.Error, "No validation available") {
			t.Errorf("error = %q", res.Error)
		}
	})
}

// ---- Test (wizard inline value test) ----

func TestCredentialTest_BadJSON(t *testing.T) {
	db := setupTestDB(t)
	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/test", strings.NewReader("{nope"))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCredentialTest_MissingValue(t *testing.T) {
	db := setupTestDB(t)
	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/test",
		strings.NewReader(`{"provider":"GITHUB","type":"API_KEY","value":""}`))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCredentialTest_OfflineProviderHappyPath(t *testing.T) {
	db := setupTestDB(t)
	h := NewCredentialHandler(db, newTestLogger())
	// CUSTOM hits probeProvider's default branch — no network needed.
	req := httptest.NewRequest("POST", "/api/v1/credentials/test",
		strings.NewReader(`{"provider":"CUSTOM","type":"API_KEY","value":"v"}`))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var res testResult
	if err := json.NewDecoder(rr.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Valid {
		t.Errorf("valid = false, want true (res=%+v)", res)
	}
}

// ---- TestStored ----

func TestCredentialTestStored_DecryptFailure500(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Garbage ciphertext that encryption.Decrypt cannot parse.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-bad-enc', ?, 'bad', 'not-encrypted', 'SECRET', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-bad-enc/test", nil)
	req.SetPathValue("credentialId", "cred-bad-enc")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCredentialTestStored_OfflineProvider_SuccessAndAudit(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Provider CUSTOM → probeProvider's default branch, no network.
	enc, err := encryption.Encrypt("plain-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-offline', ?, 'off', ?, 'SECRET', 'CUSTOM', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, enc, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-offline/test", nil)
	req.SetPathValue("credentialId", "cred-offline")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var res testResult
	if err := json.NewDecoder(rr.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Valid {
		t.Errorf("valid = false (res=%+v)", res)
	}
	// The manual test must land in the audit trail.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM credential_audit WHERE credential_id = 'cred-offline' AND event_type = 'TEST'`).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if n != 1 {
		t.Errorf("audit TEST rows = %d, want 1", n)
	}
}
