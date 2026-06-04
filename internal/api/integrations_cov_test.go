package api

// Coverage tests for the MCP integration handlers:
//   crew_integrations.go / crew_integrations_crud.go / crew_integrations_migrate.go,
//   workspace_integrations.go, integration_resolve.go, agent_bindings.go.
//
// Focus: auth/role failures, invalid-JSON (400), not-found (404), validation,
// happy paths asserting DB state, and DB-error 500 branches via fault injection
// (db.Close() before invoking a handler with an otherwise-valid request).
//
// All seed helpers here are prefixed covInt to avoid clashing with existing
// helpers; all test funcs are prefixed TestCovInt.
//
// SKIPPED: there are no live-MCP-probe / network branches in these handlers —
// the only "external" surface is auth_status which is computed purely from DB
// rows, so nothing is skipped for network reasons.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// covInt seed helpers
// ---------------------------------------------------------------------------

func covIntWSServer(t *testing.T, db *sql.DB, id, wsID, name, transport, endpoint string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var ep any
	if endpoint != "" {
		ep = endpoint
	}
	_, err := db.Exec(`INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, wsID, name, name, transport, ep, now, now)
	if err != nil {
		t.Fatalf("seed workspace mcp server %s: %v", id, err)
	}
	return id
}

func covIntCrewServer(t *testing.T, db *sql.DB, id, crewID, name, transport, endpoint string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var ep any
	if endpoint != "" {
		ep = endpoint
	}
	_, err := db.Exec(`INSERT INTO crew_mcp_servers
		(id, crew_id, name, display_name, transport, endpoint, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, crewID, name, name, transport, ep, now, now)
	if err != nil {
		t.Fatalf("seed crew mcp server %s: %v", id, err)
	}
	return id
}

func covIntBinding(t *testing.T, db *sql.DB, id, agentID, serverID, scope, credID string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var cred any
	if credID != "" {
		cred = credID
	}
	_, err := db.Exec(`INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?)`,
		id, agentID, serverID, scope, cred, now)
	if err != nil {
		t.Fatalf("seed agent binding %s: %v", id, err)
	}
	return id
}

func covIntCredential(t *testing.T, db *sql.DB, id, wsID, userID, name, status string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, scope, type, provider, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'enc', 'WORKSPACE', 'SECRET', 'NONE', ?, ?, ?, ?)`,
		id, wsID, name, status, userID, now, now)
	if err != nil {
		t.Fatalf("seed credential %s: %v", id, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// Workspace integrations
// ---------------------------------------------------------------------------

func TestCovIntListWorkspaceIntegrations(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIntWSServer(t, db, "ws-srv-1", wsID, "github", "streamable-http", "https://mcp.example/github")

	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/workspace", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListWorkspaceIntegrations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var out []workspaceMCPServerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Name != "github" {
		t.Fatalf("unexpected list: %+v", out)
	}
}

func TestCovIntListWorkspaceIntegrations_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close()

	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/workspace", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListWorkspaceIntegrations(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestCovIntCreateWorkspaceIntegration(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		body     string
		wantCode int
	}{
		{name: "forbidden", role: "MEMBER", body: `{"name":"x","endpoint":"https://e"}`, wantCode: http.StatusForbidden},
		{name: "invalid json", role: "OWNER", body: `{`, wantCode: http.StatusBadRequest},
		{name: "missing name", role: "OWNER", body: `{}`, wantCode: http.StatusBadRequest},
		{name: "bad transport", role: "OWNER", body: `{"name":"x","transport":"bogus"}`, wantCode: http.StatusBadRequest},
		{name: "http needs endpoint", role: "OWNER", body: `{"name":"x","transport":"streamable-http"}`, wantCode: http.StatusBadRequest},
		{name: "stdio needs command", role: "OWNER", body: `{"name":"x","transport":"stdio"}`, wantCode: http.StatusBadRequest},
		{name: "ok http", role: "OWNER", body: `{"name":"gh","endpoint":"https://e"}`, wantCode: http.StatusCreated},
		{name: "ok stdio", role: "OWNER", body: `{"name":"local","transport":"stdio","command":"node"}`, wantCode: http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			h := NewIntegrationHandler(db, newTestLogger())

			req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/workspace",
				strings.NewReader(tc.body))
			req = withWorkspaceUser(req, userID, wsID, tc.role)
			rec := httptest.NewRecorder()
			h.CreateWorkspaceIntegration(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("%s: want %d, got %d: %s", tc.name, tc.wantCode, rec.Code, rec.Body)
			}
			if tc.wantCode == http.StatusCreated {
				var got workspaceMCPServerResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				var cnt int
				if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE id = ?`, got.ID).Scan(&cnt); err != nil {
					t.Fatalf("count: %v", err)
				}
				if cnt != 1 {
					t.Fatalf("expected row persisted, count=%d", cnt)
				}
			}
		})
	}
}

