package api

// Tests for the COMPOSIO_DEFAULT_CONNECTOR behaviour layered onto
// resolveAgentMCPServers: legacy gating + default-connector injection. The
// flag-OFF path is asserted byte-for-byte unchanged; flag-ON exercises the
// four documented cases.

import (
	"database/sql"
	"net/http/httptest"
	"testing"
)

// dcSeedComposioWSServer seeds a workspace MCP server with icon='composio'
// (the marker the legacy gate / binding detection keys off).
func dcSeedComposioWSServer(t *testing.T, db *sql.DB, id, wsID, name, endpoint string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, icon, enabled)
		VALUES (?, ?, ?, ?, 'streamable-http', ?, 'composio', 1)`,
		id, wsID, name, "Display "+name, endpoint); err != nil {
		t.Fatalf("dcSeedComposioWSServer %s: %v", id, err)
	}
}

// dcSeedDefaults writes composio_settings with the default user/server pinned.
// encrypted_api_key is NOT NULL in the schema; the resolver never reads it
// (it reads the composio-managed-key credential), so a dummy is fine.
func dcSeedDefaults(t *testing.T, db *sql.DB, wsID, userID, serverID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO composio_settings
		(workspace_id, encrypted_api_key, base_url, default_user_id, default_mcp_server_id, created_at, updated_at)
		VALUES (?, 'x', '', ?, ?, datetime('now'), datetime('now'))`,
		wsID, userID, serverID); err != nil {
		t.Fatalf("dcSeedDefaults: %v", err)
	}
}

func dcByName(servers []mcpServerEntry) map[string]mcpServerEntry {
	m := map[string]mcpServerEntry{}
	for _, s := range servers {
		m[s.Name] = s
	}
	return m
}

// flag OFF → default config present but resolution unchanged (no injection,
// legacy NOT gated).
func TestDefaultConnector_FlagOff_Unchanged(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db) // flag defaults to false
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agOff", wsID, "", "Off", "off", "AGENT")

	// Legacy (non-composio) open workspace server + a configured default.
	covCfgSeedWSServer(t, db, "lg1", wsID, "legacy", "streamable-http", "https://l.example.com", "", "", "")
	dcSeedDefaults(t, db, wsID, "user-x", "mcp_x")
	covCfgEncCred(t, db, wsID, userID, "mkc", composioManagedKeyName, "API_KEY", "", "ak_live")

	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, agentID)
	got := dcByName(h.resolveAgentMCPServers(req, d, agentID))

	if _, ok := got["legacy"]; !ok {
		t.Errorf("flag OFF: legacy server should still resolve")
	}
	if _, ok := got["composio-default"]; ok {
		t.Errorf("flag OFF: must NOT inject composio-default")
	}
}

// flag ON + default configured + unbound agent → gets composio-default.
func TestDefaultConnector_FlagOn_InjectsDefault(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	h.SetComposioDefaultConnector(true, "https://base.example.com")
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agOn", wsID, "", "On", "on", "AGENT")

	dcSeedDefaults(t, db, wsID, "user-d", "mcp_srv_d")
	covCfgEncCred(t, db, wsID, userID, "mkc", composioManagedKeyName, "API_KEY", "", "ak_live")

	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, agentID)
	got := dcByName(h.resolveAgentMCPServers(req, d, agentID))

	def, ok := got["composio-default"]
	if !ok {
		t.Fatalf("expected composio-default entry, got %+v", got)
	}
	if def.CredType != "api_key" || def.CredHeader != "x-api-key" {
		t.Errorf("default cred shape = type=%q header=%q, want api_key/x-api-key", def.CredType, def.CredHeader)
	}
	if def.CredToken != "ak_live" {
		t.Errorf("default cred token = %q, want decrypted ak_live", def.CredToken)
	}
	if def.Transport != "streamable-http" || def.DisplayName != "Composio" {
		t.Errorf("default entry = transport=%q display=%q", def.Transport, def.DisplayName)
	}
	wantEndpoint := "https://base.example.com/v3/mcp/mcp_srv_d/mcp?user_id=user-d"
	if def.Endpoint == nil || *def.Endpoint != wantEndpoint {
		t.Errorf("default endpoint = %v, want %q", def.Endpoint, wantEndpoint)
	}
}

