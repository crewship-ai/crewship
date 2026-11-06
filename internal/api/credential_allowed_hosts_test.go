package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedHostBoundRig seeds a crew with one agent and one granted credential.
func seedHostBoundRig(t *testing.T, mode, domainsJSON, hostsJSON string) (*sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains) VALUES ('hb-crew', ?, 'C', 'hb-c', ?, ?)`,
		wsID, mode, domainsJSON)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('hb-agent', 'hb-crew', ?, 'A', 'hb-a')`, wsID)
	seedCredentialEnc(t, db, wsID, userID, "hb-cred", "GH_TOKEN", "ghp_secretvalue")
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('hb-ac', 'hb-agent', 'hb-cred', 'GH_TOKEN', 0, datetime('now'))`)
	if hostsJSON != "" {
		execOrFatal(t, db, `UPDATE credentials SET allowed_hosts = ? WHERE id = 'hb-cred'`, hostsJSON)
	}
	return db, wsID, userID
}

func TestHostBoundDelivery(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		domains     string
		hosts       string
		wantDeliver bool
		wantInNote  string
	}{
		{name: "unbound free crew keeps today's behaviour", mode: "free", hosts: "", wantDeliver: true},
		{name: "explicit empty list is unbound", mode: "free", hosts: "[]", wantDeliver: true},
		{name: "bound on free crew is withheld", mode: "free", hosts: `["api.github.com"]`, wantInNote: "no egress allowlist"},
		{name: "bound covered by crew allowlist", mode: "restricted", domains: `["api.github.com"]`, hosts: `["api.github.com"]`, wantDeliver: true},
		{name: "bound covered by built-in defaults", mode: "restricted", domains: `[]`, hosts: `["api.anthropic.com"]`, wantDeliver: true},
		{name: "bound host outside allowlist", mode: "restricted", domains: `["api.github.com"]`, hosts: `["api.github.com","evil.example.net"]`, wantInNote: "evil.example.net"},
		{name: "wildcard needs wildcard allowlist", mode: "restricted", domains: `["a.example.org"]`, hosts: `["*.example.org"]`, wantInNote: "*.example.org"},
		{name: "wildcard covered by wildcard allowlist", mode: "restricted", domains: `["*.example.org"]`, hosts: `["*.example.org"]`, wantDeliver: true},
		{name: "unnormalised stored host is normalised on read", mode: "restricted", domains: `["api.github.com"]`, hosts: `["API.GitHub.com."]`, wantDeliver: true},
		{name: "malformed stored JSON fails closed", mode: "restricted", domains: `["api.github.com"]`, hosts: `not-json`, wantInNote: "unreadable"},
		{name: "malformed stored host fails closed", mode: "restricted", domains: `["api.github.com"]`, hosts: `["*"]`, wantInNote: "unreadable"},
		{name: "malformed crew allowlist fails closed", mode: "restricted", domains: `{bad`, hosts: `["api.github.com"]`, wantInNote: "network policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _ := seedHostBoundRig(t, tc.mode, tc.domains, tc.hosts)
			delivered, notices, err := loadDeliveredCredentials(context.Background(), db, "hb-agent")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := len(delivered) == 1; got != tc.wantDeliver {
				t.Fatalf("delivered = %v, want %v (notices %+v)", got, tc.wantDeliver, notices)
			}
			if tc.wantDeliver {
				return
			}
			if len(delivered) != 0 {
				t.Fatalf("a withheld credential must leave no ciphertext behind: %+v", delivered)
			}
			var n *deliveredSlotNotice
			for i := range notices {
				if notices[i].HostBound {
					n = &notices[i]
				}
			}
			if n == nil || n.CredentialID != "hb-cred" || n.Delivered != "" {
				t.Fatalf("want a host-bound withhold notice, got %+v", notices)
			}
			if !strings.Contains(n.Reason, tc.wantInNote) {
				t.Errorf("reason %q lacks %q", n.Reason, tc.wantInNote)
			}
			if strings.Contains(strings.ToLower(n.Reason), "per-credential egress") {
				t.Errorf("reason must not claim per-credential egress enforcement: %q", n.Reason)
			}
		})
	}
}

func TestHostBoundDelivery_CrewlessAgentWithheld(t *testing.T) {
	db, wsID, _ := seedHostBoundRig(t, "restricted", `["api.github.com"]`, `["api.github.com"]`)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('hb-solo', ?, 'S', 'hb-s')`, wsID)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('hb-ac2', 'hb-solo', 'hb-cred', 'GH_TOKEN', 0, datetime('now'))`)
	delivered, notices, err := loadDeliveredCredentials(context.Background(), db, "hb-solo")
	if err != nil || len(delivered) != 0 || len(notices) != 1 || !notices[0].HostBound {
		t.Fatalf("crew-less agent: delivered=%d notices=%+v err=%v", len(delivered), notices, err)
	}
}

type hostsHTTP struct {
	code int
	body string
}

func hostsReq(t *testing.T, r *Router, method, tok, path, body string) hostsHTTP {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path+"?workspace_id=test-workspace-id", rdr)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return hostsHTTP{rr.Code, rr.Body.String()}
}

func storedHosts(t *testing.T, db *sql.DB) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT allowed_hosts FROM credentials WHERE id='hb-cred'`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAllowedHostsEndpoint(t *testing.T) {
	db, wsID, ownerID := seedHostBoundRig(t, "restricted", `["api.github.com"]`, "")
	ownerTok := mintTokenFor(t, db, ownerID, "hostsowner000000000000000")
	memberTok := seedRoleMemberToken(t, db, wsID, "hosts-member", "MEMBER", "hostsmember00000000000000")
	adminTok := seedRoleMemberToken(t, db, wsID, "hosts-admin", "ADMIN", "hostsadmin000000000000000")
	scopedTok := seedRoleMemberToken(t, db, wsID, "hosts-scoped", "ADMIN", "hostsscoped00000000000000")
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes = '["credentials:read"]' WHERE id = 'clt-hosts-scoped'`)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/credentials/hb-cred/allowed-hosts"

	t.Run("fresh credential is unbound", func(t *testing.T) {
		res := hostsReq(t, r, "GET", ownerTok, path, "")
		if res.code != 200 || !strings.Contains(res.body, `"allowed_hosts":[]`) {
			t.Fatalf("%d %s", res.code, res.body)
		}
	})

	t.Run("non-human and under-privileged callers cannot set", func(t *testing.T) {
		for name, tok := range map[string]string{"member": memberTok, "scoped token": scopedTok, "no token": ""} {
			res := hostsReq(t, r, "PUT", tok, path, `{"allowed_hosts":["api.github.com"]}`)
			if res.code != 401 && res.code != 403 {
				t.Errorf("%s: status %d, want 401/403 (%s)", name, res.code, res.body)
			}
		}
		if got := storedHosts(t, db); got != "[]" {
			t.Fatalf("hosts changed by a refused caller: %s", got)
		}
	})

	t.Run("generic create and update cannot set hosts", func(t *testing.T) {
		res := hostsReq(t, r, "PATCH", ownerTok, "/api/v1/credentials/hb-cred", `{"description":"x","allowed_hosts":["api.github.com"]}`)
		_ = res
		if got := storedHosts(t, db); got != "[]" {
			t.Fatalf("PATCH smuggled hosts: %s", got)
		}
	})

	t.Run("malformed hosts are rejected and change nothing", func(t *testing.T) {
		for _, body := range []string{
			`{"allowed_hosts":["*"]}`, `{"allowed_hosts":["*.com"]}`, `{"allowed_hosts":["https://api.github.com"]}`,
			`{"allowed_hosts":["api.github.com:443"]}`, `{"allowed_hosts":["10.0.0.1"]}`, `{"allowed_hosts":["a..b.com"]}`,
			`{"allowed_hosts":["ok.example.com","bad host"]}`, `{"allowed_hosts":"api.github.com"}`, `{"allowed_hosts":null}`, `{}`, `nope`,
		} {
			res := hostsReq(t, r, "PUT", ownerTok, path, body)
			if res.code != 400 {
				t.Errorf("%s: status %d, want 400 (%s)", body, res.code, res.body)
			}
		}
		if got := storedHosts(t, db); got != "[]" {
			t.Fatalf("hosts changed: %s", got)
		}
	})

	t.Run("owner sets, normalised", func(t *testing.T) {
		res := hostsReq(t, r, "PUT", ownerTok, path, `{"allowed_hosts":["API.GitHub.com.","bücher.example","api.github.com"]}`)
		if res.code != 200 {
			t.Fatalf("%d %s", res.code, res.body)
		}
		var out struct {
			AllowedHosts []string `json:"allowed_hosts"`
			Note         string   `json:"note"`
		}
		if err := json.Unmarshal([]byte(res.body), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.AllowedHosts) != 2 || out.AllowedHosts[0] != "api.github.com" || out.AllowedHosts[1] != "xn--bcher-kva.example" {
			t.Fatalf("hosts = %v", out.AllowedHosts)
		}
		if !strings.Contains(out.Note, "does not stop") {
			t.Errorf("response must state the limit of the guarantee: %q", out.Note)
		}
		if got := storedHosts(t, db); got != `["api.github.com","xn--bcher-kva.example"]` {
			t.Fatalf("stored = %s", got)
		}
	})

	t.Run("admin may narrow and clear", func(t *testing.T) {
		res := hostsReq(t, r, "PUT", adminTok, path, `{"allowed_hosts":[]}`)
		if res.code != 200 || storedHosts(t, db) != "[]" {
			t.Fatalf("%d %s stored=%s", res.code, res.body, storedHosts(t, db))
		}
	})

	t.Run("unknown credential is 404", func(t *testing.T) {
		res := hostsReq(t, r, "PUT", ownerTok, "/api/v1/credentials/nope/allowed-hosts", `{"allowed_hosts":["a.example.com"]}`)
		if res.code != http.StatusNotFound {
			t.Fatalf("%d", res.code)
		}
	})
}

func TestAllowedHostsEndpoint_NoAuthKindIsRefused(t *testing.T) {
	db, wsID, userID := seedHostBoundRig(t, "restricted", `[]`, "")
	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("PUT", "/api/v1/credentials/hb-cred/allowed-hosts", strings.NewReader(`{"allowed_hosts":["api.github.com"]}`))
	req.SetPathValue("credentialId", "hb-cred")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	rr := httptest.NewRecorder()
	h.SetAllowedHosts(rr, req.WithContext(ctx))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 for a context RequireAuth never stamped", rr.Code)
	}
	if got := storedHosts(t, db); got != "[]" {
		t.Fatalf("hosts changed: %s", got)
	}
}
