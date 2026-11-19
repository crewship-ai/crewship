package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// An authenticated endpoint remains an ENDPOINT_URL in the Vault. Its proxy
// projection must retain the real identity used by admission, audit and cost.
func TestEndpointProxySourceAdmission(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	r, _ := costRouter(t)
	db := r.db
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "endpoint-crew", ws, "Endpoint", "endpoint")
	agent := seedAgentRow(t, db, "endpoint-agent", ws, crew, "Endpoint", "endpoint", "AGENT")
	value := `{"baseURL":"https://fixture.example:38423/v1","apiKey":"synthetic-endpoint-token"}`
	enc, err := encryption.Encrypt(value)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES('endpoint-source',?,'endpoint-source',?,'ENDPOINT_URL','OLLAMA','ACTIVE','WORKSPACE',?)`, ws, enc, user)
	mustExec(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('endpoint-grant',?,'endpoint-source','OWN_ENDPOINT')`, agent)
	h := NewInternalHandler(db, bindTestMaster, testLogger())
	original := mcpCredEntry{ID: "endpoint-source", Type: CredTypeEndpointURL, EnvVar: "OWN_ENDPOINT", Value: value, SourceRevision: endpointSourceRevision(enc), AgentIDs: []string{agent}, AgentGrants: map[string]string{agent: ""}}
	ep := resolveLocalModelEndpoint(context.Background(), db, testLogger(), agent, ws, []mcpCredEntry{original})
	projected, added := h.appendProxiedEndpointCredential([]mcpCredEntry{original}, ep)
	if !added || len(projected) != 1 {
		t.Fatalf("authenticated endpoint not projected exactly once: %d entries", len(projected))
	}
	t.Run("real source identity replaces raw snapshot", func(t *testing.T) {
		p := projected[0]
		if p.ID != "endpoint-source" || p.Type != string(CredTypeAPIKey) || p.Provider != sidecarOpenAICompatProvider {
			t.Errorf("projection = %q/%q/%q; want real ID as proxy-only OPENAI_COMPAT", p.ID, p.Type, p.Provider)
		}
		if p.EnvVar != "" {
			t.Errorf("projection EnvVar = %q; the endpoint key must not enter agent env", p.EnvVar)
		}
		if p.AgentGrants == nil || p.SourceRevision != endpointSourceRevision(enc) {
			t.Error("effective grant snapshot or source revision lost")
		}
	})
	t.Run("native host admission records real source", func(t *testing.T) {
		code, body := admitEndpoint(t, r, h, ws, crew, agent, "endpoint-source", endpointSourceRevision(enc))
		if code != http.StatusOK {
			t.Fatalf("endpoint admission %d: %s", code, body)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id='endpoint-source' AND event_type=? AND agent_id=? AND metadata_json LIKE '%"run_id":"endpoint-run"%'`, string(AuditEventProxyAdmitted), agent).Scan(&n); err != nil || n == 0 {
			t.Fatalf("PROXY_ADMITTED for real source and agent missing: n=%d err=%v", n, err)
		}
	})
}

// admitEndpoint drives the real internal cost-admission handler the sidecar
// calls before forwarding an LLM request.
func admitEndpoint(t *testing.T, r *Router, h *InternalHandler, ws, crew, agent, cred, revision string) (int, string) {
	t.Helper()
	// An active journal run in the caller's scope: admission audits only an
	// unambiguous run it validated itself (#5518).
	mustExec(t, r.db, `INSERT OR IGNORE INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('endpoint-run-start',?,?,?,'run.started','orchestrator','fixture','{}','endpoint-run')`, ws, crew, agent)
	payload := `{"agent_id":"` + agent + `","credential_id":"` + cred + `","provider":"OPENAI_COMPAT","run_id":"endpoint-run"`
	if revision != "" {
		payload += `,"source_revision":"` + revision + `"`
	}
	req := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(payload+"}"))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(bindTestMaster, ws, crew))
	rr := httptest.NewRecorder()
	h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

// Admission re-resolves the selected endpoint inside its own transaction, so a
// stale or foreign sidecar snapshot cannot keep spending through an endpoint
// whose authority changed (#5542).
func TestEndpointProxyAdmissionAuthority(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	const value = `{"baseURL":"https://fixture.example:38423/v1","apiKey":"synthetic-endpoint-token"}`
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	cases := []struct {
		name string
		// setup seeds the endpoint row(s) and returns the credential ID the
		// sidecar presents and whether a correct revision is sent.
		setup        func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string
		revision     string // "ok", "stale" or ""
		rotation     string // "open", "expired", "cancelled": rotation row holding the stale value
		wantAdmitted bool
	}{
		{
			name: "assigned grant with current revision",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep"
			},
			revision: "ok", wantAdmitted: true,
		},
		{
			name: "stale revision after rotation",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep"
			},
			revision: "stale",
		},
		{
			name: "missing revision from an older sidecar",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep"
			},
		},
		{
			name: "expired grant",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name,expires_at) VALUES('g','`+agent+`','ep','OWN_ENDPOINT',?)`, past)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "revoked source",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				mustExec(t, r.db, `UPDATE credentials SET status='REVOKED' WHERE id='ep'`)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "workspace default",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				return "ep"
			},
			revision: "ok", wantAdmitted: true,
		},
		{
			name: "orphan CREW-scoped row is not a workspace default",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "CREW", user)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "CREW-scoped row linked to the agent's crew",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "CREW", user)
				mustExec(t, r.db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('ep', ?)`, crew)
				return "ep"
			},
			revision: "ok", wantAdmitted: true,
		},
		{
			name: "row linked only to a foreign crew",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "CREW", user)
				mustExec(t, r.db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('ep', ?)`, other)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "workspace row linked only to a foreign crew",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('ep', ?)`, other)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "previous revision inside an open rotation grace window",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep"
			},
			revision: "stale", rotation: "open", wantAdmitted: true,
		},
		{
			name: "previous revision after grace expired",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep"
			},
			revision: "stale", rotation: "expired",
		},
		{
			name: "previous revision after grace ended early",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep"
			},
			revision: "stale", rotation: "cancelled",
		},
		{
			name: "peer agent's per-agent grant",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				peer := seedAgentRow(t, r.db, "endpoint-peer", ws, crew, "Peer", "peer", "AGENT")
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g',?,'ep','PEER_ENDPOINT')`, peer)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "row bound only to a foreign crew",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO credential_bindings (id,workspace_id,credential_id,slot,scope,crew_id) VALUES ('b',?,'ep','FOREIGN_ENDPOINT','CREW',?)`, ws, other)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "handle-only workspace row",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `UPDATE credentials SET handle_only=1 WHERE id='ep'`)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "host-bound workspace default without approval",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `UPDATE credentials SET allowed_hosts='["approved.example"]' WHERE id='ep'`)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "host-bound assigned endpoint without approval",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				mustExec(t, r.db, `UPDATE credentials SET allowed_hosts='["approved.example"]' WHERE id='ep'`)
				return "ep"
			},
			revision: "ok",
		},
		{
			name: "presented ID is not the selected endpoint",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				seedEndpoint(t, r, "ep-other", ws, enc, "WORKSPACE", user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('g','`+agent+`','ep','OWN_ENDPOINT')`)
				return "ep-other"
			},
			revision: "ok",
		},
		{
			name: "assigned OPENAI_COMPAT key wins over the endpoint",
			setup: func(t *testing.T, r *Router, ws, crew, other, agent, user, enc string) string {
				seedEndpoint(t, r, "ep", ws, enc, "WORKSPACE", user)
				key, err := encryption.Encrypt(`{"baseURL":"https://gateway.example/v1","apiKey":"synthetic-compat"}`)
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, r.db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES('compat',?,'compat',?,'API_KEY','OPENAI_COMPAT','ACTIVE','WORKSPACE',?)`, ws, key, user)
				mustExec(t, r.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('gc','`+agent+`','compat','COMPAT_KEY')`)
				return "ep"
			},
			revision: "ok",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := costRouter(t)
			user := seedTestUser(t, r.db)
			ws := seedTestWorkspace(t, r.db, user)
			crew := seedCrewRow(t, r.db, "endpoint-crew", ws, "Endpoint", "endpoint")
			other := seedCrewRow(t, r.db, "foreign-crew", ws, "Foreign", "foreign")
			agent := seedAgentRow(t, r.db, "endpoint-agent", ws, crew, "Endpoint", "endpoint", "AGENT")
			enc, err := encryption.Encrypt(value)
			if err != nil {
				t.Fatal(err)
			}
			cred := tc.setup(t, r, ws, crew, other, agent, user, enc)
			old, err := encryption.Encrypt(`{"baseURL":"https://old.example/v1","apiKey":"synthetic-old"}`)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			switch tc.rotation {
			case "open":
				mustExec(t, r.db, `INSERT INTO credential_rotations (id,credential_id,old_value,grace_seconds,rotated_at,expires_at,rotated_by,status) VALUES ('rot',?,?,3600,?,?,?,'ACTIVE')`, cred, old, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), user)
			case "expired":
				mustExec(t, r.db, `INSERT INTO credential_rotations (id,credential_id,old_value,grace_seconds,rotated_at,expires_at,rotated_by,status) VALUES ('rot',?,?,60,?,?,?,'ACTIVE')`, cred, old, now.Add(-2*time.Hour).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339), user)
			case "cancelled":
				mustExec(t, r.db, `INSERT INTO credential_rotations (id,credential_id,old_value,grace_seconds,rotated_at,expires_at,rotated_by,status) VALUES ('rot',?,'',3600,?,?,?,'CANCELLED')`, cred, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), user)
			}
			revision := ""
			switch tc.revision {
			case "ok":
				revision = endpointSourceRevision(enc)
			case "stale":
				revision = endpointSourceRevision(old)
			}
			h := NewInternalHandler(r.db, bindTestMaster, testLogger())
			code, body := admitEndpoint(t, r, h, ws, crew, agent, cred, revision)
			if tc.wantAdmitted && code != http.StatusOK {
				t.Fatalf("want admitted, got %d: %s", code, body)
			}
			if !tc.wantAdmitted && code != http.StatusForbidden {
				t.Fatalf("want 403 refusal, got %d: %s", code, body)
			}
			var audited int
			if err := r.db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id=? AND event_type=?`, cred, string(AuditEventProxyAdmitted)).Scan(&audited); err != nil {
				t.Fatal(err)
			}
			if tc.wantAdmitted != (audited > 0) {
				t.Errorf("PROXY_ADMITTED rows = %d; want admitted=%v", audited, tc.wantAdmitted)
			}
		})
	}
}

func seedEndpoint(t *testing.T, r *Router, id, ws, enc, scope, user string) {
	t.Helper()
	mustExec(t, r.db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES(?,?,?,?,'ENDPOINT_URL','OLLAMA','ACTIVE',?,?)`, id, ws, id, enc, scope, user)
}

