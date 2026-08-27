package api

// Coverage for the MCP OAuth client's two RFC MUSTs that were previously
// missing entirely:
//
//   - RFC 8707 `resource` — sent on both the authorization request
//     (buildOAuthURL) and the token request (exchangeOAuthCode), sourced
//     from the credential's RFC 9728 protected-resource metadata.
//   - RFC 9207 `iss` — validated on every path that sees the authorization
//     response directly (Callback, Loopback) against the RFC 8414 issuer
//     recorded for the credential at connect time.
//
// Tests that swap the package-global discoveryClient / oauthTokenClient
// stay SERIAL (no t.Parallel()), same convention as oauth_creds_cov_test.go —
// see its file comment for why.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// withTestOAuthTokenClient swaps oauthTokenClient (oauth_token.go) to a
// client whose transport reroutes every request to srv — same technique as
// withTestDiscoveryClient in oauth_test.go, applied to the token-exchange
// client so exchangeOAuthCode's HTTP-response branches (including the
// resource form field) can be asserted without weakening ssrfSafeTransport.
func withTestOAuthTokenClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	orig := oauthTokenClient
	oauthTokenClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	}
	t.Cleanup(func() { oauthTokenClient = orig })
}

// ---- buildOAuthURL: resource ----

func TestBuildOAuthURL_Resource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resource string
	}{
		{"empty resource omits the param", ""},
		{"resource forwarded verbatim", "https://mcp.example.test/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildOAuthURL("https://provider/auth", "cid", "https://app/cb", "state", "chal", "", tt.resource)
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			hasResource := u.Query().Has("resource")
			if tt.resource == "" {
				if hasResource {
					t.Errorf("resource param present when it should be omitted: %q", u.Query().Get("resource"))
				}
				return
			}
			if !hasResource || u.Query().Get("resource") != tt.resource {
				t.Errorf("resource = %q, want %q", u.Query().Get("resource"), tt.resource)
			}
		})
	}
}

// ---- exchangeOAuthCode: resource ----

func TestExchangeOAuthCode_SendsResource(t *testing.T) {
	tests := []struct {
		name     string
		resource string
	}{
		{"empty resource omits the form field", ""},
		{"resource forwarded to the token endpoint", "https://mcp.example.test/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sawResourceKey bool
			var gotResource string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse form: %v", err)
				}
				sawResourceKey = r.PostForm.Has("resource")
				gotResource = r.PostFormValue("resource")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"access_token":"tok-x","token_type":"Bearer"}`)
			}))
			defer srv.Close()
			withTestOAuthTokenClient(t, srv)

			_, err := exchangeOAuthCode(context.Background(), "https://token.test/token", "cid", "", "code-x", "https://app/cb", "", tt.resource)
			if err != nil {
				t.Fatalf("exchange: %v", err)
			}
			if tt.resource == "" {
				if sawResourceKey {
					t.Errorf("resource form field present when it should be omitted: %q", gotResource)
				}
				return
			}
			if !sawResourceKey || gotResource != tt.resource {
				t.Errorf("resource = %q, want %q", gotResource, tt.resource)
			}
		})
	}
}

// ---- discovery: resource + issuer ----

func TestDiscoverOAuthFromMCPURL_ResourceAndIssuer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"resource":"https://mcp.example.test/","authorization_servers":["https://discovery.test"]}`)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"issuer":"https://discovery.test",
			"authorization_endpoint":"https://discovery.test/auth",
			"token_endpoint":"https://discovery.test/token"
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	got, err := discoverOAuthFromMCPURL(context.Background(), "https://discovery.test/mcp")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got.Resource != "https://mcp.example.test/" {
		t.Errorf("resource = %q, want the PRM-advertised resource, not an invented one", got.Resource)
	}
	if got.Issuer != "https://discovery.test" {
		t.Errorf("issuer = %q", got.Issuer)
	}
}

