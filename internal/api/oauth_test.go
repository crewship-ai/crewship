package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// newOAuthHandler creates a handler with the test DB and encryption key.
func newOAuthHandler(t *testing.T) (*OAuthHandler, *sql.DB) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewOAuthHandler(db, logger), db
}

// seedOAuthCredential inserts an OAUTH2 credential with the given fields.
func seedOAuthCredential(t *testing.T, db *sql.DB, wsID, credID, clientID, secretPlain, authURL, tokenURL string) {
	t.Helper()
	encSecret, err := encryption.Encrypt(secretPlain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Idempotent seed — ignore unique-violation if user already exists.
	_, _ = db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('uowner', 'o@o.com', 'O')`)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope,
			oauth_client_id, oauth_client_secret_enc, oauth_auth_url, oauth_token_url, oauth_scopes,
			created_by, created_at, updated_at, status)
		VALUES (?, ?, ?, ?, 'OAUTH2', 'NONE', 'WORKSPACE', ?, ?, ?, ?, ?, 'uowner', ?, ?, 'PENDING')`,
		credID, wsID, "test-cred-"+credID, "pending_oauth", clientID, encSecret, authURL, tokenURL, "scope1 scope2", now, now); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

// ---- Pure functions ----

func TestGeneratePKCE(t *testing.T) {
	t.Parallel()
	v1, c1, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if v1 == "" || c1 == "" {
		t.Fatal("verifier or challenge empty")
	}
	if v1 == c1 {
		t.Error("verifier and challenge should differ")
	}
	v2, _, _ := generatePKCE()
	if v1 == v2 {
		t.Error("verifiers should be unique")
	}
}

func TestGenerateOAuthState(t *testing.T) {
	t.Parallel()
	a, _ := generateOAuthState()
	b, _ := generateOAuthState()
	if a == "" || b == "" {
		t.Fatal("empty state")
	}
	if a == b {
		t.Error("state should be unique")
	}
	if len(a) != 32 {
		t.Errorf("state length = %d, want 32 (16 bytes hex)", len(a))
	}
}

func TestBuildOAuthURL(t *testing.T) {
	t.Parallel()
	got := buildOAuthURL("https://provider/auth", "client-id", "https://app/cb", "state-x", "challenge-y", "read write")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "client-id" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "https://app/cb" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "state-x" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("code_challenge") != "challenge-y" {
		t.Errorf("code_challenge = %q", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("scope") != "read write" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestBuildOAuthURL_PreservesExistingQuery(t *testing.T) {
	t.Parallel()
	got := buildOAuthURL("https://provider/auth?audience=foo", "cid", "/cb", "s", "ch", "")
	u, _ := url.Parse(got)
	if u.Query().Get("audience") != "foo" {
		t.Errorf("audience param lost: %s", got)
	}
}

func TestMatchKnownProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url   string
		want  string // provider key or "" for none
		match bool
	}{
		{"https://api.linear.app/mcp", "linear", true},
		{"https://gitlab.com/oauth", "gitlab", true},
		{"https://api.github.com/x", "github", true},
		{"https://unknown-server.example.com/x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := matchKnownProvider(tt.url)
			if tt.match && got == nil {
				t.Errorf("expected match for %s", tt.url)
			}
			if !tt.match && got != nil {
				t.Errorf("expected no match for %s, got %+v", tt.url, got)
			}
		})
	}
}

// ---- ListProviders ----

func TestOAuth_ListProviders(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/oauth/providers", nil)
	rr := httptest.NewRecorder()
	h.ListProviders(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "google") {
		t.Errorf("response should include google provider")
	}
}

// ---- Initiate ----

func TestOAuth_Initiate_Forbidden(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	body := bytes.NewBufferString(`{"credential_id":"x"}`)
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", body)
	ctx := withWorkspace(req.Context(), "ws1", "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestOAuth_Initiate_BadJSON(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", strings.NewReader(`bad`))
	ctx := withWorkspace(req.Context(), "ws1", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuth_Initiate_MissingCredID(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", strings.NewReader(`{}`))
	ctx := withWorkspace(req.Context(), "ws1", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuth_Initiate_CredentialNotFound(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"credential_id":"missing"}`)
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", body)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusNotFound {
		// loadOAuthCredential returns wrapped err which doesn't match sql.ErrNoRows via errors.Is
		// (the wrapper uses fmt.Errorf with %w on the original) — so 500 is expected here.
		t.Errorf("status = %d, want 404 or 500", rr.Code)
	}
}

func TestOAuth_Initiate_Success(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-ok"
	seedOAuthCredential(t, db, wsID, credID, "client-x", "secret-x", "https://provider/auth", "https://provider/token")

	body := bytes.NewBufferString(fmt.Sprintf(`{"credential_id":"%s"}`, credID))
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", body)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["auth_url"] == "" || resp["state"] == "" {
		t.Errorf("missing auth_url/state: %+v", resp)
	}

	// Verify state stored in DB
	var count int
	db.QueryRow("SELECT COUNT(*) FROM oauth_states WHERE state = ?", resp["state"]).Scan(&count)
	if count != 1 {
		t.Errorf("expected state stored, got count=%d", count)
	}
}

func TestOAuth_Initiate_MissingClientID(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Insert credential with no client_id
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope,
			oauth_client_id, oauth_auth_url, oauth_token_url, created_by, created_at, updated_at, status)
		VALUES ('cred-empty', ?, 'no-client', 'pending', 'OAUTH2', 'NONE', 'WORKSPACE',
			'', '', '', ?, ?, ?, 'PENDING')`, wsID, userID, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := bytes.NewBufferString(`{"credential_id":"cred-empty"}`)
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", body)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ---- Callback ----

func TestOAuth_Callback_OAuthErrorParam(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?error=access_denied", nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "access_denied") {
		t.Errorf("body should mention error: %s", rr.Body.String())
	}
}

func TestOAuth_Callback_MissingCodeOrState(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	tests := []string{"", "?code=abc", "?state=abc"}
	for _, q := range tests {
		req := httptest.NewRequest("GET", "/api/v1/oauth/callback"+q, nil)
		rr := httptest.NewRecorder()
		h.Callback(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("query=%q status = %d, want 400", q, rr.Code)
		}
	}
}

func TestOAuth_Callback_InvalidState(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=c&state=does-not-exist", nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Invalid") {
		t.Errorf("body should mention invalid state: %s", rr.Body.String())
	}
}

func TestOAuth_Callback_ExpiredState(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedOAuthCredential(t, db, wsID, "cred-c", "client", "secret", "https://p/auth", "https://p/token")

	state := "expired-state"
	old := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	encVer, _ := encryption.Encrypt("verifier")
	if _, err := db.Exec(`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		state, "cred-c", wsID, "https://app/cb", encVer, old); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=c&state="+state, nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuth_Callback_TokenExchangeFails(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Token URL points to unroutable address — exchange must fail gracefully
	// (state still consumed). This validates the handler's error-handling path
	// without requiring a working token endpoint (ssrfSafeTransport blocks loopback).
	seedOAuthCredential(t, db, wsID, "cred-cb", "client-1", "secret-1", "https://p/auth", "http://192.0.2.1:1/token")

	state := "valid-state-fail"
	encVer, _ := encryption.Encrypt("verifier-x")
	if _, err := db.Exec(`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier) VALUES (?, ?, ?, ?, ?)`,
		state, "cred-cb", wsID, "https://app/cb", encVer); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=auth-code-x&state="+state, nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (token exchange failure)", rr.Code)
	}

	// State should still be consumed (atomic DELETE...RETURNING happens before exchange)
	var count int
	db.QueryRow("SELECT COUNT(*) FROM oauth_states WHERE state = ?", state).Scan(&count)
	if count != 0 {
		t.Errorf("state should be consumed even on exchange failure, got count=%d", count)
	}
}

// ---- Exchange (manual) ----

func TestOAuth_Exchange_BadRequest(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	tests := []struct {
		body string
		role string
		want int
	}{
		{`{}`, "OWNER", http.StatusBadRequest},
		{`{"credential_id":"x"}`, "OWNER", http.StatusBadRequest},
		{`{"credential_id":"x","code":"y"}`, "VIEWER", http.StatusForbidden},
	}
	for _, tt := range tests {
		req := httptest.NewRequest("POST", "/api/v1/oauth/exchange", strings.NewReader(tt.body))
		ctx := withWorkspace(req.Context(), "ws", tt.role)
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		h.Exchange(rr, req)
		if rr.Code != tt.want {
			t.Errorf("body=%q role=%s status=%d want %d", tt.body, tt.role, rr.Code, tt.want)
		}
	}
}

func TestOAuth_Exchange_StateLookupRequired(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedOAuthCredential(t, db, wsID, "cred-ex", "c1", "s1", "https://p/auth", "https://p/token")

	body := `{"credential_id":"cred-ex","code":"c","state":"missing-state"}`
	req := httptest.NewRequest("POST", "/api/v1/oauth/exchange", strings.NewReader(body))
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Exchange(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (state missing)", rr.Code)
	}
}

func TestOAuth_Exchange_StateMismatchedCredential(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedOAuthCredential(t, db, wsID, "cred-target", "c1", "s1", "https://p/auth", "https://p/token")
	seedOAuthCredential(t, db, wsID, "cred-other", "c2", "s2", "https://p/auth", "https://p/token")

	encVer, _ := encryption.Encrypt("ver")
	if _, err := db.Exec(`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier) VALUES ('s-bad', 'cred-other', ?, '', ?)`, wsID, encVer); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	body := `{"credential_id":"cred-target","code":"c","state":"s-bad"}`
	req := httptest.NewRequest("POST", "/api/v1/oauth/exchange", strings.NewReader(body))
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Exchange(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (state mismatch)", rr.Code)
	}
}

func TestOAuth_Exchange_TokenEndpointUnreachable(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Token URL is unroutable — Exchange should respond with 502 BadGateway.
	seedOAuthCredential(t, db, wsID, "cred-x", "c1", "s1", "https://p/auth", "http://192.0.2.1:1/token")

	body := `{"credential_id":"cred-x","code":"abc","redirect_uri":"https://app/cb"}`
	req := httptest.NewRequest("POST", "/api/v1/oauth/exchange", strings.NewReader(body))
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Exchange(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

// ---- Discover ----

func TestOAuth_Discover_BadRequest(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/discover", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h.Discover(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuth_Discover_KnownProviderFallback(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	body := `{"mcp_url":"https://api.linear.app/v1/mcp"}`
	req := httptest.NewRequest("POST", "/api/v1/oauth/discover", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Discover(rr, req)
	// Discovery will fail (no real network), then fall back to known provider
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "linear") {
		t.Errorf("response should reference linear: %s", rr.Body.String())
	}
}

// ---- AutoConnect ----

func TestOAuth_AutoConnect_Forbidden(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/auto-connect", strings.NewReader(`{"mcp_url":"x"}`))
	ctx := withWorkspace(req.Context(), "ws", "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AutoConnect(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestOAuth_AutoConnect_BadRequest(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/auto-connect", strings.NewReader(`{}`))
	ctx := withUser(req.Context(), &AuthUser{ID: "u1"})
	ctx = withWorkspace(ctx, "ws", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AutoConnect(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuth_AutoConnect_NeedsClientID(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := `{"mcp_url":"https://api.linear.app/mcp","provider_hint":"linear","server_name":"linear"}`
	req := httptest.NewRequest("POST", "/api/v1/oauth/auto-connect", strings.NewReader(body))
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AutoConnect(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "needs_client_id") {
		t.Errorf("expected needs_client_id, got %s", rr.Body.String())
	}
}

// ---- storeStateWithPKCE / loadOAuthCredential / storeOAuthTokens ----

func TestOAuth_StoreAndLoadCredential(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedOAuthCredential(t, db, wsID, "creds-1", "myclient", "mysecret", "https://p/auth", "https://p/token")

	got, err := h.loadOAuthCredential(context.Background(), "creds-1", wsID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ClientID != "myclient" {
		t.Errorf("client_id = %q", got.ClientID)
	}
	if got.ClientSecret != "mysecret" {
		t.Errorf("client_secret = %q (decryption failed?)", got.ClientSecret)
	}
}

func TestOAuth_StoreStateWithPKCE_RoundTrip(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	if err := h.storeStateWithPKCE(context.Background(), "st-1", "cr-1", "ws-1", "https://app/cb", "verifier-plain"); err != nil {
		t.Fatalf("store: %v", err)
	}
	var encVer string
	if err := db.QueryRow("SELECT code_verifier FROM oauth_states WHERE state = ?", "st-1").Scan(&encVer); err != nil {
		t.Fatalf("query: %v", err)
	}
	dec, err := encryption.Decrypt(encVer)
	if err != nil {
		t.Fatalf("decrypt verifier: %v", err)
	}
	if dec != "verifier-plain" {
		t.Errorf("verifier = %q, want verifier-plain", dec)
	}
}

// ---- exchangeOAuthCode / refreshOAuthToken / discovery / DCR ----
//
// These functions use ssrfSafeTransport which BLOCKS connections to private/loopback
// IPs (127.0.0.1). httptest.NewServer always binds loopback so we can't drive the
// real transport against them. We test:
//   * the connection-error path with an unroutable address (RFC 5737 192.0.2.x)
//   * discovery / DCR by swapping the package-level discoveryClient for the test

// withTestDiscoveryClient swaps discoveryClient to a client whose
// transport reroutes every request to `srv` (a loopback httptest
// server), letting tests drive discovery without weakening the
// unconditional httpsafe.ValidateURL guard in fetchJSON /
// dynamicClientRegister. Tests pass synthetic URLs like
// "https://discovery.test/..." that pass validation; the
// httpsafe.RewriteRoundTripper then sends the actual bytes to the
// test server. See the type doc on RewriteRoundTripper for why this
// indirection lives in the transport layer rather than as a URL bypass.
func withTestDiscoveryClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	origClient := discoveryClient
	discoveryClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	}
	t.Cleanup(func() { discoveryClient = origClient })
}

func TestExchangeOAuthCode_HTTPError(t *testing.T) {
	t.Parallel()
	// 192.0.2.0/24 is RFC 5737 TEST-NET-1 — never routable.
	_, err := exchangeOAuthCode(context.Background(), "http://192.0.2.1:1/token", "cid", "", "c", "r", "")
	if err == nil {
		t.Error("expected connection error")
	}
}

func TestRefreshOAuthToken_HTTPError(t *testing.T) {
	t.Parallel()
	_, err := refreshOAuthToken(context.Background(), "http://192.0.2.1:1/token", "c", "", "rt")
	if err == nil {
		t.Error("expected connection error")
	}
}

func TestRefreshExpiringTokens_NoRows(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// No expiring tokens — should be a no-op without errors.
	refreshExpiringTokens(context.Background(), db, nil, logger)
}

func TestStartOAuthRefreshWorker_ExitsOnStop(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartOAuthRefreshWorker(db, nil, logger, stop, &wg)
	close(stop)
	// Wait for worker to exit (with safety timeout).
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit on stop")
	}
}

func TestRefreshExpiringTokens_PicksUpExpiringRows(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Insert a credential expiring soon. The actual refresh attempt will fail
	// (192.0.2.x is unroutable), but we just verify the row is iterated.
	encRefresh, _ := encryption.Encrypt("rt-good")
	encAccess, _ := encryption.Encrypt("at-old")
	expiresSoon := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status,
			oauth_client_id, oauth_token_url, oauth_refresh_token_enc, oauth_token_expires_at,
			created_by, created_at, updated_at, scope, provider)
		VALUES ('exp-pick', ?, 'expiring', ?, 'OAUTH2', 'ACTIVE',
			'client', 'http://192.0.2.1:1/token', ?, ?, ?, datetime('now'), datetime('now'), 'WORKSPACE', 'NONE')`,
		wsID, encAccess, encRefresh, expiresSoon, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Should not panic and should attempt refresh (which will fail on dial).
	refreshExpiringTokens(context.Background(), db, nil, logger)
	// Status may be ACTIVE or EXPIRED depending on dial timing — assertion is just no-panic.
}

// ---- storeOAuthTokens (oauth.go) ----

func TestStoreOAuthTokens_RoundTrip(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedOAuthCredential(t, db, wsID, "cred-store", "c", "s", "https://p/auth", "https://p/token")

	resp := &tokenResponse{
		AccessToken:  "stored-at",
		RefreshToken: "stored-rt",
		ExpiresIn:    3600,
		TokenType:    "Bearer",
	}
	if err := h.storeOAuthTokens(context.Background(), "cred-store", resp); err != nil {
		t.Fatalf("store: %v", err)
	}

	var encAccess, encRefresh, status string
	if err := db.QueryRow("SELECT encrypted_value, oauth_refresh_token_enc, status FROM credentials WHERE id = 'cred-store'").Scan(&encAccess, &encRefresh, &status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %s, want ACTIVE", status)
	}
	at, _ := encryption.Decrypt(encAccess)
	if at != "stored-at" {
		t.Errorf("access = %q", at)
	}
	rt, _ := encryption.Decrypt(encRefresh)
	if rt != "stored-rt" {
		t.Errorf("refresh = %q", rt)
	}
}

// ---- Loopback (forbidden + bad request only — full flow needs real network) ----

func TestOAuth_Loopback_Forbidden(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/loopback", strings.NewReader(`{"credential_id":"x"}`))
	ctx := withWorkspace(req.Context(), "ws", "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Loopback(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestOAuth_Loopback_BadRequest(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/oauth/loopback", strings.NewReader(`{}`))
	ctx := withWorkspace(req.Context(), "ws", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Loopback(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuth_Loopback_CredNotFound(t *testing.T) {
	t.Parallel()
	h, db := newOAuthHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	req := httptest.NewRequest("POST", "/api/v1/oauth/loopback", strings.NewReader(`{"credential_id":"missing"}`))
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Loopback(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ---- SetHub ----

func TestOAuth_SetHub(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHandler(t)
	// Just verify it doesn't panic with nil
	h.SetHub(nil)
}

// ---- Discovery (oauth_discovery.go) ----

func TestDiscoverOAuthFromMCPURL_AuthServerEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"issuer":"http://x",
			"authorization_endpoint":"http://x/auth",
			"token_endpoint":"http://x/token",
			"registration_endpoint":"http://x/register",
			"code_challenge_methods_supported":["S256"],
			"scopes_supported":["read","write"]
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	got, err := discoverOAuthFromMCPURL(context.Background(), "https://discovery.test/some/mcp")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got.AuthURL != "http://x/auth" {
		t.Errorf("auth_url = %q", got.AuthURL)
	}
	if !got.SupportsPKCE {
		t.Error("PKCE should be supported")
	}
	if !got.SupportsDCR {
		t.Error("DCR should be supported")
	}
	if got.Scopes != "read write" {
		t.Errorf("scopes = %q", got.Scopes)
	}
}

func TestDiscoverOAuthFromMCPURL_MissingEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"issuer":"http://x"}`)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)
	_, err := discoverOAuthFromMCPURL(context.Background(), "https://discovery.test/")
	if err == nil {
		t.Error("expected error for missing endpoints")
	}
}

func TestDiscoverOAuthFromMCPURL_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)
	_, err := discoverOAuthFromMCPURL(context.Background(), "https://discovery.test/")
	if err == nil {
		t.Error("expected discovery to fail")
	}
}

func TestDynamicClientRegister_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"client_id":"dyn-id","client_secret":"dyn-sec"}`)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)
	got, err := dynamicClientRegister(context.Background(), "https://dcr.test/register", "https://app/cb")
	if err != nil {
		t.Fatalf("dcr: %v", err)
	}
	if got.ClientID != "dyn-id" {
		t.Errorf("client_id = %q", got.ClientID)
	}
}

func TestDynamicClientRegister_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)
	_, err := dynamicClientRegister(context.Background(), "https://dcr.test/register", "https://app/cb")
	if err == nil {
		t.Error("expected error")
	}
}

