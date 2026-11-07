package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// #5045: every path that hands a host-bound credential's value to a run or a
// service applies the shared credhostgate rule. Each test below fails when
// that path stops asking.

// hostBoundCases: unbound is never affected; bound needs a restricted crew that
// covers every approved host; anything unreadable withholds.
var hostBoundCases = []struct {
	name, mode, domains, hosts string
	deliver                    bool
}{
	{"unbound on free crew unchanged", "free", "", "", true},
	{"empty list unchanged", "free", "", "[]", true},
	{"bound on free crew withheld", "free", "", `["api.github.com"]`, false},
	{"bound and covered delivered", "restricted", `["api.github.com"]`, `["api.github.com"]`, true},
	{"bound outside allowlist withheld", "restricted", `["a.example.org"]`, `["api.github.com"]`, false},
	{"unreadable hosts withheld", "restricted", `["api.github.com"]`, `not-json`, false},
	{"unreadable crew policy withheld", "restricted", `{bad`, `["api.github.com"]`, false},
}

func TestHostBoundServiceEnv(t *testing.T) {
	for _, tc := range hostBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _ := seedHostBoundRig(t, tc.mode, tc.domains, tc.hosts)
			execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('hb-cred', 'hb-crew')`)
			got, err := loadCrewDeliveredEnv(t.Context(), db, "hb-crew")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if has := got["GH_TOKEN"] == "ghp_secretvalue"; has != tc.deliver {
				t.Fatalf("service env delivered = %v, want %v (%v)", has, tc.deliver, got)
			}
		})
	}
}

func TestHostBoundOAuthAccessToken(t *testing.T) {
	for _, tc := range hostBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _ := seedHostBoundRig(t, tc.mode, tc.domains, tc.hosts)
			execOrFatal(t, db, `UPDATE credentials SET type = 'OAUTH2' WHERE id = 'hb-cred'`)
			h := covCfgHandler(db)
			in := []mcpCredEntry{{ID: "hb-cred", EnvVar: "FOO_CLIENT_ID", Type: "OAUTH2", Value: "cid"}}
			out := h.resolveOAuthAccessTokens(httptest.NewRequest("GET", "/", nil), "hb-agent", in)
			has := false
			for _, c := range out {
				if c.EnvVar == "_OAUTH_ACCESS_TOKEN:hb-cred" && c.Value == "ghp_secretvalue" {
					has = true
				}
				if !tc.deliver && c.ID == "hb-cred" {
					t.Fatalf("withheld credential still present: %+v", c)
				}
			}
			if tc.deliver && !has {
				t.Fatalf("unaffected credential lost its access token: %+v", out)
			}
		})
	}
}

func TestHostBoundMCPBindingToken(t *testing.T) {
	for _, tc := range hostBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			db, wsID, userID := seedHostBoundRig(t, tc.mode, tc.domains, tc.hosts)
			h := covCfgHandler(db)
			req := httptest.NewRequest("GET", "/", nil)
			d, err := h.loadAgentData(req, "hb-agent")
			if err != nil {
				t.Fatal(err)
			}
			_ = userID
			covCfgSeedWSServer(t, db, "ws1", wsID, "github", "stdio", "", "github-mcp", `[]`, `{}`)
			covCfgBindServer(t, db, "b1", "hb-agent", "ws1", "workspace", "hb-cred", "bearer", "GITHUB_TOKEN", 1)
			var token string
			for _, s := range h.resolveAgentMCPServers(req, d, "hb-agent") {
				if s.Name == "github" {
					token = s.CredToken
				}
			}
			if has := token == "ghp_secretvalue"; has != tc.deliver {
				t.Fatalf("MCP token delivered = %v, want %v", has, tc.deliver)
			}
		})
	}
}

func TestHostBoundAutoResolvedMCPEnv(t *testing.T) {
	for _, tc := range hostBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			db, wsID, _ := seedHostBoundRig(t, tc.mode, tc.domains, tc.hosts)
			execOrFatal(t, db, `UPDATE credentials SET name = 'gh-token' WHERE id = 'hb-cred'`)
			h := covCfgHandler(db)
			cfg := `{"mcpServers":{"x":{"env":{"GH_TOKEN":"${GH_TOKEN}"}}}}`
			out := h.autoResolveMCPCredentialsGated(t.Context(), "hb-agent", wsID, nil, cfg)
			has := false
			for _, c := range out {
				if c.Value == "ghp_secretvalue" {
					has = true
				}
			}
			if has != tc.deliver {
				t.Fatalf("auto-resolved env delivered = %v, want %v (%+v)", has, tc.deliver, out)
			}
		})
	}
}

func TestHostBoundKeeperExecuteRelease(t *testing.T) {
	for _, tc := range hostBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
			execOrFatal(t, db, `UPDATE crews SET network_mode = ?, allowed_domains = ? WHERE id = ?`, tc.mode, tc.domains, crewID)
			if tc.hosts != "" {
				execOrFatal(t, db, `UPDATE credentials SET allowed_hosts = ? WHERE id = ?`, tc.hosts, credID)
			}
			gk := &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 1}}
			ctr := &mockContainerExec{output: "done"}
			h := newKeeperHandlerWithGK(t, db, gk).
				WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "topsecret"}}).
				WithContainer(ctr)
			rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
			if tc.deliver {
				if rr.Code != http.StatusOK || len(ctr.lastExecEnv) == 0 {
					t.Fatalf("unaffected credential not injected: %d %s", rr.Code, rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusForbidden || len(ctr.lastExecEnv) != 0 {
				t.Fatalf("host-bound credential released: %d %s env=%v", rr.Code, rr.Body.String(), ctr.lastExecEnv)
			}
		})
	}
}