func TestDiscoverOAuthFromMCPURL_NoPRM_ResourceStaysEmpty(t *testing.T) {
	// No protected-resource metadata at all (404) — discovery still succeeds
	// via the authorization-server metadata fallback, but Resource must stay
	// empty rather than being invented from the MCP URL.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"issuer":"https://discovery.test",
			"authorization_endpoint":"https://discovery.test/auth",
			"token_endpoint":"https://discovery.test/token"
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	got, err := discoverOAuthFromMCPURL(context.Background(), "https://discovery.test/mcp")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got.Resource != "" {
		t.Errorf("resource = %q, want empty (no PRM to source it from)", got.Resource)
	}
	if got.Issuer != "https://discovery.test" {
		t.Errorf("issuer = %q", got.Issuer)
	}
}

func TestDiscoverOAuthFromMCPURL_MissingIssuerFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "oauth-authorization-server") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"authorization_endpoint":"https://x/auth","token_endpoint":"https://x/token"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	_, err := discoverOAuthFromMCPURL(context.Background(), "https://discovery.test/mcp")
	if err == nil {
		t.Fatal("expected discovery to fail closed when the authorization server's metadata omits issuer")
	}
}

// ---- validateIssuer (RFC 9207) ----

func TestValidateIssuer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		expectedIssuer string
		gotIssuer      string
		wantErr        bool
	}{
		{"no recorded issuer skips the check", "", "", false},
		{"no recorded issuer skips the check even if iss is present", "", "https://anything.example", false},
		{"matching issuer accepted", "https://as.example", "https://as.example", false},
		{"missing iss rejected when an issuer is known", "https://as.example", "", true},
		{"mismatched issuer rejected", "https://as.example", "https://attacker.example", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateIssuer(tt.expectedIssuer, tt.gotIssuer)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateIssuer(%q, %q) err = %v, wantErr %v", tt.expectedIssuer, tt.gotIssuer, err, tt.wantErr)
			}
		})
	}
}

// ---- Callback: iss validation end to end ----