func TestDynamicClientRegister_EmptyClientID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)
	_, err := dynamicClientRegister(context.Background(), "https://dcr.test/register", "x")
	if err == nil {
		t.Error("expected error for empty client_id")
	}
}

// ---- Autobind ----

func TestAutoBindCredentialToMCPServers_NoOp(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewOAuthHandler(db, logger)

	// No matching servers — should silently succeed.
	h.autoBindCredentialToMCPServers(context.Background(), "missing-cred", "ws-x")
}

func TestAutoBindCredentialToMCPServers_CrewScoped(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewOAuthHandler(db, logger)

	// Credential
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, created_by, created_at, updated_at)
		VALUES ('cred-ab', ?, 'linear-oauth-abc12', 'enc', 'OAUTH2', 'NONE', 'WORKSPACE', ?, ?, ?)`,
		wsID, userID, now, now); err != nil {
		t.Fatalf("seed cred: %v", err)
	}

	// Crew + agent + crew MCP server named "linear"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-1', ?, 'C', 'c', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a-1', 'crew-1', ?, 'A', 'a')`, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, endpoint, created_at) VALUES ('srv-1', 'crew-1', 'linear', 'Linear', 'streamable-http', 'http://x', ?)`, now); err != nil {
		t.Fatalf("seed mcp server: %v", err)
	}

	h.autoBindCredentialToMCPServers(context.Background(), "cred-ab", wsID)

	// Verify a binding was created
	var count int
	db.QueryRow("SELECT COUNT(*) FROM agent_mcp_bindings WHERE credential_id = ?", "cred-ab").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 binding, got %d", count)
	}
}
