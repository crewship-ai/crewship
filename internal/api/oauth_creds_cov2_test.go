package api

// Second coverage pass for oauth_creds.go: loadOAuthCredential's error
// wraps, storeOAuthTokens' UPDATE failure, Discover's no-match 404, and
// AutoConnect's forwarded-header redirect URI + post-DCR insert failures
// (RAISE triggers on credentials / oauth_states).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOC2_LoadOAuthCredential_Direct(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)

	t.Run("not found wraps ErrNoRows", func(t *testing.T) {
		_, err := h.loadOAuthCredential(context.Background(), "ghost-cred", wsID)
		if err == nil || !errors.Is(err, sql.ErrNoRows) || !strings.Contains(err.Error(), "credential not found") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("decrypt failure", func(t *testing.T) {
		covOF2SeedBadSecretCred(t, h, wsID, userID, "cred-oc2-bad")
		_, err := h.loadOAuthCredential(context.Background(), "cred-oc2-bad", wsID)
		if err == nil || !strings.Contains(err.Error(), "decrypt client secret") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestOC2_StoreOAuthTokens_UpdateError(t *testing.T) {
	h, db, _, _ := covOAuthRig(t)
	db.Close()
	err := h.storeOAuthTokens(context.Background(), "cred-x", &tokenResponse{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 60})
	if err == nil || !strings.Contains(err.Error(), "update credentials") {
		t.Errorf("err = %v", err)
	}
}

func TestOC2_Discover_NoMatch404(t *testing.T) {
	h, _, _, _ := covOAuthRig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withTestDiscoveryClient(t, srv)

	req := httptest.NewRequest("POST", "/api/v1/oauth/discover",
		strings.NewReader(`{"mcp_url":"https://nobody-knows-this.example/sse"}`))
	rr := httptest.NewRecorder()
	h.Discover(rr, req)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "Could not discover OAuth endpoints") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestOC2_AutoConnect_ForwardedHeaders(t *testing.T) {
	h, _, userID, wsID := covOAuthRig(t)
	// Discovery succeeds without DCR → needs_client_id, but the redirect
	// URI derivation (X-Forwarded-Proto / X-Forwarded-Host) runs first.
	srv := covDiscoverySrv(t, false, 0)
	withTestDiscoveryClient(t, srv)

	req := httptest.NewRequest("POST", "/api/v1/oauth/auto-connect",
		strings.NewReader(`{"mcp_url":"https://issuer.test/mcp"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "edge.example.org")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AutoConnect(rr, req)
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
}

func TestOC2_AutoConnect_CredentialInsertFails500(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	srv := covDiscoverySrv(t, true, http.StatusCreated)
	withTestDiscoveryClient(t, srv)

	if _, err := db.Exec(`
		CREATE TRIGGER oc2_block_cred BEFORE INSERT ON credentials
		WHEN NEW.oauth_client_id = 'dcr-client-id'
		BEGIN SELECT RAISE(ABORT, 'oc2 no creds'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://issuer.test/mcp","server_name":"s"}`)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "Failed to create credential") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestOC2_AutoConnect_StateInsertFails500(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	srv := covDiscoverySrv(t, true, http.StatusCreated)
	withTestDiscoveryClient(t, srv)

	if _, err := db.Exec(`
		CREATE TRIGGER oc2_block_state BEFORE INSERT ON oauth_states
		BEGIN SELECT RAISE(ABORT, 'oc2 no states'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := covACPost(t, h, userID, wsID, "OWNER", `{"mcp_url":"https://issuer.test/mcp","server_name":"s"}`)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "Failed to store state") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}
