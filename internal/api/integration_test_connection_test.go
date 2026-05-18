package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// integration_test_connection.go — loadAndTestConnection,
// TestWorkspaceIntegrationConnection, TestCrewIntegrationConnection.
//
// These three live next to each other; the workspace/crew handlers
// delegate to loadAndTestConnection which then routes to
// testMCPConnection. We cover the lookup + 404 + transport-routing
// branches without exercising real HTTP (the streamable-http happy
// path is partially covered by testMCPConnection / SSRF-safe transport
// tests already in the package).
// ---------------------------------------------------------------------------

func newIntegrationHandlerForTest(t *testing.T) *IntegrationHandler {
	t.Helper()
	db := setupTestDB(t)
	return NewIntegrationHandler(db, newTestLogger())
}

// seedWorkspaceMCP inserts a workspace_mcp_servers row with the given
// id/transport/endpoint. Returns the inserted id for convenience.
func seedWorkspaceMCP(t *testing.T, h *IntegrationHandler, id, wsID, transport, endpoint string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, enabled)
		VALUES (?, ?, ?, ?, ?, ?, 1)`,
		id, wsID, id, id, transport, endpoint); err != nil {
		t.Fatalf("seed workspace_mcp_servers %s: %v", id, err)
	}
}

func seedCrewMCP(t *testing.T, h *IntegrationHandler, id, crewID, transport, endpoint string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO crew_mcp_servers
		(id, crew_id, name, display_name, transport, endpoint, enabled)
		VALUES (?, ?, ?, ?, ?, ?, 1)`,
		id, crewID, id, id, transport, endpoint); err != nil {
		t.Fatalf("seed crew_mcp_servers %s: %v", id, err)
	}
}

// ---- TestWorkspaceIntegrationConnection ----

func TestWorkspaceIntegrationConnection_NotFound(t *testing.T) {
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("POST", "/api/v1/integrations/missing/test", nil)
	req.SetPathValue("integrationId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestWorkspaceIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestWorkspaceIntegrationConnection_CrossWorkspace_NotFound(t *testing.T) {
	// Integration exists in another workspace; the WHERE clause
	// scopes by workspace_id so the caller's lookup must 404 — pins
	// the no-cross-workspace-leak contract.
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-int-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-int')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedWorkspaceMCP(t, h, "int-foreign", wsB, "streamable-http", "https://api.example/mcp")

	req := httptest.NewRequest("POST", "/api/v1/integrations/int-foreign/test", nil)
	req.SetPathValue("integrationId", "int-foreign")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.TestWorkspaceIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace = %d, want 404", rr.Code)
	}
}

func TestWorkspaceIntegrationConnection_SoftDeleted_NotFound(t *testing.T) {
	// The query filter is `deleted_at IS NULL`; a soft-deleted row must
	// be invisible.
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedWorkspaceMCP(t, h, "int-gone", wsID, "streamable-http", "https://api.example/mcp")
	if _, err := h.db.Exec(`UPDATE workspace_mcp_servers SET deleted_at = datetime('now') WHERE id = 'int-gone'`); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/integrations/int-gone/test", nil)
	req.SetPathValue("integrationId", "int-gone")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestWorkspaceIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("soft-deleted = %d, want 404", rr.Code)
	}
}