func TestCovIntCreateWorkspaceIntegration_Duplicate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIntWSServer(t, db, "dup-1", wsID, "github", "streamable-http", "https://e")
	h := NewIntegrationHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"github","endpoint":"https://e"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body)
	}
}

func TestCovIntGetWorkspaceIntegration(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIntWSServer(t, db, "g-1", wsID, "github", "streamable-http", "https://e")
	h := NewIntegrationHandler(db, newTestLogger())

	// found
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("integrationId", "g-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.GetWorkspaceIntegration(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("found: want 200, got %d", rec.Code)
	}

	// not found
	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.SetPathValue("integrationId", "missing")
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rec2 := httptest.NewRecorder()
	h.GetWorkspaceIntegration(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec2.Code)
	}
}

func TestCovIntUpdateWorkspaceIntegration(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIntWSServer(t, db, "u-1", wsID, "github", "streamable-http", "https://e")
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(id, body, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader(body))
		req.SetPathValue("integrationId", id)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.UpdateWorkspaceIntegration(rec, req)
		return rec
	}

	if rec := mk("u-1", `{}`, "MEMBER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk("u-1", `{`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", rec.Code)
	}
	if rec := mk("missing", `{}`, "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec.Code)
	}
	if rec := mk("u-1", `{"transport":"bogus"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad transport: want 400, got %d", rec.Code)
	}
	// switch to stdio but no command -> 400 (merged final state)
	if rec := mk("u-1", `{"transport":"stdio","endpoint":""}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("stdio needs command: want 400, got %d: %s", rec.Code, rec.Body)
	}
	// happy path: rename + disable
	rec := mk("u-1", `{"display_name":"GitHub MCP","enabled":false}`, "OWNER")
	if rec.Code != http.StatusOK {
		t.Fatalf("update ok: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var dn string
	var enabled int
	if err := db.QueryRow(`SELECT display_name, enabled FROM workspace_mcp_servers WHERE id='u-1'`).Scan(&dn, &enabled); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if dn != "GitHub MCP" || enabled != 0 {
		t.Fatalf("update not persisted: dn=%q enabled=%d", dn, enabled)
	}
}

func TestCovIntDeleteWorkspaceIntegration(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-d", wsID, "Crew", "crew-d")
	agentID := seedAgentRow(t, db, "agent-d", wsID, crewID, "Ag", "ag", "AGENT")
	covIntWSServer(t, db, "wd-1", wsID, "github", "streamable-http", "https://e")
	covIntCrewServer(t, db, "cd-1", crewID, "ghcrew", "streamable-http", "https://e")
	// link crew server to workspace server + create a workspace-scope binding
	if _, err := db.Exec(`UPDATE crew_mcp_servers SET workspace_mcp_server_id='wd-1' WHERE id='cd-1'`); err != nil {
		t.Fatalf("link: %v", err)
	}
	covIntBinding(t, db, "bd-1", agentID, "wd-1", "workspace", "")
	h := NewIntegrationHandler(db, newTestLogger())

	// forbidden
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("integrationId", "wd-1")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rec := httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}

	// not found
	req2 := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req2.SetPathValue("integrationId", "missing")
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rec2 := httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec2.Code)
	}

	// happy path: cascades crew server + bindings
	req3 := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req3.SetPathValue("integrationId", "wd-1")
	req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
	rec3 := httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d: %s", rec3.Code, rec3.Body)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE id='wd-1'`).Scan(&n); err != nil {
		t.Fatalf("scan ws count: %v", err)
	}
	if n != 0 {
		t.Fatalf("ws server not deleted")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE id='cd-1'`).Scan(&n); err != nil {
		t.Fatalf("scan crew count: %v", err)
	}
	if n != 0 {
		t.Fatalf("crew server override not cascaded")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE id='bd-1'`).Scan(&n); err != nil {
		t.Fatalf("scan binding count: %v", err)
	}
	if n != 0 {
		t.Fatalf("binding not cascaded")
	}
}