// flag ON + agent WITH a per-agent composio binding → only its scoped server,
// the default is suppressed (binding overrides).
func TestDefaultConnector_FlagOn_PerAgentBindingOverrides(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	h.SetComposioDefaultConnector(true, "https://base.example.com")
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agBound", wsID, "", "Bound", "bound", "AGENT")

	dcSeedDefaults(t, db, wsID, "user-d", "mcp_srv_d")
	covCfgEncCred(t, db, wsID, userID, "mkc", composioManagedKeyName, "API_KEY", "", "ak_live")

	// Per-agent composio binding: workspace server named composio-<agentID>-gmail.
	srvName := "composio-" + agentID + "-gmail"
	dcSeedComposioWSServer(t, db, "cws1", wsID, srvName, "https://base.example.com/v3/mcp/scoped/mcp?user_id=user-d")
	covCfgBindServer(t, db, "cb1", agentID, "cws1", "workspace", "mkc", "api_key", "", 1)

	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, agentID)
	got := dcByName(h.resolveAgentMCPServers(req, d, agentID))

	if _, ok := got[srvName]; !ok {
		t.Errorf("expected scoped per-agent binding %s present", srvName)
	}
	if _, ok := got["composio-default"]; ok {
		t.Errorf("per-agent binding must suppress the default connector")
	}
}

// flag ON → legacy (icon != 'composio') workspace server is excluded.
func TestDefaultConnector_FlagOn_GatesLegacy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	h.SetComposioDefaultConnector(true, "https://base.example.com")
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agGate", wsID, "", "Gate", "gate", "AGENT")

	dcSeedDefaults(t, db, wsID, "user-d", "mcp_srv_d")
	covCfgEncCred(t, db, wsID, userID, "mkc", composioManagedKeyName, "API_KEY", "", "ak_live")
	// Legacy open server (icon NULL) — must be gated off.
	covCfgSeedWSServer(t, db, "lg1", wsID, "legacy", "streamable-http", "https://l.example.com", "", "", "")

	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, agentID)
	got := dcByName(h.resolveAgentMCPServers(req, d, agentID))

	if _, ok := got["legacy"]; ok {
		t.Errorf("legacy non-composio server must be gated off when default active")
	}
	if _, ok := got["composio-default"]; !ok {
		t.Errorf("default connector should still be injected")
	}
}

// flag ON but default user/server empty → no default injected, no crash, and
// legacy is NOT gated (the default isn't configured so there's no replacement).
func TestDefaultConnector_FlagOn_NoDefaultConfigured(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	h.SetComposioDefaultConnector(true, "https://base.example.com")
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "agNone", wsID, "", "None", "none", "AGENT")

	// composio_settings exists but with NULL defaults.
	if _, err := db.Exec(`INSERT INTO composio_settings
		(workspace_id, encrypted_api_key, base_url, created_at, updated_at)
		VALUES (?, 'x', '', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed empty settings: %v", err)
	}
	covCfgSeedWSServer(t, db, "lg1", wsID, "legacy", "streamable-http", "https://l.example.com", "", "", "")

	req := httptest.NewRequest("GET", "/", nil)
	d, _ := h.loadAgentData(req, agentID)
	servers := h.resolveAgentMCPServers(req, d, agentID)
	got := dcByName(servers)

	if _, ok := got["composio-default"]; ok {
		t.Errorf("no default should be injected when default_user_id/server_id empty")
	}
	if _, ok := got["legacy"]; !ok {
		t.Errorf("legacy server should remain (no default configured → no gating)")
	}
}
