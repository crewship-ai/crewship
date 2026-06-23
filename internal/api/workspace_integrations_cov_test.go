package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// workspace_integrations_cov_test.go — remaining branches of the
// workspace MCP CRUD: the PATCH field fan-out + transport/field
// validation against merged state, the UPDATE failure, and the
// cascade-delete failure arms. Helpers prefixed covWSI.

func covWSIFixture(t *testing.T) (*IntegrationHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewIntegrationHandler(db, newTestLogger()), userID, wsID
}

func covWSIPatch(h *IntegrationHandler, userID, wsID, srvID string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/integrations/workspace/"+srvID, jsonBody(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("integrationId", srvID)
	rr := httptest.NewRecorder()
	h.UpdateWorkspaceIntegration(rr, req)
	return rr
}

func TestCovWSI_Update_AllFields_OK(t *testing.T) {
	h, userID, wsID := covWSIFixture(t)
	covCfgSeedWSServer(t, h.db, "covwsi-srv", wsID, "covwsi", "stdio", "", "npx covwsi", "", "")
	rr := covWSIPatch(h, userID, wsID, "covwsi-srv", map[string]any{
		"display_name": "Renamed",
		"command":      "npx renamed",
		"args_json":    `["-y"]`,
		"env_json":     `{"A":"1"}`,
		"config_json":  `{"t":2}`,
		"icon":         "bolt",
		"enabled":      false,
		"endpoint":     "",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["display_name"] != "Renamed" || resp["enabled"] != false {
		t.Errorf("resp = %v, want patched fields applied", resp)
	}
}

// TestCovWSI_Update_TransportFieldValidation — switching transport must
// validate against the MERGED state: streamable-http without any
// endpoint and stdio without any command are both 400s.
func TestCovWSI_Update_TransportFieldValidation(t *testing.T) {
	h, userID, wsID := covWSIFixture(t)
	// stdio server with a command but no endpoint.
	covCfgSeedWSServer(t, h.db, "covwsi-v1", wsID, "covwsiv1", "stdio", "", "npx v1", "", "")
	rr := covWSIPatch(h, userID, wsID, "covwsi-v1", map[string]any{"transport": "streamable-http"})
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "endpoint is required") {
		t.Fatalf("streamable-http switch = %d %s, want 400 endpoint required", rr.Code, rr.Body.String())
	}

	// http server with an endpoint but no command.
	covCfgSeedWSServer(t, h.db, "covwsi-v2", wsID, "covwsiv2", "streamable-http", "https://x.example/mcp", "", "", "")
	rr = covWSIPatch(h, userID, wsID, "covwsi-v2", map[string]any{"transport": "stdio"})
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "command is required") {
		t.Fatalf("stdio switch = %d %s, want 400 command required", rr.Code, rr.Body.String())
	}
}

func TestCovWSI_Update_ExecError_500(t *testing.T) {
	h, userID, wsID := covWSIFixture(t)
	covCfgSeedWSServer(t, h.db, "covwsi-srv2", wsID, "covwsi2", "stdio", "", "npx x", "", "")
	execOrFatal(t, h.db, `CREATE TRIGGER covwsi_block_upd BEFORE UPDATE ON workspace_mcp_servers
		BEGIN SELECT RAISE(ABORT, 'covwsi forced'); END`)
	rr := covWSIPatch(h, userID, wsID, "covwsi-srv2", map[string]any{"display_name": "X"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func covWSIDelete(h *IntegrationHandler, userID, wsID, srvID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/integrations/workspace/"+srvID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("integrationId", srvID)
	rr := httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rr, req)
	return rr
}

// covWSIDeleteFixture builds a workspace server with a crew override
// plus bindings at both scopes, so every cascade DELETE statement has
// rows to chew on (RAISE triggers only fire on affected rows).
func covWSIDeleteFixture(t *testing.T, h *IntegrationHandler, wsID, suffix string) string {
	t.Helper()
	db := h.db
	srvID := "covwsi-del-" + suffix
	crewID := seedCrewRow(t, db, "covwsi-crew-"+suffix, wsID, "C", "covwsi-crew-"+suffix)
	agentID := seedAgentRow(t, db, "covwsi-ag-"+suffix, wsID, crewID, "A", "covwsi-ag-"+suffix, "AGENT")
	covCfgSeedWSServer(t, db, srvID, wsID, "covwsidel"+suffix, "stdio", "", "npx x", "", "")
	execOrFatal(t, db, `INSERT INTO crew_mcp_servers
		(id, crew_id, workspace_mcp_server_id, name, display_name, transport, enabled)
		VALUES (?, ?, ?, ?, 'D', 'stdio', 1)`,
		"covwsi-cs-"+suffix, crewID, srvID, "covwsidel"+suffix)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES (?, ?, ?, 'workspace', 1)`, "covwsi-bw-"+suffix, agentID, srvID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES (?, ?, ?, 'crew', 1)`, "covwsi-bc-"+suffix, agentID, "covwsi-cs-"+suffix)
	return srvID
}

func TestCovWSI_Delete_BindingDeleteError_500(t *testing.T) {
	h, userID, wsID := covWSIFixture(t)
	srvID := covWSIDeleteFixture(t, h, wsID, "a")
	execOrFatal(t, h.db, `CREATE TRIGGER covwsi_block_b BEFORE DELETE ON agent_mcp_bindings
		BEGIN SELECT RAISE(ABORT, 'covwsi forced'); END`)
	rr := covWSIDelete(h, userID, wsID, srvID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// Tx rollback: the workspace server must still exist.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE id = ?`, srvID).Scan(&n); err != nil || n != 1 {
		t.Errorf("server rows = %d err=%v, want rollback to keep it", n, err)
	}
}

func TestCovWSI_Delete_CrewServerDeleteError_500(t *testing.T) {
	h, userID, wsID := covWSIFixture(t)
	srvID := covWSIDeleteFixture(t, h, wsID, "b")
	execOrFatal(t, h.db, `CREATE TRIGGER covwsi_block_cs BEFORE DELETE ON crew_mcp_servers
		BEGIN SELECT RAISE(ABORT, 'covwsi forced'); END`)
	rr := covWSIDelete(h, userID, wsID, srvID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWSI_Delete_ServerDeleteError_500(t *testing.T) {
	h, userID, wsID := covWSIFixture(t)
	srvID := covWSIDeleteFixture(t, h, wsID, "c")
	execOrFatal(t, h.db, `CREATE TRIGGER covwsi_block_ws BEFORE DELETE ON workspace_mcp_servers
		BEGIN SELECT RAISE(ABORT, 'covwsi forced'); END`)
	rr := covWSIDelete(h, userID, wsID, srvID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
