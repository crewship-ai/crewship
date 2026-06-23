package api

// Coverage for oauth_creds.go — AutoConnect's discovery/DCR decision
// tree. Uses withTestDiscoveryClient (oauth_test.go) to reroute the
// SSRF-guarded discoveryClient at a loopback httptest server, so
// discovery + DCR run for real without touching the network. These
// tests mutate the package-global discoveryClient, so they stay SERIAL.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covACPost(t *testing.T, h *OAuthHandler, userID, wsID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/oauth/auto-connect", strings.NewReader(body))
	req.Host = "crewship.local"
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.AutoConnect(rr, req)
	return rr
}

func TestOAuthAutoConnect_Guards(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	t.Run("member forbidden", func(t *testing.T) {
		if rr := covACPost(t, h, userID, wsID, "MEMBER", `{"mcp_url":"https://x.test"}`); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("missing mcp_url", func(t *testing.T) {
		if rr := covACPost(t, h, userID, wsID, "OWNER", `{}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

func TestOAuthAutoConnect_ProviderHint_NeedsClientID(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	// provider_hint resolves endpoints from the static table; no DCR
	// registration endpoint → "needs_client_id" short-circuit, offline.
	rr := covACPost(t, h, userID, wsID, "OWNER",
		`{"mcp_url":"https://mcp.example.test/sse","server_name":"gh","provider_hint":"github"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["status"] != "needs_client_id" {
		t.Errorf("status = %v", out["status"])
	}
	if out["auth_url"] != OAuthProviders["github"].AuthURL {
		t.Errorf("auth_url = %v", out["auth_url"])
	}
}

func TestOAuthAutoConnect_DiscoveryFails_KnownProviderFallback(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	// 404 on every .well-known path → discovery fails; the URL contains
	// "linear.app" so matchKnownProvider kicks in → needs_client_id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://mcp.linear.app/sse"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&out)
	if out["status"] != "needs_client_id" {
		t.Errorf("status = %v", out["status"])
	}
	if out["auth_url"] != OAuthProviders["linear"].AuthURL {
		t.Errorf("auth_url = %v", out["auth_url"])
	}
}

func TestOAuthAutoConnect_DiscoveryFails_NoMatch400(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://totally-unknown.example/sse"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Personal API Key") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

// covDiscoverySrv serves RFC 8414 metadata (with optional DCR endpoint)
// and an RFC 7591 registration endpoint.
func covDiscoverySrv(t *testing.T, withDCR bool, dcrStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer":                           "https://issuer.test",
			"authorization_endpoint":           "https://issuer.test/authorize",
			"token_endpoint":                   "https://issuer.test/token",
			"scopes_supported":                 []string{"read", "write"},
			"code_challenge_methods_supported": []string{"S256"},
		}
		if withDCR {
			meta["registration_endpoint"] = "https://issuer.test/register"
		}
		_ = json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var req DCRRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("DCR body decode: %v", err)
		}
		if len(req.RedirectURIs) != 1 || !strings.Contains(req.RedirectURIs[0], "/api/v1/oauth/callback") {
			t.Errorf("DCR redirect_uris = %v", req.RedirectURIs)
		}
		w.WriteHeader(dcrStatus)
		if dcrStatus == http.StatusCreated || dcrStatus == http.StatusOK {
			_ = json.NewEncoder(w).Encode(DCRResponse{ClientID: "dcr-client-id", ClientSecret: "dcr-secret"})
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOAuthAutoConnect_DCRHappyPath_CreatesPendingCredential(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	srv := covDiscoverySrv(t, true, http.StatusCreated)
	withTestDiscoveryClient(t, srv)

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://issuer.test/mcp","server_name":"myserver"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["status"] != "authorize" {
		t.Fatalf("status = %v; body=%v", out["status"], out)
	}
	credID, _ := out["credential_id"].(string)
	if credID == "" {
		t.Fatal("credential_id missing")
	}
	au, _ := out["auth_url"].(string)
	if !strings.Contains(au, "client_id=dcr-client-id") || !strings.Contains(au, "code_challenge=") {
		t.Errorf("auth_url = %q", au)
	}

	// Credential row landed in PENDING with the DCR client + discovered endpoints.
	var name, status, clientID, authURL, tokenURL, scopes string
	if err := db.QueryRow(`
		SELECT name, status, oauth_client_id, oauth_auth_url, oauth_token_url, COALESCE(oauth_scopes,'')
		FROM credentials WHERE id = ?`, credID).
		Scan(&name, &status, &clientID, &authURL, &tokenURL, &scopes); err != nil {
		t.Fatalf("query credential: %v", err)
	}
	if !strings.HasPrefix(name, "myserver-oauth-") {
		t.Errorf("name = %q", name)
	}
	if status != "PENDING" || clientID != "dcr-client-id" {
		t.Errorf("status=%q client_id=%q", status, clientID)
	}
	if authURL != "https://issuer.test/authorize" || tokenURL != "https://issuer.test/token" {
		t.Errorf("endpoints = %q / %q", authURL, tokenURL)
	}
	if scopes != "read write" {
		t.Errorf("scopes = %q", scopes)
	}
	// CSRF state stored for the upcoming callback.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_states WHERE credential_id = ?`, credID).Scan(&n); err != nil || n != 1 {
		t.Errorf("oauth_states rows = %d (err=%v)", n, err)
	}
}

func TestOAuthAutoConnect_DCRFails_NeedsClientID(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	srv := covDiscoverySrv(t, true, http.StatusForbidden)
	withTestDiscoveryClient(t, srv)

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://issuer.test/mcp"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&out)
	if out["status"] != "needs_client_id" {
		t.Errorf("status = %v", out["status"])
	}
	if out["token_url"] != "https://issuer.test/token" {
		t.Errorf("token_url = %v", out["token_url"])
	}
}

func TestOAuthAutoConnect_DiscoveredNoDCR_NeedsClientID(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	srv := covDiscoverySrv(t, false, 0)
	withTestDiscoveryClient(t, srv)

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://issuer.test/mcp"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&out)
	if out["status"] != "needs_client_id" {
		t.Errorf("status = %v", out["status"])
	}
}

// Discover endpoint — success + known-provider fallback paths.
func TestOAuthDiscover_SuccessAndFallback(t *testing.T) {
	h, _, _, _ := covOAuthRig(t)

	t.Run("discovery success", func(t *testing.T) {
		srv := covDiscoverySrv(t, true, http.StatusCreated)
		withTestDiscoveryClient(t, srv)
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"mcp_url":"https://issuer.test/mcp"}`))
		rr := httptest.NewRecorder()
		h.Discover(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&out)
		if out["source"] != "discovery" || out["supports_dcr"] != true || out["supports_pkce"] != true {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("fallback to known provider", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		withTestDiscoveryClient(t, srv)
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"mcp_url":"https://api.github.com/mcp"}`))
		rr := httptest.NewRecorder()
		h.Discover(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&out)
		if out["source"] != "known_provider" {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("missing mcp_url 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		h.Discover(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}