func TestCovIntDeleteWorkspaceIntegration_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIntWSServer(t, db, "wde-1", wsID, "github", "streamable-http", "https://e")
	db.Close()
	h := NewIntegrationHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("integrationId", "wde-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Crew integrations
// ---------------------------------------------------------------------------

func TestCovIntListAllCrewIntegrations(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-all", wsID, "Crew", "crew-all")
	covIntCrewServer(t, db, "ca-1", crewID, "gh", "streamable-http", "https://e")
	h := NewIntegrationHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var out []crewIntegrationOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].AuthStatus != "missing" {
		t.Fatalf("unexpected overview (missing auth_status expected): %+v", out)
	}
}

func TestCovIntListAllCrewIntegrations_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close()
	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestCovIntListCrewIntegrations(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-l", wsID, "Crew", "crew-l")
	agentID := seedAgentRow(t, db, "agent-l", wsID, crewID, "Ag", "ag", "AGENT")
	covIntCrewServer(t, db, "cl-1", crewID, "gh", "streamable-http", "https://e")
	// bind with an ACTIVE credential → auth_status "connected"
	credID := covIntCredential(t, db, "cred-l", wsID, userID, "gh-oauth", "ACTIVE")
	covIntBinding(t, db, "bl-1", agentID, "cl-1", "crew", credID)
	h := NewIntegrationHandler(db, newTestLogger())

	// not found crew
	reqNF := httptest.NewRequest(http.MethodGet, "/x", nil)
	reqNF.SetPathValue("crewId", "nope")
	reqNF = withWorkspaceUser(reqNF, userID, wsID, "OWNER")
	recNF := httptest.NewRecorder()
	h.ListCrewIntegrations(recNF, reqNF)
	if recNF.Code != http.StatusNotFound {
		t.Fatalf("crew nf: want 404, got %d", recNF.Code)
	}

	// happy path
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListCrewIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var out []crewMCPServerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].AuthStatus != "connected" || out[0].AgentBindCount != 1 {
		t.Fatalf("unexpected crew list: %+v", out)
	}
}

