package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared seed helpers (covMTB-prefixed to avoid clashing with the existing
// seedCrewMCPServer in mcp_tool_bindings_test.go, which pins fixed IDs).
// ---------------------------------------------------------------------------

// covMTBSeedCrewServer inserts a crew + a (non-soft-deleted) crew_mcp_server
// pair and returns the crew_id / server_id. Uses unique IDs so it can be
// called alongside the package's other MCP seeders without UNIQUE conflicts.
func covMTBSeedCrewServer(t *testing.T, db *sql.DB, wsID string) (crewID, serverID string) {
	t.Helper()
	crewID = "covmtb-crew-" + generateCUID()
	serverID = "covmtb-srv-" + generateCUID()
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Cov Crew', ?)`,
		crewID, wsID, "cov-"+crewID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, command, args_json)
		VALUES (?, ?, 'github', 'GitHub', 'stdio', 'npx', '["-y"]')`,
		serverID, crewID); err != nil {
		t.Fatalf("insert crew_mcp_server: %v", err)
	}
	return
}

// covMTBSeedBinding materialises one mcp_tool_bindings row directly.
func covMTBSeedBinding(t *testing.T, db *sql.DB, serverID, tool string, enabled bool, desc *string) {
	t.Helper()
	en := 0
	if enabled {
		en = 1
	}
	if _, err := db.Exec(`
		INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, description, enabled)
		VALUES (?, ?, 'crew', ?, ?, ?)`,
		"covmtb-b-"+generateCUID(), serverID, tool, desc, en); err != nil {
		t.Fatalf("seed binding %s: %v", tool, err)
	}
}

// covMTBSeedRegistryServer inserts one mcp_registry_servers row with the
// supplied trust_tier / featured flags. Other columns get sane defaults.
func covMTBSeedRegistryServer(t *testing.T, db *sql.DB, name, displayName, desc, category, trustTier string, featured bool) {
	t.Helper()
	f := 0
	if featured {
		f = 1
	}
	if _, err := db.Exec(`
		INSERT INTO mcp_registry_servers
			(id, name, display_name, description, category, trust_tier, is_featured, is_verified)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		name, name, displayName, desc, category, trustTier, f); err != nil {
		t.Fatalf("seed registry server %s: %v", name, err)
	}
}

// covMTBSeedToolCall inserts one mcp_tool_calls audit row.
func covMTBSeedToolCall(t *testing.T, db *sql.DB, id, wsID, agentID, serverID, tool, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO mcp_tool_calls
			(id, workspace_id, crew_id, agent_id, mcp_server_id, mcp_server_scope, tool_name, status, duration_ms, error_message)
		VALUES (?, ?, NULL, ?, ?, 'crew', ?, ?, 12, NULL)`,
		id, wsID, agentID, serverID, tool, status); err != nil {
		t.Fatalf("seed tool call %s: %v", id, err)
	}
}

func covMTBHandler(t *testing.T, db *sql.DB) *IntegrationHandler {
	t.Helper()
	return NewIntegrationHandler(db, newTestLogger())
}

// ---------------------------------------------------------------------------
// ListCrewIntegrationTools
// ---------------------------------------------------------------------------

func TestCovMTBListToolsHappy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	covMTBSeedBinding(t, db, serverID, "alpha", true, ptr("first"))
	covMTBSeedBinding(t, db, serverID, "beta", false, nil)
	h := covMTBHandler(t, db)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out []toolBindingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(out))
	}
	// ORDER BY tool_name ASC → alpha first.
	if out[0].ToolName != "alpha" || !out[0].Enabled {
		t.Errorf("alpha: got %+v", out[0])
	}
	if out[1].ToolName != "beta" || out[1].Enabled {
		t.Errorf("beta: got %+v", out[1])
	}
}

func TestCovMTBListToolsNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, _ := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", "does-not-exist")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// DB-error path: close the DB so assertCrewServerExists returns a non-ErrNoRows
// error, which respondCrewServerErr must surface as 500 (not 404).
func TestCovMTBListToolsDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)
	db.Close()

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// UpdateCrewIntegrationTool
// ---------------------------------------------------------------------------

func TestCovMTBUpdateToolForbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(map[string]any{"enabled": true})
	req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "foo")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovMTBUpdateToolEmptyName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(map[string]any{"enabled": true})
	req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "   ") // trims to empty
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovMTBUpdateToolNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, _ := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(map[string]any{"enabled": true})
	req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", "nope")
	req.SetPathValue("toolName", "foo")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovMTBUpdateToolInvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	req := httptest.NewRequest("PATCH", "/", strings.NewReader("{not-json"))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "foo")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Both enabled and description omitted → 400 "provide at least one".
func TestCovMTBUpdateToolNoFields(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	req := httptest.NewRequest("PATCH", "/", strings.NewReader("{}"))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "foo")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Description-only PATCH on a pre-disabled tool must NOT flip it back on
// (the COALESCE(?, existing) bug CodeRabbit caught). Asserts DB state.
func TestCovMTBUpdateToolDescriptionOnlyPreservesEnabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	covMTBSeedBinding(t, db, serverID, "danger", false, ptr("old"))
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(map[string]any{"description": "new desc"})
	req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "danger")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got toolBindingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Enabled {
		t.Errorf("description-only PATCH flipped disabled tool back on")
	}
	if got.Description == nil || *got.Description != "new desc" {
		t.Errorf("description not updated: %v", got.Description)
	}
}

// Fresh tool, enabled omitted → COALESCE(?, 1) defaults to enabled=1.
func TestCovMTBUpdateToolNewRowDefaultsEnabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(map[string]any{"description": "brand new"})
	req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "fresh")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND tool_name = 'fresh'`, serverID).Scan(&enabled); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if enabled != 1 {
		t.Errorf("new row should default enabled=1, got %d", enabled)
	}
}

