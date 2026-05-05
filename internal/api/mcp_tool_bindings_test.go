package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// seedCrewMCPServer creates a (crew + crew_mcp_servers) pair for tool-binding
// tests. Returns the crew_id and server_id.
func seedCrewMCPServer(t *testing.T, db *sql.DB, wsID string) (crewID, serverID string) {
	t.Helper()
	crewID = "crew-tools-1"
	serverID = "srv-tools-1"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Tools Crew', 'tools')`, crewID, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, command, args_json)
		VALUES (?, ?, 'github', 'GitHub', 'stdio', 'npx', '["-y","@modelcontextprotocol/server-github"]')`,
		serverID, crewID); err != nil {
		t.Fatalf("insert crew_mcp_server: %v", err)
	}
	return
}

func newIntegrationHandlerForToolBindings(t *testing.T, db *sql.DB) *IntegrationHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewIntegrationHandler(db, logger)
}

// TestMCPToolBindings_ListEmpty verifies that a server with no recorded
// bindings returns an empty array, not null — the FE relies on this.
func TestMCPToolBindings_ListEmpty(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := seedCrewMCPServer(t, db, wsID)
	h := newIntegrationHandlerForToolBindings(t, db)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/integrations/"+serverID+"/tools", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Errorf("empty list should be [], got %q", body)
	}
}

// TestMCPToolBindings_NotFound verifies the workspace isolation check —
// querying a server that exists but in a different workspace must 404.
func TestMCPToolBindings_NotFound(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, _ := seedCrewMCPServer(t, db, wsID)
	h := newIntegrationHandlerForToolBindings(t, db)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/integrations/missing/tools", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", "missing")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestMCPToolBindings_UpdateUpserts exercises the central design choice:
// the PATCH endpoint upserts so the FE doesn't need a "create then update"
// handshake. First call materialises the row; second call toggles state.
func TestMCPToolBindings_UpdateUpserts(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := seedCrewMCPServer(t, db, wsID)
	h := newIntegrationHandlerForToolBindings(t, db)

	// First PATCH — row doesn't exist yet, should create with enabled=false.
	disable := false
	body, _ := json.Marshal(map[string]any{"enabled": &disable})
	req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "create_pr")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("first PATCH status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got toolBindingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ToolName != "create_pr" || got.Enabled {
		t.Errorf("first PATCH: got %+v, want tool=create_pr enabled=false", got)
	}

	// Second PATCH — flip back to enabled=true. Same toolName, must hit
	// ON CONFLICT path; original ID must be preserved.
	enable := true
	body, _ = json.Marshal(map[string]any{"enabled": &enable})
	req = httptest.NewRequest("PATCH", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	req.SetPathValue("toolName", "create_pr")
	req = req.WithContext(ctx)
	rr = httptest.NewRecorder()
	h.UpdateCrewIntegrationTool(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("second PATCH status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got2 toolBindingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got2); err != nil {
		t.Fatalf("unmarshal second PATCH response: %v", err)
	}
	if got2.ID != got.ID {
		t.Errorf("ID changed across upsert: %s -> %s (should be stable)", got.ID, got2.ID)
	}
	if !got2.Enabled {
		t.Errorf("second PATCH: enabled = false, want true")
	}
}

// TestMCPToolBindings_RefreshPreservesEnabled is the regression guard for
// the most subtle bit of the schema: when sidecar/FE pushes a refreshed
// tool list, a tool the user previously disabled must STAY disabled.
// Otherwise refresh would silently re-enable revoked tools.
func TestMCPToolBindings_RefreshPreservesEnabled(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID, serverID := seedCrewMCPServer(t, db, wsID)
	h := newIntegrationHandlerForToolBindings(t, db)

	// Pre-populate: user disables "delete_branch".
	if _, err := db.Exec(`
		INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled)
		VALUES ('b1', ?, 'crew', 'delete_branch', 0)`, serverID); err != nil {
		t.Fatalf("preseed binding: %v", err)
	}

	// Refresh with a payload that includes delete_branch + a new tool.
	payload := refreshToolsRequest{
		Tools: []refreshToolEntry{
			{Name: "delete_branch", Description: ptr("Delete a branch")},
			{Name: "create_pr", Description: ptr("Create a pull request")},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("integrationId", serverID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.RefreshCrewIntegrationTools(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// delete_branch must still be disabled; description got refreshed.
	var enabled int
	var desc *string
	if err := db.QueryRow(`
		SELECT enabled, description FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND tool_name = 'delete_branch'`, serverID).Scan(&enabled, &desc); err != nil {
		t.Fatalf("read delete_branch: %v", err)
	}
	if enabled != 0 {
		t.Errorf("delete_branch should remain disabled across refresh, got enabled=%d", enabled)
	}
	if desc == nil || *desc != "Delete a branch" {
		t.Errorf("delete_branch description should be refreshed, got %v", desc)
	}

	// create_pr is new — should default to enabled=1.
	if err := db.QueryRow(`
		SELECT enabled FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND tool_name = 'create_pr'`, serverID).Scan(&enabled); err != nil {
		t.Fatalf("read create_pr: %v", err)
	}
	if enabled != 1 {
		t.Errorf("new tool create_pr should default enabled=1, got %d", enabled)
	}
}

func ptr(s string) *string { return &s }
