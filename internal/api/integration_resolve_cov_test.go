package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// integration_resolve_cov_test.go covers the cascade resolution
// branches of ResolveAgentIntegrations: the three query-error 500s
// (forced by renaming exactly the table each step reads) and the full
// cascade semantics — crew overrides workspace by name, bindings
// disable/attach credentials/override config, and opt-in filtering
// hides servers bound to other agents only. Helpers: covIRes.

func covIResFixture(t *testing.T) (*IntegrationHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covires-crew", wsID, "Crew", "covires-crew")
	agentID := seedAgentRow(t, db, "covires-ag", wsID, crewID, "Agent", "covires-ag", "AGENT")
	return NewIntegrationHandler(db, newTestLogger()), userID, wsID, crewID, agentID
}

func covIResGet(h *IntegrationHandler, userID, wsID, agentID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/resolved-integrations", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	h.ResolveAgentIntegrations(rr, req)
	return rr
}

func TestCovIRes_WorkspaceServersQueryError_500(t *testing.T) {
	h, userID, wsID, _, agentID := covIResFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE workspace_mcp_servers RENAME TO wms_broken`)
	rr := covIResGet(h, userID, wsID, agentID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIRes_CrewServersQueryError_500(t *testing.T) {
	h, userID, wsID, _, agentID := covIResFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE crew_mcp_servers RENAME TO cms_broken`)
	rr := covIResGet(h, userID, wsID, agentID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIRes_BindingsQueryError_500(t *testing.T) {
	h, userID, wsID, _, agentID := covIResFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE agent_mcp_bindings RENAME TO amb_broken`)
	rr := covIResGet(h, userID, wsID, agentID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovIRes_CascadeOverrideBindingAndOptIn drives the full merge:
//   - "github": workspace-level, overridden by a crew-level server of
//     the same name → resolved Scope must be "crew";
//   - "search": workspace-level with a binding that attaches a
//     credential and a config override → both must surface;
//   - "muted": workspace-level with a binding that disables it → gone;
//   - "private": workspace-level bound ONLY to another agent → opt-in
//     filtering hides it from this agent.
func TestCovIRes_CascadeOverrideBindingAndOptIn(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID, crewID, agentID := covIResFixture(t)
	db := h.db
	otherAgent := seedAgentRow(t, db, "covires-other", wsID, crewID, "Other", "covires-other", "AGENT")

	covCfgSeedWSServer(t, db, "covires-ws-github", wsID, "github", "stdio", "", "npx github", "", "")
	covCfgSeedCrewServer(t, db, "covires-crew-github", crewID, "github", "streamable-http", "https://crew.example/mcp")
	covCfgSeedWSServer(t, db, "covires-ws-search", wsID, "search", "stdio", "", "npx search", "", "")
	covCfgSeedWSServer(t, db, "covires-ws-muted", wsID, "muted", "stdio", "", "npx muted", "", "")
	covCfgSeedWSServer(t, db, "covires-ws-private", wsID, "private", "stdio", "", "npx private", "", "")

	seedCredentialEnc(t, db, wsID, userID, "covires-cred", "covires-cred-name", "sk-test")
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, config_override_json)
		VALUES ('covires-b1', ?, 'covires-ws-search', 'workspace', 'covires-cred', 1, '{"timeout":5}')`,
		agentID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES ('covires-b2', ?, 'covires-ws-muted', 'workspace', 0)`, agentID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES ('covires-b3', ?, 'covires-ws-private', 'workspace', 1)`, otherAgent)

	rr := covIResGet(h, userID, wsID, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resolved []ResolvedIntegration
	if err := json.Unmarshal(rr.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]ResolvedIntegration{}
	for _, s := range resolved {
		byName[s.Name] = s
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved %d servers (%v), want 2 (github overridden + search)", len(resolved), byName)
	}

	gh, ok := byName["github"]
	if !ok || gh.Scope != "crew" || gh.ServerID != "covires-crew-github" {
		t.Errorf("github = %+v, want crew-scope override", gh)
	}
	se, ok := byName["search"]
	if !ok {
		t.Fatalf("search missing from %v", byName)
	}
	if se.CredentialID == nil || *se.CredentialID != "covires-cred" {
		t.Errorf("search credential = %v, want covires-cred", se.CredentialID)
	}
	if se.CredName == nil || *se.CredName != "covires-cred-name" {
		t.Errorf("search cred name = %v, want covires-cred-name", se.CredName)
	}
	if se.ConfigJSON == nil || *se.ConfigJSON != `{"timeout":5}` {
		t.Errorf("search config = %v, want binding override", se.ConfigJSON)
	}
	if _, present := byName["muted"]; present {
		t.Errorf("muted server resolved despite disabled binding")
	}
	if _, present := byName["private"]; present {
		t.Errorf("private server resolved for agent without binding (opt-in filter broken)")
	}
}

func TestCovIRes_AgentNotFound_404(t *testing.T) {
	h, userID, wsID, _, _ := covIResFixture(t)
	rr := covIResGet(h, userID, wsID, "no-such-agent")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
