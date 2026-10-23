package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An agent's tool profile, MCP servers, crew and role, and a temporary hire's
// crew membership, are permission changes: each is chained in the journal on
// the transaction that makes it, with previous/current values, and an audit
// failure rolls the change back. Run healthy and with the journal insert
// aborted, through the real router.
func TestAgentPrivilegeChangesAreAuditedAtomically(t *testing.T) {
	type operation struct {
		method, path, body, action string
		read                       func(*sql.DB) string
		before, after              string
		check                      func(t *testing.T, metadata map[string]any)
	}
	seed := func(t *testing.T, f *authorizationAuditFixture) {
		t.Helper()
		for _, id := range []string{"ap-crew-a", "ap-crew-b"} {
			execOrFatal(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug,autonomy_level,max_ephemeral_agents) VALUES(?,?,?,?,'full',5)`, id, f.workspace, id, id)
		}
		seedAgentRow(t, f.db, "ap-agent", f.workspace, "ap-crew-a", "Agent", "ap-agent", "AGENT")
	}
	scalar := func(query string, args ...any) func(*sql.DB) string {
		return func(db *sql.DB) string {
			var v sql.NullString
			if err := db.QueryRow(query, args...).Scan(&v); err != nil {
				return "error: " + err.Error()
			}
			return v.String
		}
	}
	privilegesMoved := func(field, from, to string) func(*testing.T, map[string]any) {
		return func(t *testing.T, m map[string]any) {
			prev, _ := m["previous"].(map[string]any)
			cur, _ := m["current"].(map[string]any)
			if prev[field] != from || cur[field] != to {
				t.Errorf("%s previous=%v current=%v, want %s -> %s", field, prev[field], cur[field], from, to)
			}
		}
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := "2024-01-01T00:00:00Z"
	cases := map[string]func(*testing.T, *authorizationAuditFixture) operation{
		"tool_profile": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			return operation{"PATCH", "/api/v1/agents/ap-agent", `{"tool_profile":"FULL"}`, "update",
				scalar(`SELECT tool_profile FROM agents WHERE id='ap-agent'`), "CODING", "FULL", privilegesMoved("tool_profile", "CODING", "FULL")}
		},
		"crew_move": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			return operation{"PATCH", "/api/v1/agents/ap-agent", `{"crew_id":"ap-crew-b"}`, "update",
				scalar(`SELECT crew_id FROM agents WHERE id='ap-agent'`), "ap-crew-a", "ap-crew-b", privilegesMoved("crew_id", "ap-crew-a", "ap-crew-b")}
		},
		"promote_to_lead": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			seedAgentRow(t, f.db, "ap-old-lead", f.workspace, "ap-crew-a", "Lead", "ap-old-lead", "LEAD")
			return operation{"PATCH", "/api/v1/agents/ap-agent", `{"agent_role":"LEAD"}`, "update",
				scalar(`SELECT agent_role || '/' || (SELECT agent_role FROM agents WHERE id='ap-old-lead') FROM agents WHERE id='ap-agent'`), "AGENT/LEAD", "LEAD/AGENT",
				func(t *testing.T, m map[string]any) {
					privilegesMoved("agent_role", "AGENT", "LEAD")(t, m)
					if d, _ := m["demoted_leads"].([]any); len(d) != 1 || d[0] != "ap-old-lead" {
						t.Errorf("demoted_leads = %v, want [ap-old-lead]", m["demoted_leads"])
					}
				}}
		},
		"create": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			return operation{"POST", "/api/v1/agents", `{"name":"Created","slug":"ap-created","crew_id":"ap-crew-b","agent_role":"AGENT","cli_adapter":"CLAUDE_CODE","tool_profile":"MINIMAL"}`, "create",
				scalar(`SELECT CAST(COUNT(*) AS TEXT) FROM agents WHERE slug='ap-created'`), "0", "1",
				func(t *testing.T, m map[string]any) {
					cur, _ := m["current"].(map[string]any)
					if cur["crew_id"] != "ap-crew-b" || cur["tool_profile"] != "MINIMAL" || cur["agent_role"] != "AGENT" {
						t.Errorf("create audit current = %v", m["current"])
					}
				}}
		},
		"delete": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			execOrFatal(t, f.db, `UPDATE agents SET mcp_config_json=? WHERE id='ap-agent'`, `{"mcpServers":{"gh":{"command":"npx","env":{"GITHUB_TOKEN":"del-value-marker"}}}}`)
			return operation{"DELETE", "/api/v1/agents/ap-agent", ``, "delete",
				scalar(`SELECT CASE WHEN deleted_at IS NULL THEN 'live' ELSE 'deleted' END FROM agents WHERE id='ap-agent'`), "live", "deleted",
				func(t *testing.T, m map[string]any) {
					prev, _ := m["previous"].(map[string]any)
					if prev["crew_id"] != "ap-crew-a" || prev["tool_profile"] != "CODING" {
						t.Errorf("delete audit previous = %v", m["previous"])
					}
					if raw, _ := json.Marshal(m); strings.Contains(string(raw), "del-value-marker") || !strings.Contains(string(raw), "GITHUB_TOKEN") {
						t.Errorf("delete audit must name MCP env keys without values: %s", raw)
					}
				}}
		},
		"hire": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			execOrFatal(t, f.db, `INSERT INTO crew_templates (id, name, slug, agents_json, is_builtin) VALUES ('ap-tmpl', 'Docs Writer', 'ap-docs-writer', '[]', 1)`)
			return operation{"POST", "/api/v1/agents/hire", `{"crew_id":"ap-crew-a","template_slug":"ap-docs-writer","ttl_minutes":30,"reason":"audit"}`, "agent.hired",
				scalar(`SELECT CAST(COUNT(*) AS TEXT) FROM agents WHERE ephemeral=1`), "0", "1",
				func(t *testing.T, m map[string]any) {
					if m["crew_id"] != "ap-crew-a" || m["tool_profile"] != "CODING" || m["agent_role"] != "AGENT" {
						t.Errorf("hire audit lacks membership: %v", m)
					}
				}}
		},
		"rehire": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			id := seedEphemeralAgent(t, f.db, f.workspace, "ap-crew-a", "ap-ghost", &past, &past, "[x] hire: test")
			return operation{"POST", "/api/v1/agents/" + id + "/rehire", `{"reason":"again","ttl_minutes":30}`, "agent.rehired",
				scalar(`SELECT CASE WHEN expired_at IS NULL THEN 'live' ELSE 'ghost' END FROM agents WHERE id=?`, id), "ghost", "live",
				func(t *testing.T, m map[string]any) {
					if m["crew_id"] != "ap-crew-a" || m["was_ghost"] != true {
						t.Errorf("rehire audit = %v", m)
					}
				}}
		},
		"release": func(t *testing.T, f *authorizationAuditFixture) operation {
			seed(t, f)
			id := seedEphemeralAgent(t, f.db, f.workspace, "ap-crew-a", "ap-live", &future, nil, "[x] hire: test")
			return operation{"POST", "/api/v1/agents/" + id + "/release", ``, "agent.released",
				scalar(`SELECT CASE WHEN expired_at IS NULL THEN 'live' ELSE 'ghost' END FROM agents WHERE id=?`, id), "live", "ghost",
				func(t *testing.T, m map[string]any) {
					if m["crew_id"] != "ap-crew-a" {
						t.Errorf("release audit = %v", m)
					}
				}}
		},
	}
	for name, setup := range cases {
		for _, fault := range []string{"healthy", "audit_abort"} {
			t.Run(name+"/"+fault, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				op := setup(t, f)
				if fault == "audit_abort" {
					execOrFatal(t, f.db, `CREATE TRIGGER stop_access_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN SELECT RAISE(ABORT,'access audit unavailable'); END`)
				}
				rr := f.do(op.method, op.path, op.body)
				saved := op.read(f.db)
				var chained, legacy int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type LIKE 'audit.entity_%' AND json_extract(payload,'$.action')=? AND json_extract(payload,'$.entity_type')='AGENT'`, op.action).Scan(&chained); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action=? AND entity_type='AGENT'`, op.action).Scan(&legacy); err != nil {
					t.Fatal(err)
				}
				if fault == "audit_abort" {
					if rr.Code != 500 || saved != op.before || chained != 0 || legacy != 0 {
						t.Fatalf("audit failure committed the change: HTTP%d state=%q want=%q chain=%d legacy=%d body=%s",
							rr.Code, saved, op.before, chained, legacy, rr.Body.String())
					}
					return
				}
				if rr.Code >= 300 || saved != op.after {
					t.Fatalf("healthy HTTP%d state=%q want=%q: %s", rr.Code, saved, op.after, rr.Body.String())
				}
				if chained != 1 || legacy != 1 {
					t.Fatalf("missing atomic dual audit: chain=%d legacy=%d", chained, legacy)
				}
				var actor, raw string
				if err := f.db.QueryRow(`SELECT actor_id,payload FROM journal_entries WHERE json_extract(payload,'$.action')=? AND json_extract(payload,'$.entity_type')='AGENT'`, op.action).Scan(&actor, &raw); err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Metadata map[string]any `json:"metadata"`
				}
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatal(err)
				}
				if actor != f.owner {
					t.Errorf("actor = %s, want %s", actor, f.owner)
				}
				op.check(t, payload.Metadata)
				f.checkChain(t)
			})
		}
	}
}

// MCP configurations commonly carry API keys in env, headers, arguments or
// URLs. The audit of an MCP change records server names, transport, the
// executable's name and env/header KEY names only; no value of the request
// reaches audit_logs or the journal.
func TestAgentMCPConfigAuditNeverStoresSecrets(t *testing.T) {
	secrets := []string{"env-value-marker", "header-value-marker", "arg-value-marker", "url-value-marker"}
	config := `{"mcpServers":{` +
		`"github":{"command":"/usr/local/bin/npx","args":["-y","server-github","--token=arg-value-marker"],"env":{"GITHUB_TOKEN":"env-value-marker"}},` +
		`"remote":{"type":"http","url":"https://mcp.example.test/v1?key=url-value-marker","headers":{"Authorization":"` + secrets[1] + `"}}}}`
	tests := []struct {
		name   string
		before string // stored config before the PATCH ("" = none)
		body   map[string]any
	}{
		{"set", "", map[string]any{"mcp_config_json": config}},
		{"replace", config, map[string]any{"mcp_config_json": `{"mcpServers":{}}`}},
		{"clear", config, map[string]any{"mcp_config_json": ""}},
		{"alongside other fields", "", map[string]any{"mcp_config_json": config, "name": "Renamed", "tool_profile": "FULL"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			execOrFatal(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('mcp-crew',?,'mcp-crew','mcp-crew')`, f.workspace)
			seedAgentRow(t, f.db, "mcp-agent", f.workspace, "mcp-crew", "Agent", "mcp-agent", "AGENT")
			if tc.before != "" {
				execOrFatal(t, f.db, `UPDATE agents SET mcp_config_json=? WHERE id='mcp-agent'`, tc.before)
			}
			body, _ := json.Marshal(tc.body)
			if rr := f.do("PATCH", "/api/v1/agents/mcp-agent", string(body)); rr.Code != 200 {
				t.Fatalf("PATCH = %d: %s", rr.Code, rr.Body.String())
			}
			var stored strings.Builder
			for _, q := range []string{`SELECT COALESCE(metadata,'') FROM audit_logs`, `SELECT COALESCE(payload,'') || COALESCE(summary,'') FROM journal_entries`} {
				rows, err := f.db.Query(q)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var s string
					if err := rows.Scan(&s); err != nil {
						t.Fatal(err)
					}
					stored.WriteString(s + "\n")
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
			}
			all := stored.String()
			for _, secret := range secrets {
				if strings.Contains(all, secret) {
					t.Errorf("secret %q stored in audit_logs or journal", secret)
				}
			}
			for _, name := range []string{`"GITHUB_TOKEN"`, `"Authorization"`, `"github"`, `"npx"`} {
				if !strings.Contains(all, name) {
					t.Errorf("audit lacks the redacted MCP description %s:\n%s", name, all)
				}
			}
		})
	}
}

