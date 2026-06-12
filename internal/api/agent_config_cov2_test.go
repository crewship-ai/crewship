package api

// Second coverage pass for agent_config.go — the resolver branches the
// first pass skipped: the crew/agent JSON-blob auto-migration inside
// resolveAgentConfigWithOpener, the credential-decrypt 500, the table-based
// MCP env auto-resolve loop, and the [KEEPER] block injection for SECRET
// credentials.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func covCfg2Rig(t *testing.T) (h *InternalHandler, wsID, crewID, agentID string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "crew-cfg2", wsID, "CFG2", "cfg2")
	agentID = seedAgentRow(t, db, "agent-cfg2", wsID, crewID, "Cfg", "cfg2-agent", "AGENT")
	h = NewInternalHandler(db, "tok", newTestLogger())
	return
}

func covCfg2Resolve(t *testing.T, h *InternalHandler, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/internal/agents/"+agentID+"/resolve", nil)
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	h.ResolveAgent(rr, req)
	return rr
}

func TestCfg2_Resolve_MigratesJSONBlobsToTables(t *testing.T) {
	h, _, crewID, agentID := covCfg2Rig(t)

	crewBlob := `{"mcpServers":{"linear":{"command":"npx","args":["-y","linear-mcp"],"env":{"LINEAR_API_KEY":"${LINEAR_API_KEY}"}}}}`
	agentBlob := `{"mcpServers":{"gh":{"command":"npx","args":["-y","gh-mcp"],"env":{"GITHUB_TOKEN":"${GITHUB_TOKEN}"}}}}`
	if _, err := h.db.Exec(`UPDATE crews SET mcp_config_json = ? WHERE id = ?`, crewBlob, crewID); err != nil {
		t.Fatalf("set crew blob: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE agents SET mcp_config_json = ? WHERE id = ?`, agentBlob, agentID); err != nil {
		t.Fatalf("set agent blob: %v", err)
	}

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	// Blobs migrated into the integration tables.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = ? AND name = 'linear'`, crewID).Scan(&n); err != nil || n != 1 {
		t.Errorf("crew_mcp_servers linear rows = %d (err=%v), want 1", n, err)
	}
	// The agent blob also lands in crew_mcp_servers, with a binding row.
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = ? AND name = 'gh'`, crewID).Scan(&n); err != nil || n != 1 {
		t.Errorf("crew_mcp_servers gh rows = %d (err=%v), want 1", n, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE agent_id = ?`, agentID).Scan(&n); err != nil || n < 1 {
		t.Errorf("agent_mcp_bindings rows = %d (err=%v), want >= 1", n, err)
	}

	// The response must list the migrated servers (table-based path) and
	// blank out the legacy blob fields.
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := resp["crew_mcp_config_json"].(string); got != "" {
		t.Errorf("crew_mcp_config_json = %q, want empty after migration", got)
	}
	servers, _ := resp["mcp_servers"].([]any)
	if len(servers) < 2 {
		t.Errorf("mcp_servers = %d entries, want >= 2 (migrated linear + gh)", len(servers))
	}
}

func TestCfg2_Resolve_CredentialDecryptError500(t *testing.T) {
	h, _, _, agentID := covCfg2Rig(t)
	// Break only agent_credentials: loadAgentData (agents/crews) still
	// works, so the failure surfaces from resolveAgentCredentials.
	if _, err := h.db.Exec(`ALTER TABLE agent_credentials RENAME TO ac_hidden_cfg2`); err != nil {
		t.Fatalf("rename agent_credentials: %v", err)
	}
	t.Cleanup(func() { _, _ = h.db.Exec(`ALTER TABLE ac_hidden_cfg2 RENAME TO agent_credentials`) })

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCfg2_Resolve_KeeperBlockForSecretCreds(t *testing.T) {
	h, wsID, _, agentID := covCfg2Rig(t)
	h.SetKeeperEnabled(true)

	enc, err := encryption.Encrypt("super-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-cfg2-sec', ?, 'ProdKey', ?, 'SECRET', 'CUSTOM', 'ACTIVE', 'test-user-id')`,
		wsID, enc); err != nil {
		t.Fatalf("seed cred: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('ac-cfg2-sec', ?, 'cr-cfg2-sec', 'PROD_KEY', 1)`, agentID); err != nil {
		t.Fatalf("seed agent_credentials: %v", err)
	}

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	prompt, _ := resp["system_prompt"].(string)
	if !strings.Contains(prompt, "KEEPER") || !strings.Contains(prompt, "/keeper/request") {
		t.Errorf("system_prompt missing KEEPER block")
	}
}

func TestCfg2_Resolve_TableServerEnvAutoResolve(t *testing.T) {
	h, wsID, crewID, agentID := covCfg2Rig(t)

	// A SECRET credential whose name matches the env var prefix so the
	// table-based env auto-resolve finds it.
	enc, err := encryption.Encrypt("xoxb-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-cfg2-slack', ?, 'slack-bot-token', ?, 'API_KEY', 'CUSTOM', 'ACTIVE', 'test-user-id')`,
		wsID, enc); err != nil {
		t.Fatalf("seed cred: %v", err)
	}
	// Crew-scoped MCP server whose env references ${SLACK_BOT_TOKEN}.
	if _, err := h.db.Exec(`
		INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, command, env_json, enabled)
		VALUES ('cms-cfg2', ?, 'slack', 'Slack', 'stdio', 'npx', '{"SLACK_BOT_TOKEN":"${SLACK_BOT_TOKEN}"}', 1)`,
		crewID); err != nil {
		t.Fatalf("seed crew_mcp_servers: %v", err)
	}

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	creds, _ := resp["credentials"].([]any)
	found := false
	for _, c := range creds {
		if m, ok := c.(map[string]any); ok && m["env_var"] == "SLACK_BOT_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Errorf("SLACK_BOT_TOKEN not auto-resolved from table-based MCP env; creds=%v", creds)
	}
}