func TestWorkspaceIntegrationConnection_StdioTransport_RoutesToSkipped(t *testing.T) {
	// Stdio servers are tested at runtime inside the container — the
	// handler returns the documented "skipped" status without
	// attempting any network I/O. Pin the routing.
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedWorkspaceMCP(t, h, "int-stdio", wsID, "stdio", "")

	req := httptest.NewRequest("POST", "/api/v1/integrations/int-stdio/test", nil)
	req.SetPathValue("integrationId", "int-stdio")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestWorkspaceIntegrationConnection(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got testConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if got.Status != "skipped" {
		t.Errorf("Status = %q, want \"skipped\"", got.Status)
	}
}

func TestWorkspaceIntegrationConnection_UnknownTransport_RoutesToErrorWithName(t *testing.T) {
	// An unrecognised transport surfaces as "error" with the offending
	// transport name in the message — operators need that hint to fix
	// their config (silent rejection would leave them debugging in the
	// dark).
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedWorkspaceMCP(t, h, "int-bogus", wsID, "ftp", "ftp://x/mcp")

	req := httptest.NewRequest("POST", "/api/v1/integrations/int-bogus/test", nil)
	req.SetPathValue("integrationId", "int-bogus")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestWorkspaceIntegrationConnection(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got testConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if got.Status != "error" {
		t.Errorf("Status = %q, want \"error\"", got.Status)
	}
	if got.Message == "" || !intConnContains(got.Message, "ftp") {
		t.Errorf("Message = %q, want it to mention the offending transport \"ftp\"", got.Message)
	}
}

// ---- TestCrewIntegrationConnection ----

func TestCrewIntegrationConnection_CrewMissing_404(t *testing.T) {
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("POST", "/api/v1/crews/missing/integrations/anything/test", nil)
	req.SetPathValue("crewId", "missing")
	req.SetPathValue("integrationId", "anything")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestCrewIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (crew check fires before integration lookup)", rr.Code)
	}
}

func TestCrewIntegrationConnection_CrossWorkspaceCrew_404(t *testing.T) {
	// Crew exists in another workspace — the explicit workspace_id
	// guard in TestCrewIntegrationConnection must catch it before the
	// integration lookup runs (otherwise a foreign crew's MCP servers
	// would be testable cross-tenant).
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-int-crew-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-int-c')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedCrewRow(t, h.db, "crew-foreign-int", wsB, "F", "f-int-c")

	req := httptest.NewRequest("POST", "/api/v1/crews/crew-foreign-int/integrations/x/test", nil)
	req.SetPathValue("crewId", "crew-foreign-int")
	req.SetPathValue("integrationId", "x")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.TestCrewIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace crew = %d, want 404 (no cross-tenant MCP probe)", rr.Code)
	}
}

func TestCrewIntegrationConnection_IntegrationMissing_404(t *testing.T) {
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-a", wsID, "A", "alpha-int")

	req := httptest.NewRequest("POST", "/api/v1/crews/crew-a/integrations/missing/test", nil)
	req.SetPathValue("crewId", "crew-a")
	req.SetPathValue("integrationId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestCrewIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCrewIntegrationConnection_StdioTransport_RoutesToSkipped(t *testing.T) {
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-b", wsID, "B", "beta-int")
	seedCrewMCP(t, h, "crew-int-stdio", "crew-b", "stdio", "")

	req := httptest.NewRequest("POST", "/api/v1/crews/crew-b/integrations/crew-int-stdio/test", nil)
	req.SetPathValue("crewId", "crew-b")
	req.SetPathValue("integrationId", "crew-int-stdio")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestCrewIntegrationConnection(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got testConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if got.Status != "skipped" {
		t.Errorf("Status = %q, want \"skipped\"", got.Status)
	}
}

func TestCrewIntegrationConnection_SoftDeleted_404(t *testing.T) {
	h := newIntegrationHandlerForTest(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-c", wsID, "C", "gamma-int")
	seedCrewMCP(t, h, "crew-int-gone", "crew-c", "stdio", "")
	if _, err := h.db.Exec(`UPDATE crew_mcp_servers SET deleted_at = datetime('now') WHERE id = 'crew-int-gone'`); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/crews/crew-c/integrations/crew-int-gone/test", nil)
	req.SetPathValue("crewId", "crew-c")
	req.SetPathValue("integrationId", "crew-int-gone")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestCrewIntegrationConnection(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("soft-deleted = %d, want 404", rr.Code)
	}
}

// intConnContains is a tiny substring helper. Local name avoids a
// collision with the `contains` helper defined in agent_inbox_test.go.
func intConnContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
