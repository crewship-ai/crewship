package api

// TestResolveAgentConfig_PrivilegedCrew_FailClosedCredentials is issue
// #1032: a --privileged crew container drops no-new-privileges +
// CapDrop:ALL, collapsing the UID 1001 (agent) / 1002 (sidecar) boundary
// that normally keeps a compromised agent from reading the sidecar's
// process memory — and with it any credentials loaded into its CredStore.
// The agent-config resolver must fail CLOSED (omit credentials entirely)
// for a privileged crew unless the workspace has explicitly opted in via
// workspaces.allow_privileged_credentials.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestResolveAgentConfig_PrivilegedCrew_FailClosedCredentials(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-priv", wsID, "Privileged Crew", "privileged-crew")
	if _, err := db.Exec(`UPDATE crews SET cached_requirements = '{"privileged":true}' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("mark crew privileged: %v", err)
	}
	agentID := seedAgentRow(t, db, "ag-priv", wsID, crewID, "Ada", "ada", "AGENT")
	covCfgEncCred(t, db, wsID, userID, "cred1", "Anthropic Key", "ANTHROPIC_API_KEY", "", "sk-test-secret")
	covCfgAssignCred(t, db, "ac1", agentID, "cred1", "ANTHROPIC_API_KEY", 0)

	resolve := func() []any {
		req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
		w := httptest.NewRecorder()
		h.resolveAgentConfig(w, req, agentID)
		if w.Code != 200 {
			t.Fatalf("resolveAgentConfig status = %d, body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		creds, _ := resp["credentials"].([]any)
		return creds
	}

	t.Run("workspace_not_opted_in_creds_empty", func(t *testing.T) {
		if creds := resolve(); len(creds) != 0 {
			t.Fatalf("expected 0 credentials for a privileged crew without opt-in, got %d: %+v", len(creds), creds)
		}
	})

	t.Run("workspace_opted_in_creds_present", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE workspaces SET allow_privileged_credentials = 1 WHERE id = ?`, wsID); err != nil {
			t.Fatalf("opt in workspace: %v", err)
		}
		if creds := resolve(); len(creds) == 0 {
			t.Fatalf("expected credentials once the workspace opted in, got 0")
		}
	})
}

// TestResolveAgentConfig_PrivilegedCrew_MCPServerCredentialsAlsoBlocked is a
// gap the initial #1032 fix missed: resolveAgentMCPServers has its OWN
// independent credential path (agent_mcp_bindings.credential_id → decrypted
// into mcpServerEntry.CredToken), completely separate from the
// agent_credentials path the fail-closed gate covers. That token flows
// downstream into the orchestrator's MCPServerConfig.Credential and from
// there straight into the sidecar's mcp_servers input payload — the exact
// "credentials loaded into the sidecar" #1032 exists to prevent. A
// privileged crew without the opt-in must have cred_token/cred_type/
// cred_header stripped from every mcp_servers entry too, not just from the
// top-level "credentials" array.
func TestResolveAgentConfig_PrivilegedCrew_MCPServerCredentialsAlsoBlocked(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-priv-mcp", wsID, "Privileged Crew", "privileged-crew-mcp")
	if _, err := db.Exec(`UPDATE crews SET cached_requirements = '{"privileged":true}' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("mark crew privileged: %v", err)
	}
	agentID := seedAgentRow(t, db, "ag-priv-mcp", wsID, crewID, "Ada", "ada", "AGENT")
	covCfgEncCred(t, db, wsID, userID, "cred1", "GitHub Token", "SECRET", "", "ghp_test_secret")
	covCfgSeedWSServer(t, db, "srv1", wsID, "github", "streamable-http", "https://api.github.com/mcp", "", "", "")
	covCfgBindServer(t, db, "bind1", agentID, "srv1", "workspace", "cred1", "bearer", "", 1)

	resolve := func() []map[string]any {
		req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
		w := httptest.NewRecorder()
		h.resolveAgentConfig(w, req, agentID)
		if w.Code != 200 {
			t.Fatalf("resolveAgentConfig status = %d, body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			MCPServers []map[string]any `json:"mcp_servers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp.MCPServers
	}

	t.Run("workspace_not_opted_in_mcp_cred_stripped", func(t *testing.T) {
		servers := resolve()
		if len(servers) == 0 {
			t.Fatal("expected the MCP server definition to still be listed (just without credentials)")
		}
		for _, s := range servers {
			if tok, ok := s["cred_token"]; ok && tok != "" {
				t.Errorf("expected cred_token stripped for a privileged crew without opt-in, got %v in %+v", tok, s)
			}
		}
	})

	t.Run("workspace_opted_in_mcp_cred_present", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE workspaces SET allow_privileged_credentials = 1 WHERE id = ?`, wsID); err != nil {
			t.Fatalf("opt in workspace: %v", err)
		}
		servers := resolve()
		found := false
		for _, s := range servers {
			if tok, _ := s["cred_token"].(string); tok != "" {
				found = true
			}
		}
		if !found {
			t.Fatal("expected cred_token present once the workspace opted in")
		}
	})
}

// TestResolveAgentConfig_NonPrivilegedCrew_CredentialsUnaffected pins the
// non-regression: a normal (non-privileged) crew must keep resolving
// credentials exactly as before, regardless of the workspace flag.
func TestResolveAgentConfig_NonPrivilegedCrew_CredentialsUnaffected(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-normal", wsID, "Normal Crew", "normal-crew")
	agentID := seedAgentRow(t, db, "ag-normal", wsID, crewID, "Ada", "ada", "AGENT")
	covCfgEncCred(t, db, wsID, userID, "cred1", "Anthropic Key", "ANTHROPIC_API_KEY", "", "sk-test-secret")
	covCfgAssignCred(t, db, "ac1", agentID, "cred1", "ANTHROPIC_API_KEY", 0)

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	w := httptest.NewRecorder()
	h.resolveAgentConfig(w, req, agentID)
	if w.Code != 200 {
		t.Fatalf("resolveAgentConfig status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	creds, _ := resp["credentials"].([]any)
	if len(creds) == 0 {
		t.Fatalf("expected credentials for a non-privileged crew, got 0")
	}
}
