package api

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestResolveAgentMCPServersCarriesDisabledToolBindings pins the server half of
// #2178: switched-off mcp_tool_bindings travel with the resolved server as
// DisabledTools (the sidecar gateway's deny set), enabled ones stay in Tools,
// and a server is never handed out without its deny set.
func TestResolveAgentMCPServersCarriesDisabledToolBindings(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crewTB", wsID, "Crew TB", "crew-tb")
	agentID := seedAgentRow(t, db, "agTB", wsID, crewID, "TB", "tb", "AGENT")
	req := httptest.NewRequest("GET", "/", nil)
	d, err := h.loadAgentData(req, agentID)
	if err != nil {
		t.Fatalf("loadAgentData: %v", err)
	}
	covCfgSeedCrewServer(t, db, "csTB", crewID, "github", "streamable-http", "https://gh.example.com")
	for _, b := range []struct {
		id, tool string
		enabled  int
	}{{"tb1", "read_issue", 1}, {"tb2", "delete_repo", 0}, {"tb3", "force_push", 0}} {
		execOrFatal(t, db, `INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled) VALUES (?, 'csTB', 'crew', ?, ?)`, b.id, b.tool, b.enabled)
	}

	servers := h.resolveAgentMCPServers(req, d, agentID)
	if len(servers) != 1 {
		t.Fatalf("servers = %+v, want the one crew server", servers)
	}
	if got := servers[0].Tools; !reflect.DeepEqual(got, []string{"read_issue"}) {
		t.Errorf("Tools = %v, want [read_issue]", got)
	}
	if got := servers[0].DisabledTools; !reflect.DeepEqual(got, []string{"delete_repo", "force_push"}) {
		t.Errorf("DisabledTools = %v, want [delete_repo force_push]", got)
	}

	// If the bindings cannot be read, no server goes out: one without its
	// deny set would let a switched-off tool be called.
	execOrFatal(t, db, `DROP TABLE mcp_tool_bindings`)
	if got := h.resolveAgentMCPServers(req, d, agentID); len(got) != 0 {
		t.Fatalf("bindings unreadable: servers = %+v, want none", got)
	}
}