// The sidecar reaper drops every boot-time CredStore entry the metadata
// listing does not return. The endpoint projection carries the real
// ENDPOINT_URL ID (#5542), so that ID must be listed, or endpoint traffic
// stops at the first reap tick. The plaintext pool must still exclude it.
func TestListCredentials_EndpointProjectionSurvivesReaper(t *testing.T) {
	h, db, userID, wsID := covICRig(t)
	// A real ciphertext, so exclusion from the value pool is the type gate
	// and not a decrypt failure.
	enc, err := encryption.Encrypt(`{"baseURL":"https://fixture.example/v1","apiKey":"synthetic-endpoint-token"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('reap-ep', ?, 'reap-ep', ?, 'ENDPOINT_URL', 'OLLAMA', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`, wsID, enc, userID); err != nil {
		t.Fatal(err)
	}
	list := func(query string) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID+query, nil)
		req.RemoteAddr = "127.0.0.1:9999"
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, row := range out {
			id, _ := row["id"].(string)
			ids[id] = true
		}
		return ids
	}
	if !list("")["reap-ep"] {
		t.Fatal("metadata listing omits the ENDPOINT_URL ID; the reaper would evict the projection")
	}
	if list("&include_values=true")["reap-ep"] {
		t.Fatal("ENDPOINT_URL entered the plaintext value pool")
	}
}
