package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// mcp_tool_bindings_cov2_test.go — remaining error branches: the list
// query 500, the upsert 500s (renamed table), and the read-back 500
// (an AFTER INSERT trigger deletes the fresh row, simulating a racing
// delete). Helpers prefixed covMTB.

func covMTBFixture(t *testing.T) (*IntegrationHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covmtb-crew", wsID, "Crew", "covmtb-crew")
	covCfgSeedCrewServer(t, db, "covmtb-srv", crewID, "covmtb", "stdio", "")
	return NewIntegrationHandler(db, newTestLogger()), userID, wsID, crewID, "covmtb-srv"
}

func TestCovMTB_List_QueryError_500(t *testing.T) {
	h, userID, wsID, crewID, srvID := covMTBFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE mcp_tool_bindings RENAME TO mtb_broken`)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/integrations/"+srvID+"/tools", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", srvID)
	rr := httptest.NewRecorder()
	h.ListCrewIntegrationTools(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func covMTBPatchTool(h *IntegrationHandler, userID, wsID, crewID, srvID, tool string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH",
			"/api/v1/crews/"+crewID+"/integrations/"+srvID+"/tools/"+tool,
			jsonBody(map[string]any{"enabled": false})),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", srvID)
	req.SetPathValue("toolName", tool)
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)
	return rr
}

func TestCovMTB_Update_UpsertError_500(t *testing.T) {
	h, userID, wsID, crewID, srvID := covMTBFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covmtb_block_ins BEFORE INSERT ON mcp_tool_bindings
		BEGIN SELECT RAISE(ABORT, 'covmtb forced'); END`)
	rr := covMTBPatchTool(h, userID, wsID, crewID, srvID, "search")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovMTB_Update_ReadBackRaces_500 — the upsert lands but the row
// vanishes before the read-back (simulated via AFTER INSERT
// self-delete); the handler must surface a 500, not a half body.
func TestCovMTB_Update_ReadBackRaces_500(t *testing.T) {
	h, userID, wsID, crewID, srvID := covMTBFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covmtb_vanish AFTER INSERT ON mcp_tool_bindings
		BEGIN DELETE FROM mcp_tool_bindings WHERE id = NEW.id; END`)
	rr := covMTBPatchTool(h, userID, wsID, crewID, srvID, "search")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovMTB_Refresh_UpsertError_500(t *testing.T) {
	h, userID, wsID, crewID, srvID := covMTBFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE mcp_tool_bindings RENAME TO mtb_broken`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST",
			"/api/v1/crews/"+crewID+"/integrations/"+srvID+"/tools/refresh",
			jsonBody(map[string]any{"tools": []map[string]any{{"name": "search"}}})),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", srvID)
	rr := httptest.NewRecorder()
	h.RefreshCrewIntegrationTools(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