func TestOAuth_Callback_IssuerValidation(t *testing.T) {
	tests := []struct {
		name       string
		credIssuer string
		respIss    string
		wantStatus int
	}{
		{"matching issuer proceeds to token exchange", "https://as.example", "https://as.example", http.StatusBadGateway},
		{"mismatched issuer rejected before exchange", "https://as.example", "https://attacker.example", http.StatusBadRequest},
		{"missing iss rejected when issuer is known", "https://as.example", "", http.StatusBadRequest},
		{"no recorded issuer proceeds unchecked", "", "", http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, db := newOAuthHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			credID := "cred-iss-" + strings.ReplaceAll(strings.ReplaceAll(tt.name, " ", "-"), "_", "-")
			// Unroutable token URL: if the iss check is (correctly) bypassed
			// or passed, the handler must reach the network and fail with
			// 502 — that is how a passing case is told apart from a
			// short-circuited 400.
			seedOAuthCredential(t, db, wsID, credID, "client", "secret", "https://p/auth", "http://192.0.2.1:1/token")
			if tt.credIssuer != "" {
				if _, err := db.Exec("UPDATE credentials SET oauth_issuer = ? WHERE id = ?", tt.credIssuer, credID); err != nil {
					t.Fatalf("set issuer: %v", err)
				}
			}

			state := "state-" + credID
			encVer, err := encryption.Encrypt("verifier")
			if err != nil {
				t.Fatalf("encrypt verifier: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier) VALUES (?, ?, ?, ?, ?)`,
				state, credID, wsID, "https://app/cb", encVer); err != nil {
				t.Fatalf("seed state: %v", err)
			}

			target := "/api/v1/oauth/callback?code=auth-code&state=" + state
			if tt.respIss != "" {
				target += "&iss=" + url.QueryEscape(tt.respIss)
			}
			req := httptest.NewRequest("GET", target, nil)
			rr := httptest.NewRecorder()
			h.Callback(rr, req)
			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body=%s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

// ---- Loopback: iss validation end to end ----

func TestOAuthLoopback_IssuerValidation(t *testing.T) {
	tests := []struct {
		name       string
		credIssuer string
		queryIss   string
		wantBody   string
	}{
		{"matching issuer proceeds to exchange", "https://as.example", "https://as.example", "Token exchange failed"},
		{"mismatched issuer rejected before exchange", "https://as.example", "https://attacker.example", "Issuer validation failed"},
		{"missing iss rejected when issuer is known", "https://as.example", "", "Issuer validation failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, db, userID, wsID := covOAuthRig(t)
			credID := "cred-lb-iss-" + strings.ReplaceAll(tt.name, " ", "-")
			// "://bad" (same fixture oauth_flow_cov_test.go uses) makes
			// http.NewRequestWithContext fail immediately inside
			// exchangeOAuthCode, so a reached exchange always surfaces as
			// "Token exchange failed" without needing a real token server.
			covSeedOAuthCred(t, db, wsID, userID, credID, "https://p.example/auth", "://bad")
			if _, err := db.Exec("UPDATE credentials SET oauth_issuer = ? WHERE id = ?", tt.credIssuer, credID); err != nil {
				t.Fatalf("set issuer: %v", err)
			}
			port, state := covStartLoopback(t, h, userID, wsID, credID)
			q := "?code=abc&state=" + state
			if tt.queryIss != "" {
				q += "&iss=" + url.QueryEscape(tt.queryIss)
			}
			body := covGetLoopback(t, port, q)
			if !strings.Contains(body, tt.wantBody) {
				t.Errorf("body = %q, want substring %q", body, tt.wantBody)
			}
		})
	}
}

// ---- Initiate: resource end to end ----

func TestOAuthInitiate_SendsResourceParam(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-init-res", "https://provider.example/authorize", "https://provider.example/token")
	if _, err := db.Exec("UPDATE credentials SET oauth_resource = ? WHERE id = ?", "https://mcp.example.test/", "cred-init-res"); err != nil {
		t.Fatalf("set resource: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", strings.NewReader(`{"credential_id":"cred-init-res"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var out struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(out.AuthURL)
	if err != nil {
		t.Fatalf("parse auth_url: %v", err)
	}
	if got := u.Query().Get("resource"); got != "https://mcp.example.test/" {
		t.Errorf("resource = %q", got)
	}
}

// ---- AutoConnect: discovered resource + issuer are persisted and used ----

func TestOAuthAutoConnect_PersistsDiscoveredResourceAndIssuer(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"resource":"https://issuer.test/mcp","authorization_servers":["https://issuer.test"]}`)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"issuer":"https://issuer.test",
			"authorization_endpoint":"https://issuer.test/authorize",
			"token_endpoint":"https://issuer.test/token",
			"registration_endpoint":"https://issuer.test/register"
		}`)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(DCRResponse{ClientID: "dcr-client-id", ClientSecret: "dcr-secret"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://issuer.test/mcp","server_name":"myserver"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	credID, _ := out["credential_id"].(string)
	if credID == "" {
		t.Fatal("credential_id missing")
	}

	var resource, issuer string
	if err := db.QueryRow(`SELECT oauth_resource, oauth_issuer FROM credentials WHERE id = ?`, credID).Scan(&resource, &issuer); err != nil {
		t.Fatalf("query: %v", err)
	}
	if resource != "https://issuer.test/mcp" {
		t.Errorf("oauth_resource = %q", resource)
	}
	if issuer != "https://issuer.test" {
		t.Errorf("oauth_issuer = %q", issuer)
	}

	authURL, _ := out["auth_url"].(string)
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth_url: %v", err)
	}
	if got := u.Query().Get("resource"); got != "https://issuer.test/mcp" {
		t.Errorf("auth_url resource = %q", got)
	}
}
