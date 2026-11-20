package api

// #5529: deleting an agent left its agent_credentials rows behind. They kept
// counting in `credential list` and could not be removed, because every agent
// lookup answers "agent not found" for a tombstoned agent.
//
// These drive the real delete paths that tombstone agents (agent delete, crew
// delete, workspace delete) and assert on the rows: the deleted agents' grants
// are gone and audited, a bystander's grant on the SAME credential stays, and
// the credential itself — row, material, tombstone — is untouched.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const revokeTestMaterial = "opaque-ciphertext-must-survive"

// seedGrantRevokeFixture builds one workspace with two crews:
//
//	crew-doomed:  agent-doomed (standing grant + a lease), agent-doomed-peer (standing grant)
//	crew-keep:    agent-bystander (standing grant on the same credential)
//
// and returns the db, user id and workspace id.
func seedGrantRevokeFixture(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	seedCrewRow(t, db, "crew-doomed", wsID, "Doomed", "doomed")
	seedCrewRow(t, db, "crew-keep", wsID, "Keep", "keep")
	seedAgentRow(t, db, "agent-doomed", wsID, "crew-doomed", "Doomed", "doomed-agent", "LEAD")
	seedAgentRow(t, db, "agent-doomed-peer", wsID, "crew-doomed", "Peer", "doomed-peer", "AGENT")
	seedAgentRow(t, db, "agent-bystander", wsID, "crew-keep", "Bystander", "bystander", "LEAD")

	for _, id := range []string{"cred-shared", "cred-lease"} {
		if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
			id, wsID, "NAME_"+id, revokeTestMaterial, userID); err != nil {
			t.Fatalf("seed credential %s: %v", id, err)
		}
	}
	lease := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	for _, g := range []struct {
		id, agent, cred, env string
		expires              any
		source               any
	}{
		{"ac-doomed-standing", "agent-doomed", "cred-shared", "ANTHROPIC_API_KEY", nil, nil},
		{"ac-doomed-lease", "agent-doomed", "cred-lease", "CLAUDE_CODE_OAUTH_TOKEN", lease, leaseSourceManual},
		{"ac-peer", "agent-doomed-peer", "cred-shared", "ANTHROPIC_API_KEY", nil, nil},
		{"ac-bystander", "agent-bystander", "cred-shared", "ANTHROPIC_API_KEY", nil, nil},
	} {
		if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at, expires_at, lease_source)
			VALUES (?, ?, ?, ?, 0, datetime('now'), ?, ?)`, g.id, g.agent, g.cred, g.env, g.expires, g.source); err != nil {
			t.Fatalf("seed grant %s: %v", g.id, err)
		}
	}
	return db, userID, wsID
}

func grantExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("read grant %s: %v", id, err)
	}
	return n == 1
}

// revokeAuditFor returns the REVOKE audit metadata recorded for an assignment.
func revokeAuditFor(t *testing.T, db *sql.DB, assignmentID string) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT COALESCE(agent_id,''), COALESCE(metadata_json,'') FROM credential_audit
		WHERE event_type = 'REVOKE' AND json_valid(metadata_json)
		  AND json_extract(metadata_json, '$.assignment_id') = ?`, assignmentID)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var agentID, raw string
		if err := rows.Scan(&agentID, &raw); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("decode audit metadata: %v", err)
		}
		m["_agent_id"] = agentID
		out = append(out, m)
	}
	return out
}

