package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// crew_integrations_crud_cov_test.go — remaining branches of the crew
// MCP CRUD: crewExists DB error, the full PATCH field fan-out, write
// failures (triggers), the read-back race, and the OAuth-credential
// cascade arms on delete. Helpers prefixed covCIC.

func covCICFixture(t *testing.T) (*IntegrationHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covcic-crew", wsID, "Crew", "covcic-crew")
	return NewIntegrationHandler(db, newTestLogger()), userID, wsID, crewID
}

func TestCovCIC_Create_CrewExistsDBError_500(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/integrations",
			jsonBody(map[string]any{"name": "x", "transport": "stdio", "command": "npx x"})),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	rr := httptest.NewRecorder()
	h.CreateCrewIntegration(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func covCICPatch(h *IntegrationHandler, userID, wsID, crewID, srvID string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/integrations/"+srvID,
			jsonBody(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", srvID)
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)
	return rr
}

// TestCovCIC_Update_AllFields_OK exercises every optional u.Set branch
// in one PATCH and verifies the persisted row.
func TestCovCIC_Update_AllFields_OK(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	covCfgSeedCrewServer(t, h.db, "covcic-srv", crewID, "covcic", "stdio", "")
	rr := covCICPatch(h, userID, wsID, crewID, "covcic-srv", map[string]any{
		"display_name": "Renamed",
		"command":      "npx renamed",
		"args_json":    `["-y"]`,
		"env_json":     `{"A":"1"}`,
		"config_json":  `{"t":2}`,
		"icon":         "sparkles",
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
	if resp["display_name"] != "Renamed" || resp["icon"] != "sparkles" || resp["enabled"] != false {
		t.Errorf("resp = %v, want all patched fields applied", resp)
	}
}

func TestCovCIC_Update_ExecError_500(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	covCfgSeedCrewServer(t, h.db, "covcic-srv2", crewID, "covcic2", "stdio", "")
	execOrFatal(t, h.db, `CREATE TRIGGER covcic_block_upd BEFORE UPDATE ON crew_mcp_servers
		BEGIN SELECT RAISE(ABORT, 'covcic forced'); END`)
	rr := covCICPatch(h, userID, wsID, crewID, "covcic-srv2", map[string]any{"display_name": "X"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovCIC_Update_ReadBackRaces_500 — the UPDATE succeeds but an
// AFTER UPDATE trigger deletes the row before the response read-back.
func TestCovCIC_Update_ReadBackRaces_500(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	covCfgSeedCrewServer(t, h.db, "covcic-srv3", crewID, "covcic3", "stdio", "")
	execOrFatal(t, h.db, `CREATE TRIGGER covcic_vanish AFTER UPDATE ON crew_mcp_servers
		BEGIN DELETE FROM crew_mcp_servers WHERE id = NEW.id; END`)
	rr := covCICPatch(h, userID, wsID, crewID, "covcic-srv3", map[string]any{"display_name": "X"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func covCICDelete(h *IntegrationHandler, userID, wsID, crewID, srvID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/integrations/"+srvID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", srvID)
	rr := httptest.NewRecorder()
	h.DeleteCrewIntegration(rr, req)
	return rr
}

func TestCovCIC_Delete_BindingDeleteError_500(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	covCfgSeedCrewServer(t, h.db, "covcic-d1", crewID, "covcicd1", "stdio", "")
	agentID := seedAgentRow(t, h.db, "covcic-ag", wsID, crewID, "A", "covcic-ag", "AGENT")
	execOrFatal(t, h.db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES ('covcic-b1', ?, 'covcic-d1', 'crew', 1)`, agentID)
	execOrFatal(t, h.db, `CREATE TRIGGER covcic_block_del BEFORE DELETE ON agent_mcp_bindings
		BEGIN SELECT RAISE(ABORT, 'covcic forced'); END`)
	rr := covCICDelete(h, userID, wsID, crewID, "covcic-d1")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovCIC_Delete_OAuthCredentialCascade — one OAuth credential is
// bound on the deleted server AND on a second server: it must survive
// (still referenced). A second credential bound only here is removed.
func TestCovCIC_Delete_OAuthCredentialCascade(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	db := h.db
	covCfgSeedCrewServer(t, db, "covcic-d2", crewID, "covcicd2", "stdio", "")
	covCfgSeedCrewServer(t, db, "covcic-keep", crewID, "covcickeep", "stdio", "")
	agentID := seedAgentRow(t, db, "covcic-ag2", wsID, crewID, "A", "covcic-ag2", "AGENT")
	agent2ID := seedAgentRow(t, db, "covcic-ag2b", wsID, crewID, "B", "covcic-ag2b", "AGENT")
	for _, c := range []struct{ id, name string }{
		{"covcic-cred-shared", "github oauth shared"},
		{"covcic-cred-solo", "github oauth solo"},
	} {
		execOrFatal(t, db, `INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'enc', 'OAUTH2', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
			c.id, wsID, c.name, userID)
	}
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled)
		VALUES ('covcic-b2', ?, 'covcic-d2', 'crew', 'covcic-cred-shared', 1)`, agentID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled)
		VALUES ('covcic-b3', ?, 'covcic-keep', 'crew', 'covcic-cred-shared', 1)`, agentID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled)
		VALUES ('covcic-b4', ?, 'covcic-d2', 'crew', 'covcic-cred-solo', 1)`, agent2ID)

	rr := covCICDelete(h, userID, wsID, crewID, "covcic-d2")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var shared, solo int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id = 'covcic-cred-shared'`).Scan(&shared); err != nil {
		t.Fatalf("count shared: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id = 'covcic-cred-solo'`).Scan(&solo); err != nil {
		t.Fatalf("count solo: %v", err)
	}
	if shared != 1 {
		t.Errorf("shared credential deleted despite live reference on another server")
	}
	if solo != 0 {
		t.Errorf("solo OAuth credential survived; want cascade delete")
	}
}

// TestCovCIC_Delete_SoloCredentialDeleteBlocked_WarnsAndProceeds — the
// cascade credential DELETE fails (trigger) but the integration delete
// still completes (warn-only contract).
func TestCovCIC_Delete_SoloCredentialDeleteBlocked_WarnsAndProceeds(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	db := h.db
	covCfgSeedCrewServer(t, db, "covcic-d3", crewID, "covcicd3", "stdio", "")
	agentID := seedAgentRow(t, db, "covcic-ag3", wsID, crewID, "A", "covcic-ag3", "AGENT")
	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('covcic-cred-lock', ?, 'gitlab oauth lock', 'enc', 'OAUTH2', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, userID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled)
		VALUES ('covcic-b5', ?, 'covcic-d3', 'crew', 'covcic-cred-lock', 1)`, agentID)
	execOrFatal(t, db, `CREATE TRIGGER covcic_block_cred_del BEFORE DELETE ON credentials
		BEGIN SELECT RAISE(ABORT, 'covcic forced'); END`)

	rr := covCICDelete(h, userID, wsID, crewID, "covcic-d3")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (credential delete is best-effort); body=%s",
			rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE id = 'covcic-d3'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("server rows = %d err=%v, want deleted", n, err)
	}
}

func TestCovCIC_Delete_ServerDeleteError_500(t *testing.T) {
	h, userID, wsID, crewID := covCICFixture(t)
	covCfgSeedCrewServer(t, h.db, "covcic-d4", crewID, "covcicd4", "stdio", "")
	execOrFatal(t, h.db, `CREATE TRIGGER covcic_block_srv_del BEFORE DELETE ON crew_mcp_servers
		BEGIN SELECT RAISE(ABORT, 'covcic forced'); END`)
	rr := covCICDelete(h, userID, wsID, crewID, "covcic-d4")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