func TestCovIntListCrewIntegrations_Migrate(t *testing.T) {
	// Crew with a legacy mcp_config_json blob → auto-migrated to crew_mcp_servers.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-mig", wsID, "Crew", "crew-mig")
	blob := `{"mcpServers":{"weather":{"url":"https://w.example/mcp","type":"http"}}}`
	if _, err := db.Exec(`UPDATE crews SET mcp_config_json=? WHERE id=?`, blob, crewID); err != nil {
		t.Fatalf("set blob: %v", err)
	}
	h := NewIntegrationHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListCrewIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id=? AND name='weather'`, crewID).Scan(&n); err != nil {
		t.Fatalf("scan crew count: %v", err)
	}
	if n != 1 {
		t.Fatalf("blob not migrated, count=%d", n)
	}
	// blob cleared
	var blobAfter interface{}
	if err := db.QueryRow(`SELECT mcp_config_json FROM crews WHERE id=?`, crewID).Scan(&blobAfter); err != nil {
		t.Fatalf("scan blob: %v", err)
	}
	if blobAfter != nil {
		t.Fatalf("blob not cleared: %v", blobAfter)
	}
}

func TestCovIntListCrewIntegrations_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-le", wsID, "Crew", "crew-le")
	db.Close()
	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListCrewIntegrations(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestCovIntCreateCrewIntegration(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-c", wsID, "Crew", "crew-c")
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o')`, otherWS); err != nil {
		t.Fatalf("other ws: %v", err)
	}
	covIntWSServer(t, db, "link-ok", wsID, "linkable", "streamable-http", "https://e")
	covIntWSServer(t, db, "link-other", otherWS, "otherlink", "streamable-http", "https://e")
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(crew, body, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.SetPathValue("crewId", crew)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.CreateCrewIntegration(rec, req)
		return rec
	}

	if rec := mk(crewID, `{}`, "MEMBER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk("nope", `{"name":"x","endpoint":"https://e"}`, "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("crew nf: want 404, got %d", rec.Code)
	}
	if rec := mk(crewID, `{`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", rec.Code)
	}
	if rec := mk(crewID, `{}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name: want 400, got %d", rec.Code)
	}
	if rec := mk(crewID, `{"name":"x","transport":"bogus"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad transport: want 400, got %d", rec.Code)
	}
	if rec := mk(crewID, `{"name":"x","transport":"streamable-http"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("http needs endpoint: want 400, got %d", rec.Code)
	}
	if rec := mk(crewID, `{"name":"x","transport":"stdio"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("stdio needs command: want 400, got %d", rec.Code)
	}
	// linked workspace server not found
	if rec := mk(crewID, `{"name":"x","endpoint":"https://e","workspace_mcp_server_id":"missing"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("ws link missing: want 400, got %d", rec.Code)
	}
	// linked workspace server in different workspace
	if rec := mk(crewID, `{"name":"x","endpoint":"https://e","workspace_mcp_server_id":"link-other"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("ws link cross-ws: want 400, got %d", rec.Code)
	}
	// happy path linking valid ws server
	rec := mk(crewID, `{"name":"gh","endpoint":"https://e","workspace_mcp_server_id":"link-ok"}`, "OWNER")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ok: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var got crewMCPServerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE id=? AND workspace_mcp_server_id='link-ok'`, got.ID).Scan(&cnt); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("crew integration not persisted with link")
	}
	// duplicate name → conflict
	if rec := mk(crewID, `{"name":"gh","endpoint":"https://e"}`, "OWNER"); rec.Code != http.StatusConflict {
		t.Fatalf("dup: want 409, got %d", rec.Code)
	}
}

func TestCovIntUpdateCrewIntegration(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-u", wsID, "Crew", "crew-u")
	covIntCrewServer(t, db, "cu-1", crewID, "gh", "streamable-http", "https://e")
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(crew, id, body, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader(body))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("integrationId", id)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.UpdateCrewIntegration(rec, req)
		return rec
	}

	if rec := mk(crewID, "cu-1", `{}`, "MEMBER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk(crewID, "cu-1", `{`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", rec.Code)
	}
	if rec := mk(crewID, "missing", `{}`, "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec.Code)
	}
	if rec := mk(crewID, "cu-1", `{"transport":"bogus"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad transport: want 400, got %d", rec.Code)
	}
	if rec := mk(crewID, "cu-1", `{"transport":"stdio","endpoint":""}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("stdio needs command: want 400, got %d: %s", rec.Code, rec.Body)
	}
	// happy: switch to stdio with command + disable
	rec := mk(crewID, "cu-1", `{"transport":"stdio","command":"node","enabled":false,"display_name":"GH"}`, "OWNER")
	if rec.Code != http.StatusOK {
		t.Fatalf("update ok: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var transport, dn string
	var enabled int
	if err := db.QueryRow(`SELECT transport, display_name, enabled FROM crew_mcp_servers WHERE id='cu-1'`).Scan(&transport, &dn, &enabled); err != nil {
		t.Fatalf("scan crew server: %v", err)
	}
	if transport != "stdio" || dn != "GH" || enabled != 0 {
		t.Fatalf("update not persisted: %s %s %d", transport, dn, enabled)
	}
}

func TestCovIntDeleteCrewIntegration(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-del", wsID, "Crew", "crew-del")
	agentID := seedAgentRow(t, db, "agent-del", wsID, crewID, "Ag", "ag", "AGENT")
	covIntCrewServer(t, db, "cdel-1", crewID, "gh", "streamable-http", "https://e")
	// OAuth credential created for this integration → should cascade-delete
	credID := covIntCredential(t, db, "cred-oauth", wsID, userID, "gh-oauth", "ACTIVE")
	if _, err := db.Exec(`UPDATE credentials SET type='OAUTH2' WHERE id=?`, credID); err != nil {
		t.Fatalf("oauth type: %v", err)
	}
	covIntBinding(t, db, "bdel-1", agentID, "cdel-1", "crew", credID)
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(crew, id, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/x", nil)
		req.SetPathValue("crewId", crew)
		req.SetPathValue("integrationId", id)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.DeleteCrewIntegration(rec, req)
		return rec
	}

	if rec := mk(crewID, "cdel-1", "MANAGER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk(crewID, "missing", "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec.Code)
	}
	rec := mk(crewID, "cdel-1", "OWNER")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE id='cdel-1'`).Scan(&n); err != nil {
		t.Fatalf("scan crew count: %v", err)
	}
	if n != 0 {
		t.Fatalf("crew server not deleted")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE id='bdel-1'`).Scan(&n); err != nil {
		t.Fatalf("scan binding count: %v", err)
	}
	if n != 0 {
		t.Fatalf("binding not deleted")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id=?`, credID).Scan(&n); err != nil {
		t.Fatalf("scan cred count: %v", err)
	}
	if n != 0 {
		t.Fatalf("orphan oauth credential not cascade-deleted")
	}
}

func TestCovIntDeleteCrewIntegration_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-de", wsID, "Crew", "crew-de")
	covIntCrewServer(t, db, "cde-1", crewID, "gh", "streamable-http", "https://e")
	db.Close()
	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", "cde-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.DeleteCrewIntegration(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// JSON-blob migration helpers (crew_integrations_migrate.go)
// ---------------------------------------------------------------------------

func TestCovIntParseMCPConfigBlob(t *testing.T) {
	if s, err := parseMCPConfigBlob(""); err != nil || s != nil {
		t.Fatalf("empty: want nil,nil got %v,%v", s, err)
	}
	if _, err := parseMCPConfigBlob("{not json"); err == nil {
		t.Fatalf("invalid json: want error")
	}
	if s, err := parseMCPConfigBlob(`{"mcpServers":{}}`); err != nil || s != nil {
		t.Fatalf("no servers: want nil,nil got %v,%v", s, err)
	}
	blob := `{"mcpServers":{
		"my-http":{"url":"https://h/mcp","type":"http"},
		"local-tool":{"command":"node","args":["x.js"],"env":{"K":"V"}}
	}}`
	servers, err := parseMCPConfigBlob(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(servers))
	}
	for _, s := range servers {
		switch s.name {
		case "my-http":
			if s.transport != "streamable-http" || s.endpoint == nil || *s.endpoint != "https://h/mcp" {
				t.Fatalf("http server parsed wrong: %+v", s)
			}
			if s.displayName != "My Http" {
				t.Fatalf("display name: got %q", s.displayName)
			}
		case "local-tool":
			if s.transport != "stdio" || s.command == nil || *s.command != "node" {
				t.Fatalf("stdio server parsed wrong: %+v", s)
			}
			if s.argsJSON == nil || s.envJSON == nil {
				t.Fatalf("args/env not set: %+v", s)
			}
		default:
			t.Fatalf("unexpected server %q", s.name)
		}
	}
}

func TestCovIntMigrateJSONBlobToAgentServers(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-am", wsID, "Crew", "crew-am")
	agentID := seedAgentRow(t, db, "agent-am", wsID, crewID, "Ag", "ag", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET mcp_config_json=? WHERE id=?`,
		`{"mcpServers":{"weather":{"url":"https://w/mcp","type":"http"}}}`, agentID); err != nil {
		t.Fatalf("set blob: %v", err)
	}

	if err := MigrateJSONBlobToAgentServers(context.Background(), db, newTestLogger(), agentID, crewID, wsID,
		`{"mcpServers":{"weather":{"url":"https://w/mcp","type":"http"}}}`); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var srvCnt, bindCnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id=? AND name='weather'`, crewID).Scan(&srvCnt); err != nil {
		t.Fatalf("scan server count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE agent_id=?`, agentID).Scan(&bindCnt); err != nil {
		t.Fatalf("scan binding count: %v", err)
	}
	if srvCnt != 1 || bindCnt != 1 {
		t.Fatalf("migration incomplete: servers=%d bindings=%d", srvCnt, bindCnt)
	}
	// empty blob is a no-op
	if err := MigrateJSONBlobToAgentServers(context.Background(), db, newTestLogger(), agentID, crewID, wsID, ""); err != nil {
		t.Fatalf("empty migrate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Agent bindings
// ---------------------------------------------------------------------------

func TestCovIntListAgentBindings(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-lb", wsID, "Crew", "crew-lb")
	agentID := seedAgentRow(t, db, "agent-lb", wsID, crewID, "Ag", "ag", "AGENT")
	covIntCrewServer(t, db, "clb-1", crewID, "gh", "streamable-http", "https://e")
	covIntBinding(t, db, "blb-1", agentID, "clb-1", "crew", "")
	h := NewIntegrationHandler(db, newTestLogger())

	// agent not found
	reqNF := httptest.NewRequest(http.MethodGet, "/x", nil)
	reqNF.SetPathValue("agentId", "nope")
	reqNF = withWorkspaceUser(reqNF, userID, wsID, "OWNER")
	recNF := httptest.NewRecorder()
	h.ListAgentBindings(recNF, reqNF)
	if recNF.Code != http.StatusNotFound {
		t.Fatalf("agent nf: want 404, got %d", recNF.Code)
	}

	// happy
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAgentBindings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var out []agentMCPBindingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].ServerName != "gh" {
		t.Fatalf("unexpected bindings: %+v", out)
	}
}

func TestCovIntCreateAgentBinding(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-cb", wsID, "Crew", "crew-cb")
	agentID := seedAgentRow(t, db, "agent-cb", wsID, crewID, "Ag", "ag", "AGENT")
	otherWS := "other-cb"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'ocb')`, otherWS); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	covIntWSServer(t, db, "wsrv-cb", wsID, "gh", "streamable-http", "https://e")
	covIntWSServer(t, db, "wsrv-other", otherWS, "ghother", "streamable-http", "https://e")
	covIntCrewServer(t, db, "csrv-cb", crewID, "ghcrew", "streamable-http", "https://e")
	credOK := covIntCredential(t, db, "cred-cb", wsID, userID, "tok", "ACTIVE")
	credOther := covIntCredential(t, db, "cred-other", otherWS, userID, "tok2", "ACTIVE")
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(agent, body, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.SetPathValue("agentId", agent)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.CreateAgentBinding(rec, req)
		return rec
	}

	if rec := mk(agentID, `{}`, "MEMBER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk("nope", `{"mcp_server_id":"x","mcp_server_scope":"crew"}`, "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("agent nf: want 404, got %d", rec.Code)
	}
	if rec := mk(agentID, `{`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_scope":"crew"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing server id: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"x","mcp_server_scope":"bogus"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"missing","mcp_server_scope":"workspace"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("ws server nf: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"wsrv-other","mcp_server_scope":"workspace"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("ws cross-ws: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"missing","mcp_server_scope":"crew"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("crew server nf: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"wsrv-cb","mcp_server_scope":"workspace","credential_id":"missing"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("cred nf: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"wsrv-cb","mcp_server_scope":"workspace","credential_id":"`+credOther+`"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("cred cross-ws: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, `{"mcp_server_id":"wsrv-cb","mcp_server_scope":"workspace","cred_type":"bogus"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cred_type: want 400, got %d", rec.Code)
	}
	// happy path
	rec := mk(agentID, `{"mcp_server_id":"wsrv-cb","mcp_server_scope":"workspace","credential_id":"`+credOK+`","cred_type":"api_key","cred_header":"X-Key","enabled":true}`, "OWNER")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ok: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var got agentMCPBindingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE id=? AND credential_id=?`, got.ID, credOK).Scan(&cnt); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("binding not persisted")
	}
	// duplicate (same agent+server+scope) → conflict
	if rec := mk(agentID, `{"mcp_server_id":"wsrv-cb","mcp_server_scope":"workspace"}`, "OWNER"); rec.Code != http.StatusConflict {
		t.Fatalf("dup: want 409, got %d", rec.Code)
	}
}

func TestCovIntUpdateAgentBinding(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-ub", wsID, "Crew", "crew-ub")
	agentID := seedAgentRow(t, db, "agent-ub", wsID, crewID, "Ag", "ag", "AGENT")
	otherWS := "other-ub"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'oub')`, otherWS); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	covIntCrewServer(t, db, "csrv-ub", crewID, "gh", "streamable-http", "https://e")
	covIntBinding(t, db, "bind-ub", agentID, "csrv-ub", "crew", "")
	credOK := covIntCredential(t, db, "cred-ub", wsID, userID, "tok", "ACTIVE")
	credOther := covIntCredential(t, db, "cred-ubo", otherWS, userID, "tok", "ACTIVE")
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(agent, id, body, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader(body))
		req.SetPathValue("agentId", agent)
		req.SetPathValue("integrationId", id)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.UpdateAgentBinding(rec, req)
		return rec
	}

	if rec := mk(agentID, "bind-ub", `{}`, "MEMBER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk(agentID, "bind-ub", `{`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, "missing", `{}`, "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec.Code)
	}
	if rec := mk(agentID, "bind-ub", `{}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no fields: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, "bind-ub", `{"credential_id":"missing"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("cred nf: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, "bind-ub", `{"credential_id":"`+credOther+`"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("cred cross-ws: want 400, got %d", rec.Code)
	}
	if rec := mk(agentID, "bind-ub", `{"cred_type":"bogus"}`, "OWNER"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cred_type: want 400, got %d", rec.Code)
	}
	// Pass all field-validation, then reach the UPDATE. This exercises every
	// ub.Set/SetNull branch (credential_id, enabled, cred_type, cred_header,
	// env_var_name, config_override_json).
	//
	// LATENT BUG: newUpdate() always emits an "updated_at = ?" clause, but the
	// agent_mcp_bindings table has NO updated_at column (only created_at — see
	// migrate_consts_v26_v32.go). So the generated UPDATE fails with
	// "no such column: updated_at" and the handler returns 500. This means the
	// success path of UpdateAgentBinding is unreachable in production today.
	// We assert the real (broken) behaviour rather than papering over it.
	rec := mk(agentID, "bind-ub",
		`{"credential_id":"`+credOK+`","cred_type":"basic","cred_header":"X-H","env_var_name":"TOK","enabled":false,"config_override_json":"{}"}`, "OWNER")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update (no updated_at column): want 500, got %d: %s", rec.Code, rec.Body)
	}
	// SetNull branch is also reached before the failing UPDATE.
	if rec := mk(agentID, "bind-ub", `{"env_var_name":""}`, "OWNER"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("clear env (no updated_at column): want 500, got %d", rec.Code)
	}
}

func TestCovIntDeleteAgentBinding(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-db", wsID, "Crew", "crew-db")
	agentID := seedAgentRow(t, db, "agent-db2", wsID, crewID, "Ag", "ag", "AGENT")
	covIntCrewServer(t, db, "csrv-db", crewID, "gh", "streamable-http", "https://e")
	covIntBinding(t, db, "bind-db", agentID, "csrv-db", "crew", "")
	h := NewIntegrationHandler(db, newTestLogger())

	mk := func(agent, id, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/x", nil)
		req.SetPathValue("agentId", agent)
		req.SetPathValue("integrationId", id)
		req = withWorkspaceUser(req, userID, wsID, role)
		rec := httptest.NewRecorder()
		h.DeleteAgentBinding(rec, req)
		return rec
	}

	if rec := mk(agentID, "bind-db", "MEMBER"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden: want 403, got %d", rec.Code)
	}
	if rec := mk(agentID, "missing", "OWNER"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: want 404, got %d", rec.Code)
	}
	if rec := mk(agentID, "bind-db", "OWNER"); rec.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d", rec.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE id='bind-db'`).Scan(&n); err != nil {
		t.Fatalf("scan binding count: %v", err)
	}
	if n != 0 {
		t.Fatalf("binding not deleted")
	}
}

func TestCovIntDeleteAgentBinding_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close()
	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("agentId", "a")
	req.SetPathValue("integrationId", "b")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.DeleteAgentBinding(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Integration resolution (integration_resolve.go)
// ---------------------------------------------------------------------------

func TestCovIntResolveAgentIntegrations(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-r", wsID, "Crew", "crew-r")
	agentA := seedAgentRow(t, db, "agent-ra", wsID, crewID, "A", "a", "AGENT")
	agentB := seedAgentRow(t, db, "agent-rb", wsID, crewID, "B", "b", "AGENT")

	// Workspace server "shared" and a crew server "shared" that overrides it by name.
	covIntWSServer(t, db, "ws-shared", wsID, "shared", "streamable-http", "https://ws")
	covIntCrewServer(t, db, "crew-shared", crewID, "shared", "streamable-http", "https://crew")
	// Crew-only server "extra" bound to agentB only → opt-in filtering should hide
	// it from agentA.
	covIntCrewServer(t, db, "crew-extra", crewID, "extra", "streamable-http", "https://x")
	covIntBinding(t, db, "b-extra", agentB, "crew-extra", "crew", "")
	// Crew server "muted" bound to agentA with enabled=0 → opt-out for A.
	covIntCrewServer(t, db, "crew-muted", crewID, "muted", "streamable-http", "https://m")
	credID := covIntCredential(t, db, "cred-r", wsID, userID, "tok", "ACTIVE")
	if _, err := db.Exec(`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, created_at)
		VALUES ('b-muted', ?, 'crew-muted', 'crew', ?, 0, ?)`, agentA, credID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed muted binding: %v", err)
	}

	h := NewIntegrationHandler(db, newTestLogger())

	// agent not found
	reqNF := httptest.NewRequest(http.MethodGet, "/x", nil)
	reqNF.SetPathValue("agentId", "nope")
	reqNF = withWorkspaceUser(reqNF, userID, wsID, "OWNER")
	recNF := httptest.NewRecorder()
	h.ResolveAgentIntegrations(recNF, reqNF)
	if recNF.Code != http.StatusNotFound {
		t.Fatalf("agent nf: want 404, got %d", recNF.Code)
	}

	// resolve for agentA
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("agentId", agentA)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ResolveAgentIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var out []ResolvedIntegration
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byName := map[string]ResolvedIntegration{}
	for _, s := range out {
		byName[s.Name] = s
	}
	// "shared" present, crew override wins (endpoint https://crew, scope crew)
	if s, ok := byName["shared"]; !ok || s.Scope != "crew" || s.Endpoint == nil || *s.Endpoint != "https://crew" {
		t.Fatalf("shared override wrong: %+v (ok=%v)", s, ok)
	}
	// "extra" hidden from agentA (bound only to agentB)
	if _, ok := byName["extra"]; ok {
		t.Fatalf("extra should be hidden from agentA via opt-in filtering")
	}
	// "muted" excluded (binding disabled it)
	if _, ok := byName["muted"]; ok {
		t.Fatalf("muted should be excluded for agentA")
	}
}

func TestCovIntResolveAgentIntegrations_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-rde", wsID, "Crew", "crew-rde")
	agentID := seedAgentRow(t, db, "agent-rde", wsID, crewID, "A", "a", "AGENT")
	db.Close()
	h := NewIntegrationHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ResolveAgentIntegrations(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}
