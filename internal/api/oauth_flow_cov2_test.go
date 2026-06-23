package api

// Second coverage pass for oauth_flow.go: Initiate's forwarded-header
// redirect derivation and credential-decrypt failure, Callback's state-
// lookup DB error / verifier-decrypt failure / credential-load failure,
// and Exchange's stored-verifier decrypt + credential-load failures.
//
// Token-exchange success paths stay out of reach by design — the exchange
// helper pins an SSRF-guarded transport that refuses loopback targets.

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// covOF2SeedBadSecretCred inserts an OAUTH2 credential whose client secret
// is NOT valid ciphertext, so loadOAuthCredential fails with a non-NoRows
// error.
func covOF2SeedBadSecretCred(t *testing.T, h *OAuthHandler, wsID, userID, credID string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, status,
			oauth_client_id, oauth_client_secret_enc, oauth_auth_url, oauth_token_url,
			created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'OAUTH2', '', 'PENDING', 'cid', 'garbage-not-ciphertext',
			'https://p.example/auth', 'https://p.example/token', ?, datetime('now'), datetime('now'))`,
		credID, wsID, "bad-"+credID, userID); err != nil {
		t.Fatalf("seed bad-secret credential: %v", err)
	}
}

func covOF2Initiate(t *testing.T, h *OAuthHandler, userID, wsID, body string, hdr map[string]string, useTLS bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if useTLS {
		req.TLS = &tls.ConnectionState{}
	}
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	return rr
}

func TestOF2_Initiate_ForwardedHeaders(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-of2-fwd", "https://provider.example/auth", "https://provider.example/token")

	rr := covOF2Initiate(t, h, userID, wsID, `{"credential_id":"cred-of2-fwd"}`,
		map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "edge.example.com"}, false)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out["auth_url"], "redirect_uri=https%3A%2F%2Fedge.example.com%2Fapi%2Fv1%2Foauth%2Fcallback") {
		t.Errorf("auth_url = %q, want forwarded https host in redirect_uri", out["auth_url"])
	}
}

func TestOF2_Initiate_TLSRequestDerivesHTTPS(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-of2-tls", "https://provider.example/auth", "https://provider.example/token")

	rr := covOF2Initiate(t, h, userID, wsID, `{"credential_id":"cred-of2-tls"}`, nil, true)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out["auth_url"], "redirect_uri=https%3A%2F%2F") {
		t.Errorf("auth_url = %q, want https redirect_uri from TLS request", out["auth_url"])
	}
}

func TestOF2_Initiate_CredentialDecryptError500(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	covOF2SeedBadSecretCred(t, h, wsID, userID, "cred-of2-bad")

	rr := covOF2Initiate(t, h, userID, wsID, `{"credential_id":"cred-of2-bad"}`, nil, false)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "Failed to load credential") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// ---- Callback ----

func covOF2SeedState(t *testing.T, h *OAuthHandler, state, credID, wsID, verifier string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier, created_at)
		VALUES (?, ?, ?, 'http://cb.example/done', ?, ?)`,
		state, credID, wsID, verifier, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed oauth state: %v", err)
	}
}

func TestOF2_Callback_StateLookupDBError500(t *testing.T) {
	h, db, _, _ := covOAuthRig(t)
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=c&state=s", nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestOF2_Callback_VerifierDecryptError500(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-of2-cb1", "https://p/auth", "https://p/token")
	covOF2SeedState(t, h, "of2-state-badverif", "cred-of2-cb1", wsID, "not-real-ciphertext")

	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=c&state=of2-state-badverif", nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestOF2_Callback_CredentialLoadError500(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	covOF2SeedBadSecretCred(t, h, wsID, userID, "cred-of2-cb2")
	covOF2SeedState(t, h, "of2-state-badcred", "cred-of2-cb2", wsID, "")

	req := httptest.NewRequest("GET", "/api/v1/oauth/callback?code=c&state=of2-state-badcred", nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- Exchange ----

func covOF2Exchange(t *testing.T, h *OAuthHandler, userID, wsID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/oauth/exchange", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Exchange(rr, req)
	return rr
}

func TestOF2_Exchange_StoredVerifierDecryptError500(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-of2-ex1", "https://p/auth", "https://p/token")
	covOF2SeedState(t, h, "of2-ex-badverif", "cred-of2-ex1", wsID, "still-not-ciphertext")

	rr := covOF2Exchange(t, h, userID, wsID,
		`{"credential_id":"cred-of2-ex1","code":"c","state":"of2-ex-badverif"}`)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "Failed to decrypt state") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestOF2_Exchange_CredentialLoadError500(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	covOF2SeedBadSecretCred(t, h, wsID, userID, "cred-of2-ex2")

	rr := covOF2Exchange(t, h, userID, wsID,
		`{"credential_id":"cred-of2-ex2","code":"c","code_verifier":"v"}`)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "Failed to load credential") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}