func TestMCPConfigAudit(t *testing.T) {
	tests := []struct {
		name, raw, want string
	}{
		{"empty", "", `[]`},
		{"invalid", "{", `{"unparseable":true}`},
		{"stdio", `{"mcpServers":{"b":{"command":"node /srv/x.js --key=v","env":{"Z":"1","A":"2"}},"a":{"url":"https://h/x?t=1"}}}`,
			`[{"command":"","env_keys":[],"header_keys":[],"name":"a","transport":"http"},{"command":"node","env_keys":["A","Z"],"header_keys":[],"name":"b","transport":"stdio"}]`},
		{"env assignment before the command", `{"mcpServers":{"s":{"command":"TOKEN=cmd-value-marker OTHER=x /opt/bin/npx -y srv"}}}`,
			`[{"command":"npx","env_keys":[],"header_keys":[],"name":"s","transport":"stdio"}]`},
		{"quoted assignment split by whitespace", `{"mcpServers":{"s":{"command":"TOKEN=\"abc quoted-value-marker\" npx srv"}}}`,
			`[{"command":"","env_keys":[],"header_keys":[],"name":"s","transport":"stdio"}]`},
		{"shell operator", `{"mcpServers":{"s":{"command":"$(cat /secret) npx"}}}`,
			`[{"command":"","env_keys":[],"header_keys":[],"name":"s","transport":"stdio"}]`},
		{"declared transport", `{"mcpServers":{"s":{"type":"sse","url":"https://h","headers":{"X-Key":"v"}}}}`,
			`[{"command":"","env_keys":[],"header_keys":["X-Key"],"name":"s","transport":"sse"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := json.Marshal(mcpConfigAudit(tc.raw))
			if string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// Paths that run on behalf of an agent carry no user: their chained records
// name a system actor and a NULL audit_logs user, and an audit failure still
// creates nothing.
func TestAgentInternalPathsAuditAsSystem(t *testing.T) {
	type path struct {
		action string
		// setup seeds a database and returns the call to make and the
		// query counting the agents it created.
		setup func(t *testing.T) (*sql.DB, func() *httptest.ResponseRecorder, string)
	}
	paths := map[string]path{
		"internal hire": {"agent.hired", func(t *testing.T) (*sql.DB, func() *httptest.ResponseRecorder, string) {
			db := setupTestDB(t)
			_, wsID, _ := seedHireCrew(t, db, "full", 5)
			b, _ := json.Marshal(covHire2Body(nil))
			return db, func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				NewHireInternalAdapter(newHireHandler(t, db)).Hire(rr, httptest.NewRequest("POST", "/?workspace_id="+wsID, bytes.NewReader(b)))
				return rr
			}, `SELECT CAST(COUNT(*) AS TEXT) FROM agents WHERE ephemeral=1`
		}},
		"internal create": {"agent.created", func(t *testing.T) (*sql.DB, func() *httptest.ResponseRecorder, string) {
			db, wsID, crewID, _, res := autonomyRig(t, "full")
			return db, func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				gatedInternalHandler(t, db, res).CreateAgent(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
					`{"crew_id":"`+crewID+`","name":"Made By Agent","tool_profile":"MINIMAL"}`, wsID, crewID))
				return rr
			}, `SELECT CAST(COUNT(*) AS TEXT) FROM agents WHERE name='Made By Agent'`
		}},
	}
	for name, p := range paths {
		for _, fault := range []string{"healthy", "audit_abort"} {
			t.Run(name+"/"+fault, func(t *testing.T) {
				db, call, countQuery := p.setup(t)
				if fault == "audit_abort" {
					execOrFatal(t, db, `CREATE TRIGGER stop_access_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN SELECT RAISE(ABORT,'access audit unavailable'); END`)
				}
				rr := call()
				var created string
				if err := db.QueryRow(countQuery).Scan(&created); err != nil {
					t.Fatal(err)
				}
				var legacy, chained int
				if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action=? AND entity_type='AGENT' AND user_id IS NULL`, p.action).Scan(&legacy); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE json_extract(payload,'$.action')=? AND actor_type='system' AND COALESCE(actor_id,'')=''`, p.action).Scan(&chained); err != nil {
					t.Fatal(err)
				}
				if fault == "audit_abort" {
					if rr.Code != http.StatusInternalServerError || created != "0" || legacy != 0 {
						t.Fatalf("audit failure created the agent: HTTP%d created=%s legacy=%d: %s", rr.Code, created, legacy, rr.Body.String())
					}
					return
				}
				if rr.Code >= 300 || created != "1" || legacy != 1 || chained != 1 {
					t.Fatalf("HTTP%d created=%s legacy(NULL user)=%d chained(system)=%d: %s", rr.Code, created, legacy, chained, rr.Body.String())
				}
			})
		}
	}
}

// When a created agent's required review row cannot be written, the agent is
// removed again and the removal is chained next to its creation record.
func TestAgentCreationRevertIsChained(t *testing.T) {
	tests := []struct {
		name, created, reverted string
		setup                   func(t *testing.T) (*sql.DB, func() *httptest.ResponseRecorder)
	}{
		{"guided hire", "agent.hired", "agent.hire_reverted", func(t *testing.T) (*sql.DB, func() *httptest.ResponseRecorder) {
			db := setupTestDB(t)
			userID, wsID, _ := seedHireCrew(t, db, "guided", 5)
			return db, func() *httptest.ResponseRecorder {
				return postHire(t, newHireHandler(t, db), userID, wsID, "OWNER", covHire2Body(nil))
			}
		}},
		{"guided internal create", "agent.created", "agent.create_reverted", func(t *testing.T) (*sql.DB, func() *httptest.ResponseRecorder) {
			db, wsID, crewID, _, res := autonomyRig(t, "guided")
			return db, func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				gatedInternalHandler(t, db, res).CreateAgent(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
					`{"crew_id":"`+crewID+`","name":"Held"}`, wsID, crewID))
				return rr
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, call := tc.setup(t)
			execOrFatal(t, db, `CREATE TRIGGER block_inbox BEFORE INSERT ON inbox_items BEGIN SELECT RAISE(ABORT,'inbox unavailable'); END`)
			rr := call()
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", rr.Code, rr.Body.String())
			}
			var agents int
			if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE deleted_at IS NULL AND status='PENDING_REVIEW'`).Scan(&agents); err != nil {
				t.Fatal(err)
			}
			if agents != 0 {
				t.Fatalf("%d held agents left after the failed review row", agents)
			}
			for _, action := range []string{tc.created, tc.reverted} {
				var legacy, chained int
				if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action=? AND entity_type='AGENT'`, action).Scan(&legacy); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE json_extract(payload,'$.action')=?`, action).Scan(&chained); err != nil {
					t.Fatal(err)
				}
				if legacy != 1 || chained != 1 {
					t.Errorf("%s: audit_logs=%d chained=%d, want 1/1", action, legacy, chained)
				}
			}
		})
	}
}

// The compensating removal must not depend on the request still being alive:
// a client that disconnects after the agent was committed would otherwise
// leave it live with no revert record.
func TestRevertCreatedAgentSurvivesCanceledRequest(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	id := seedEphemeralAgent(t, db, wsID, crewID, "revert-me", nil, nil, "[x] hire: test")
	h := newHireHandler(t, db)
	req := httptest.NewRequest("POST", "/api/v1/agents/hire", nil)
	ctx, cancel := context.WithCancel(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	cancel()
	h.revertStagedHire(req.WithContext(ctx), wsID, id, "", userID, "test")
	var left, records int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id=?`, id).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='agent.hire_reverted' AND entity_id=?`, id).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if left != 0 || records != 1 {
		t.Fatalf("after a canceled request: agent rows=%d revert records=%d, want 0/1", left, records)
	}
}
