package api

// Coverage for oauth_flow.go — Initiate, Callback, Exchange, Loopback and
// runLoopbackServer.
//
// The token exchange helper (exchangeOAuthCode) builds its own
// SSRF-guarded HTTP client, so the success branch cannot be driven
// against a loopback httptest server. We use a syntactically invalid
// token URL ("://bad") so http.NewRequestWithContext fails immediately —
// the exchange error paths are exercised deterministically with zero
// network traffic and zero timeout waits.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func covOAuthRig(t *testing.T) (h *OAuthHandler, db *sql.DB, userID, wsID string) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	h = NewOAuthHandler(db, newTestLogger())
	return
}

// covSeedOAuthCred inserts an OAUTH2 credential with full OAuth config.
// tokenURL "://bad" makes any exchange attempt fail fast and offline.
func covSeedOAuthCred(t *testing.T, db *sql.DB, wsID, userID, credID, authURL, tokenURL string) {
	t.Helper()
	secEnc, err := encryption.Encrypt("client-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, status,
			oauth_client_id, oauth_client_secret_enc, oauth_auth_url, oauth_token_url, oauth_scopes,
			created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'OAUTH2', '', 'PENDING', 'client-id-1', ?, ?, ?, 'read write', ?, datetime('now'), datetime('now'))`,
		credID, wsID, "oauth-"+credID, secEnc, authURL, tokenURL, userID); err != nil {
		t.Fatalf("seed oauth credential: %v", err)
	}
}

// ---- Initiate ----

func TestOAuthInitiate_Matrix(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-init", "https://provider.example/authorize?audience=api", "https://provider.example/token")
	// A credential missing client/auth config.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, status, oauth_client_id, oauth_auth_url, oauth_token_url, created_by, created_at, updated_at)
		VALUES ('cred-noconf', ?, 'nc', 'OAUTH2', '', 'PENDING', '', '', '', ?, datetime('now'), datetime('now'))`,
		wsID, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	run := func(role, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/oauth/initiate", strings.NewReader(body))
		req.Host = "crewship.local:8080"
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.Initiate(rr, req)
		return rr
	}

	t.Run("member forbidden", func(t *testing.T) {
		if rr := run("MEMBER", `{"credential_id":"cred-init"}`); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		if rr := run("OWNER", `{nope`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("missing credential_id", func(t *testing.T) {
		if rr := run("OWNER", `{}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown credential 404", func(t *testing.T) {
		if rr := run("OWNER", `{"credential_id":"ghost"}`); rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("missing oauth config 400", func(t *testing.T) {
		if rr := run("OWNER", `{"credential_id":"cred-noconf"}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("happy path returns auth_url + stores state", func(t *testing.T) {
		rr := run("OWNER", `{"credential_id":"cred-init"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
		}
		var out map[string]string
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out["state"] == "" {
			t.Fatal("state missing")
		}
		au := out["auth_url"]
		for _, want := range []string{
			"client_id=client-id-1",
			"code_challenge_method=S256",
			"audience=api", // pre-existing query params preserved
			"redirect_uri=http%3A%2F%2Fcrewship.local%3A8080%2Fapi%2Fv1%2Foauth%2Fcallback",
		} {
			if !strings.Contains(au, want) {
				t.Errorf("auth_url %q missing %q", au, want)
			}
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_states WHERE state = ?`, out["state"]).Scan(&n); err != nil || n != 1 {
			t.Errorf("oauth_states rows = %d (err=%v), want 1", n, err)
		}
	})
}

// ---- Callback ----

func TestOAuthCallback_Matrix(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-cb", "https://p.example/auth", "://bad")

	seedState := func(state, credID, createdAt string) {
		verifierEnc, err := encryption.Encrypt("verifier-123")
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if _, err := db.Exec(`
			INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier, created_at)
			VALUES (?, ?, ?, 'http://localhost/cb', ?, ?)`,
			state, credID, wsID, verifierEnc, createdAt); err != nil {
			t.Fatalf("seed state: %v", err)
		}
	}

	run := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/oauth/callback"+query, nil)
		rr := httptest.NewRecorder()
		h.Callback(rr, req)
		return rr
	}

	t.Run("provider error param", func(t *testing.T) {
		rr := run("?error=access_denied")
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "access_denied") {
			t.Errorf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("missing code/state", func(t *testing.T) {
		if rr := run("?code=abc"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown state", func(t *testing.T) {
		rr := run("?code=abc&state=ghost")
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Invalid or expired state") {
			t.Errorf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("expired state", func(t *testing.T) {
		seedState("state-old", "cred-cb", time.Now().Add(-20*time.Minute).UTC().Format(time.RFC3339))
		rr := run("?code=abc&state=state-old")
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "expired") {
			t.Errorf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("exchange failure 502", func(t *testing.T) {
		seedState("state-fresh", "cred-cb", time.Now().UTC().Format(time.RFC3339))
		rr := run("?code=abc&state=state-fresh")
		if rr.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502; body=%q", rr.Code, rr.Body.String())
		}
		// State must be consumed even on failure (single-use).
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM oauth_states WHERE state = 'state-fresh'`).Scan(&n)
		if n != 0 {
			t.Errorf("state not consumed (rows=%d)", n)
		}
	})
	t.Run("state pointing at deleted credential 404", func(t *testing.T) {
		seedState("state-orphan", "ghost-cred", time.Now().UTC().Format(time.RFC3339))
		rr := run("?code=abc&state=state-orphan")
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
}

// ---- Exchange ----

func TestOAuthExchange_Matrix(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-ex", "https://p.example/auth", "://bad")

	run := func(role, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/oauth/exchange", strings.NewReader(body))
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.Exchange(rr, req)
		return rr
	}

	t.Run("member forbidden", func(t *testing.T) {
		if rr := run("MEMBER", `{"credential_id":"cred-ex","code":"c"}`); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("missing fields", func(t *testing.T) {
		if rr := run("OWNER", `{"credential_id":"cred-ex"}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("invalid state for verifier recovery", func(t *testing.T) {
		rr := run("OWNER", `{"credential_id":"cred-ex","code":"c","state":"ghost"}`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Invalid or expired OAuth state") {
			t.Errorf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("state bound to other credential rejected", func(t *testing.T) {
		verifierEnc, _ := encryption.Encrypt("v")
		if _, err := db.Exec(`
			INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier)
			VALUES ('state-othercred', 'some-other-cred', ?, 'http://l/cb', ?)`, wsID, verifierEnc); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		rr := run("OWNER", `{"credential_id":"cred-ex","code":"c","state":"state-othercred"}`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "does not match credential") {
			t.Errorf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("unknown credential 404", func(t *testing.T) {
		if rr := run("OWNER", `{"credential_id":"ghost","code":"c"}`); rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("state recovery then exchange failure 502", func(t *testing.T) {
		verifierEnc, _ := encryption.Encrypt("verifier-xyz")
		if _, err := db.Exec(`
			INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier)
			VALUES ('state-ex-ok', 'cred-ex', ?, 'http://l/cb', ?)`, wsID, verifierEnc); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		rr := run("OWNER", `{"credential_id":"cred-ex","code":"c","state":"state-ex-ok"}`)
		if rr.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502; body=%q", rr.Code, rr.Body.String())
		}
	})
}

// ---- Loopback + runLoopbackServer ----

func TestOAuthLoopback_ValidationMatrix(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, status, oauth_client_id, oauth_auth_url, oauth_token_url, created_by, created_at, updated_at)
		VALUES ('cred-lb-noconf', ?, 'nc', 'OAUTH2', '', 'PENDING', 'cid', 'https://p/auth', '', ?, datetime('now'), datetime('now'))`,
		wsID, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	run := func(role, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/oauth/loopback", strings.NewReader(body))
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.Loopback(rr, req)
		return rr
	}

	t.Run("member forbidden", func(t *testing.T) {
		if rr := run("MEMBER", `{"credential_id":"x"}`); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("missing credential_id", func(t *testing.T) {
		if rr := run("OWNER", `{}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown credential 404", func(t *testing.T) {
		if rr := run("OWNER", `{"credential_id":"ghost"}`); rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("incomplete oauth config 400", func(t *testing.T) {
		if rr := run("OWNER", `{"credential_id":"cred-lb-noconf"}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

// covStartLoopback drives Loopback to a 200 and returns the loopback
// port + state from the response.
func covStartLoopback(t *testing.T, h *OAuthHandler, userID, wsID, credID string) (port int, state string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/oauth/loopback",
		strings.NewReader(`{"credential_id":"`+credID+`"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Loopback(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Loopback status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		AuthURL      string `json:"auth_url"`
		LoopbackPort int    `json:"loopback_port"`
		State        string `json:"state"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.LoopbackPort == 0 || out.State == "" || !strings.Contains(out.AuthURL, "client_id=client-id-1") {
		t.Fatalf("unexpected loopback response: %+v", out)
	}
	return out.LoopbackPort, out.State
}

// covGetLoopback GETs the loopback /callback and returns the HTML body.
func covGetLoopback(t *testing.T, port int, query string) string {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/callback%s", port, query)
	var resp *http.Response
	var err error
	// The listener is live before Loopback returns, but the goroutine's
	// Serve may lag a beat — retry briefly.
	for i := 0; i < 20; i++ {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return sb.String()
}

func TestOAuthLoopback_CallbackErrorParam(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-lb1", "https://p.example/auth", "://bad")
	port, _ := covStartLoopback(t, h, userID, wsID, "cred-lb1")
	body := covGetLoopback(t, port, "?error=access_denied")
	if !strings.Contains(body, "Authorization failed") || !strings.Contains(body, "access_denied") {
		t.Errorf("body = %q", body)
	}
}

func TestOAuthLoopback_CallbackInvalidState(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-lb2", "https://p.example/auth", "://bad")
	port, _ := covStartLoopback(t, h, userID, wsID, "cred-lb2")
	body := covGetLoopback(t, port, "?code=abc&state=wrong-state")
	if !strings.Contains(body, "Invalid callback") {
		t.Errorf("body = %q", body)
	}
}

func TestOAuthLoopback_CallbackExchangeFailure(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-lb3", "https://p.example/auth", "://bad")
	port, state := covStartLoopback(t, h, userID, wsID, "cred-lb3")
	body := covGetLoopback(t, port, "?code=abc&state="+state)
	if !strings.Contains(body, "Token exchange failed") {
		t.Errorf("body = %q", body)
	}
}