func assertCredentialMaterialIntact(t *testing.T, db *sql.DB, credID string) {
	t.Helper()
	var value string
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT encrypted_value, deleted_at FROM credentials WHERE id = ?`, credID).Scan(&value, &deletedAt); err != nil {
		t.Fatalf("credential %s vanished: %v — revoking a grant must never delete the credential", credID, err)
	}
	if value != revokeTestMaterial {
		t.Errorf("credential %s material changed to %q — revoking a grant must never touch it", credID, value)
	}
	if deletedAt.Valid {
		t.Errorf("credential %s was soft-deleted (%s) — only the grant may go", credID, deletedAt.String)
	}
}

func TestDeletingAgentsRevokesOnlyTheirCredentialGrants(t *testing.T) {
	tests := []struct {
		name    string
		del     func(t *testing.T, db *sql.DB, userID, wsID string)
		reason  string
		revoked []string // assignment ids that must be gone and audited
		kept    []string // assignment ids that must survive
	}{
		{
			name: "agent delete",
			del: func(t *testing.T, db *sql.DB, userID, wsID string) {
				h := NewAgentHandler(db, newTestLogger())
				req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/agent-doomed", nil)
				req.SetPathValue("agentId", "agent-doomed")
				rr := httptest.NewRecorder()
				h.Delete(rr, withWorkspaceUser(req, userID, wsID, "OWNER"))
				if rr.Code != http.StatusOK {
					t.Fatalf("agent delete = %d %s", rr.Code, rr.Body.String())
				}
			},
			reason:  grantRevokeReasonAgentDeleted,
			revoked: []string{"ac-doomed-standing", "ac-doomed-lease"},
			// The crew mate on the same credential is not this delete's business.
			kept: []string{"ac-peer", "ac-bystander"},
		},
		{
			name: "crew delete",
			del: func(t *testing.T, db *sql.DB, userID, wsID string) {
				h := NewCrewHandler(db, newTestLogger())
				rr := httptest.NewRecorder()
				h.Delete(rr, crewDeleteReq(userID, wsID, "crew-doomed"))
				if rr.Code != http.StatusOK {
					t.Fatalf("crew delete = %d %s", rr.Code, rr.Body.String())
				}
			},
			reason:  grantRevokeReasonCrewDeleted,
			revoked: []string{"ac-doomed-standing", "ac-doomed-lease", "ac-peer"},
			kept:    []string{"ac-bystander"},
		},
		{
			name: "workspace delete",
			del: func(t *testing.T, db *sql.DB, _, wsID string) {
				ctx := context.Background()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := softDeleteWorkspaceTx(ctx, tx, wsID, time.Now().UTC().Format(time.RFC3339)); err != nil {
					t.Fatalf("softDeleteWorkspaceTx: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			},
			reason:  grantRevokeReasonWorkspaceDeleted,
			revoked: []string{"ac-doomed-standing", "ac-doomed-lease", "ac-peer", "ac-bystander"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, userID, wsID := seedGrantRevokeFixture(t)
			tc.del(t, db, userID, wsID)

			for _, id := range tc.revoked {
				if grantExists(t, db, id) {
					t.Errorf("grant %s outlived its deleted agent (#5529): it keeps counting in `credential list` and nothing can remove it", id)
				}
				audits := revokeAuditFor(t, db, id)
				if len(audits) != 1 {
					t.Errorf("grant %s: %d REVOKE audit rows, want exactly 1", id, len(audits))
					continue
				}
				if audits[0]["reason"] != tc.reason || audits[0]["grant_source"] != "explicit" || audits[0]["_agent_id"] == "" {
					t.Errorf("grant %s: audit = %v, want reason %q, grant_source explicit and the agent id", id, audits[0], tc.reason)
				}
			}
			for _, id := range tc.kept {
				if !grantExists(t, db, id) {
					t.Errorf("grant %s belongs to a live agent and was removed anyway", id)
				}
				if n := len(revokeAuditFor(t, db, id)); n != 0 {
					t.Errorf("grant %s: %d REVOKE audit rows for a grant that was not revoked", id, n)
				}
			}
			if lease := revokeAuditFor(t, db, "ac-doomed-lease"); len(lease) == 1 && lease[0]["lease_source"] != leaseSourceManual {
				t.Errorf("lease revoke audit = %v, want lease_source %q recorded", lease[0], leaseSourceManual)
			}
			assertCredentialMaterialIntact(t, db, "cred-shared")
			assertCredentialMaterialIntact(t, db, "cred-lease")
		})
	}
}

// The helper is the guard, not just the callers: it refuses to touch a live
// agent's grants or another workspace's, whatever scope it is handed.
func TestRevokeDeletedAgentCredentialGrantsTx_NeverTouchesLiveOrForeignAgents(t *testing.T) {
	db, _, wsID := seedGrantRevokeFixture(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		workspace string
		scope     agentGrantScope
		scopeID   string
	}{
		{"live agent by id", wsID, agentGrantScopeAgent, "agent-doomed"},
		{"live crew", wsID, agentGrantScopeCrew, "crew-doomed"},
		{"live workspace", wsID, agentGrantScopeWorkspace, wsID},
		{"foreign workspace naming our agent", "ws-other", agentGrantScopeAgent, "agent-doomed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			got, err := revokeDeletedAgentCredentialGrantsTx(context.Background(), tx, tc.workspace, tc.scope, tc.scopeID, "test", "")
			if err != nil {
				t.Fatalf("revoke: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("revoked %v from live agents", got)
			}
		})
	}
	for _, id := range []string{"ac-doomed-standing", "ac-doomed-lease", "ac-peer", "ac-bystander"} {
		if !grantExists(t, db, id) {
			t.Errorf("grant %s of a live agent was removed", id)
		}
	}

	// A tombstoned agent in ANOTHER workspace is out of reach too.
	if _, err := db.Exec(`UPDATE agents SET deleted_at = ? WHERE id = 'agent-doomed'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if got, err := revokeDeletedAgentCredentialGrantsTx(context.Background(), tx, "ws-other", agentGrantScopeAgent, "agent-doomed", "test", ""); err != nil || len(got) != 0 {
		t.Errorf("foreign workspace revoked %v (err %v)", got, err)
	}
	if _, err := revokeDeletedAgentCredentialGrantsTx(context.Background(), tx, wsID, agentGrantScope("1=1 OR ?"), "x", "test", ""); err == nil {
		t.Error("an unknown scope predicate was accepted")
	}
}

// Before: assign, delete the agent, and `credential list` still counted the
// grant. The count is what the issue reported, so it is asserted through the
// list handler rather than on the table.
func TestCredentialListCountDropsWhenAgentDeleted(t *testing.T) {
	db, userID, wsID := seedGrantRevokeFixture(t)
	count := func() int {
		t.Helper()
		h := NewCredentialHandler(db, newTestLogger())
		req := httptest.NewRequest(http.MethodGet, "/api/v1/credentials", nil)
		rr := httptest.NewRecorder()
		h.List(rr, withWorkspaceUser(req, userID, wsID, "OWNER"))
		if rr.Code != http.StatusOK {
			t.Fatalf("credential list = %d %s", rr.Code, rr.Body.String())
		}
		var list []struct {
			ID         string `json:"id"`
			AgentCount int    `json:"_count_agent_credentials"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode list: %v %s", err, rr.Body.String())
		}
		for _, c := range list {
			if c.ID == "cred-shared" {
				return c.AgentCount
			}
		}
		t.Fatalf("cred-shared not listed: %s", rr.Body.String())
		return 0
	}
	if got := count(); got != 3 {
		t.Fatalf("baseline count = %d, want 3", got)
	}
	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/agent-doomed", nil)
	req.SetPathValue("agentId", "agent-doomed")
	rr := httptest.NewRecorder()
	h.Delete(rr, withWorkspaceUser(req, userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("agent delete = %d %s", rr.Code, rr.Body.String())
	}
	if got := count(); got != 2 {
		t.Errorf("count after deleting one grantee = %d, want 2 (#5529)", got)
	}
}