// ---------------------------------------------------------------------------
// RefreshCrewIntegrationTools
// ---------------------------------------------------------------------------

func TestCovMTBRefreshForbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(refreshToolsRequest{})
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.RefreshCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovMTBRefreshNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, _ := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	body, _ := json.Marshal(refreshToolsRequest{})
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RefreshCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovMTBRefreshInvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	h := covMTBHandler(t, db)

	req := httptest.NewRequest("POST", "/", strings.NewReader("}{"))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RefreshCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Happy path: mix of a pre-existing tool (counts as updated) and two new
// tools (counted as created); an entry with a blank name is skipped.
func TestCovMTBRefreshCreatedUpdatedCounts(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	// Pre-existing row with an earlier created_at so the created/updated
	// split (created_at == now vs < now) classifies it as "updated".
	if _, err := db.Exec(`
		INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled, created_at, updated_at)
		VALUES (?, ?, 'crew', 'existing_tool', 1, '2000-01-01T00:00:00Z', '2000-01-01T00:00:00Z')`,
		"covmtb-old-"+generateCUID(), serverID); err != nil {
		t.Fatalf("preseed old binding: %v", err)
	}
	h := covMTBHandler(t, db)

	payload := refreshToolsRequest{Tools: []refreshToolEntry{
		{Name: "existing_tool", Description: ptr("refreshed")},
		{Name: "new_one", Description: ptr("n1")},
		{Name: "new_two"},
		{Name: "   "}, // skipped
	}}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RefreshCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["total"] != 4 {
		t.Errorf("total: got %d, want 4 (len(payload))", out["total"])
	}
	if out["created"] != 2 {
		t.Errorf("created: got %d, want 2", out["created"])
	}
	if out["updated"] != 1 {
		t.Errorf("updated: got %d, want 1", out["updated"])
	}

	// Three distinct rows total now exist (two new + the pre-existing).
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_bindings WHERE mcp_server_id = ?`, serverID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("row count: got %d, want 3 (blank name skipped)", n)
	}
}

// ---------------------------------------------------------------------------
// MCPRegistryHandler.List
// ---------------------------------------------------------------------------

func TestCovMTBRegistryListInvalidFilter(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPRegistryHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/?trust_tier=bogus", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("trust_tier=bogus status = %d, want 400", rr.Code)
	}

	req = httptest.NewRequest("GET", "/?featured=maybe", nil)
	rr = httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("featured=maybe status = %d, want 400", rr.Code)
	}
}

func TestCovMTBRegistryListFiltered(t *testing.T) {
	db := setupTestDB(t)
	covMTBSeedRegistryServer(t, db, "srv.anthropic", "Anthropic One", "an official", "official", "anthropic", true)
	covMTBSeedRegistryServer(t, db, "srv.community", "Community One", "a community", "misc", "community", false)
	h := NewMCPRegistryHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/?trust_tier=anthropic", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Servers []mcpRegistryServerRow `json:"servers"`
		Total   int                    `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 1 || len(out.Servers) != 1 {
		t.Fatalf("trust_tier filter: total=%d len=%d, want 1/1", out.Total, len(out.Servers))
	}
	if out.Servers[0].Name != "srv.anthropic" || out.Servers[0].TrustTier != "anthropic" {
		t.Errorf("unexpected server: %+v", out.Servers[0])
	}

	// featured=true must also narrow.
	req = httptest.NewRequest("GET", "/?featured=true", nil)
	rr = httptest.NewRecorder()
	h.List(rr, req)
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal featured: %v", err)
	}
	if out.Total != 1 || !out.Servers[0].IsFeatured {
		t.Errorf("featured filter: total=%d featured=%v", out.Total, out.Servers[0].IsFeatured)
	}
}

func TestCovMTBRegistryListDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPRegistryHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// MCPRegistryHandler.Search
// ---------------------------------------------------------------------------

// Empty q delegates to List — assert it returns the List shape (no "query"
// key would be present, and all rows come back).
func TestCovMTBRegistrySearchEmptyQDelegatesToList(t *testing.T) {
	db := setupTestDB(t)
	covMTBSeedRegistryServer(t, db, "srv.one", "One", "desc", "cat", "community", false)
	h := NewMCPRegistryHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/?q=", nil)
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, hasQuery := out["query"]; hasQuery {
		t.Errorf("empty q should delegate to List (no 'query' key), got %v", out)
	}
}

func TestCovMTBRegistrySearchInvalidFilter(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPRegistryHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/?q=git&trust_tier=nope", nil)
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovMTBRegistrySearchHappyWithFilter(t *testing.T) {
	db := setupTestDB(t)
	covMTBSeedRegistryServer(t, db, "github-mcp", "GitHub MCP", "git stuff", "vcs", "anthropic", true)
	covMTBSeedRegistryServer(t, db, "slack-mcp", "Slack MCP", "chat stuff", "chat", "community", false)
	h := NewMCPRegistryHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/?q=git&trust_tier=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Servers []mcpRegistryServerRow `json:"servers"`
		Total   int                    `json:"total"`
		Query   string                 `json:"query"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Query != "git" {
		t.Errorf("query echo: got %q", out.Query)
	}
	if out.Total != 1 || len(out.Servers) != 1 || out.Servers[0].Name != "github-mcp" {
		t.Errorf("search result: total=%d servers=%+v", out.Total, out.Servers)
	}
}

func TestCovMTBRegistrySearchDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPRegistryHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/?q=anything", nil)
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// MCPRegistryHandler.Sync
// ---------------------------------------------------------------------------

func TestCovMTBRegistrySyncForbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPRegistryHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/", nil)
	req = withWorkspaceUser(req, "u", "ws", "VIEWER")
	rr := httptest.NewRecorder()
	h.Sync(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// First Sync as a manager → 202 Accepted (and arms the cooldown). A second
// immediate Sync must hit the 1h cooldown → 429. We point the registry URL
// at an httptest server so the background goroutine doesn't reach the live
// network.
func TestCovMTBRegistrySyncAcceptedThenCooldown(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPRegistryHandler(db, newTestLogger())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"nextCursor":"","count":0}}`))
	}))
	t.Cleanup(srv.Close)
	prev := mcpRegistryURL
	mcpRegistryURL = srv.URL
	t.Cleanup(func() { mcpRegistryURL = prev })

	req := httptest.NewRequest("POST", "/", nil)
	req = withWorkspaceUser(req, "u", "ws", "OWNER")
	rr := httptest.NewRecorder()
	h.Sync(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first sync status = %d, want 202", rr.Code)
	}

	req2 := httptest.NewRequest("POST", "/", nil)
	req2 = withWorkspaceUser(req2, "u", "ws", "OWNER")
	rr2 := httptest.NewRecorder()
	h.Sync(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second sync status = %d, want 429", rr2.Code)
	}
}

// ---------------------------------------------------------------------------
// MCPAuditHandler.List
// ---------------------------------------------------------------------------

func TestCovMTBAuditMissingWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewMCPAuditHandler(db, newTestLogger())

	// No workspace in context → WorkspaceIDFromContext returns "" → 400.
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovMTBAuditEmptyList(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMCPAuditHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// No rows → "[]", never null.
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("empty audit should be [], got %q", rr.Body.String())
	}
}

// Happy path with every optional filter applied (agent_id / server_id /
// status / since / until), asserting the matching row comes back.
func TestCovMTBAuditFiltersHappy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := covMTBSeedCrewServer(t, db, wsID)
	agentID := seedAgentRow(t, db, "covmtb-agent", wsID, crewID, "Cov Agent", "cov-agent", "AGENT")
	covMTBSeedToolCall(t, db, "tc-match", wsID, agentID, serverID, "do_thing", "success")
	covMTBSeedToolCall(t, db, "tc-other-agent", wsID, "someone-else", serverID, "do_thing", "error")
	h := NewMCPAuditHandler(db, newTestLogger())

	q := "/?agent_id=" + agentID + "&server_id=" + serverID +
		"&status=success&since=2000-01-01&until=2999-01-01"
	req := httptest.NewRequest("GET", q, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out []mcpToolCallEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("filtered audit: got %d rows, want 1", len(out))
	}
	if out[0].ID != "tc-match" || out[0].Status != "success" || out[0].ToolName != "do_thing" {
		t.Errorf("unexpected row: %+v", out[0])
	}
}

func TestCovMTBAuditDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMCPAuditHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
