package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func setupIntegrationTest(t *testing.T) (*sql.DB, *IntegrationHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewIntegrationHandler(db, logger)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return db, h, wsID, userID
}

func seedCrew(t *testing.T, db *sql.DB, id, wsID, name, slug string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		id, wsID, name, slug)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}
}

func seedAgent(t *testing.T, db *sql.DB, id, wsID, crewID, name, slug string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES (?, ?, ?, ?, ?, 'IDLE')`,
		id, wsID, crewID, name, slug)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
}

func seedCredential(t *testing.T, db *sql.DB, id, wsID, name string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at) VALUES (?, ?, ?, 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', 'test-user-id', datetime('now'), datetime('now'))`,
		id, wsID, name)
	if err != nil {
		t.Fatalf("insert credential: %v", err)
	}
}

func makeReq(t *testing.T, method, path string, body interface{}, wsID, role string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := withWorkspace(req.Context(), wsID, role)
	ctx = withUser(ctx, &AuthUser{ID: "test-user-id", Email: "test@example.com"})
	return req.WithContext(ctx)
}

func TestWorkspaceIntegrations_CRUD(t *testing.T) {
	_, h, wsID, _ := setupIntegrationTest(t)

	// Create
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "gmail", "display_name": "Google Gmail", "transport": "streamable-http",
		"endpoint": "https://mcp.example.com/gmail",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var created workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&created)
	if created.Name != "gmail" || created.DisplayName != "Google Gmail" {
		t.Errorf("unexpected response: %+v", created)
	}
	if !created.Enabled {
		t.Error("expected enabled=true")
	}

	// List
	req = makeReq(t, "GET", "/api/v1/integrations", nil, wsID, "MEMBER")
	rr = httptest.NewRecorder()
	h.ListWorkspaceIntegrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rr.Code)
	}
	var list []workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&list)
	if len(list) != 1 {
		t.Fatalf("expected 1 integration, got %d", len(list))
	}

	// Get
	req = makeReq(t, "GET", "/api/v1/integrations/"+created.ID, nil, wsID, "MEMBER")
	req.SetPathValue("integrationId", created.ID)
	rr = httptest.NewRecorder()
	h.GetWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rr.Code)
	}

	// Update (disable)
	falseVal := false
	req = makeReq(t, "PATCH", "/api/v1/integrations/"+created.ID, map[string]interface{}{
		"enabled": falseVal,
	}, wsID, "ADMIN")
	req.SetPathValue("integrationId", created.ID)
	rr = httptest.NewRecorder()
	h.UpdateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var updated workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&updated)
	if updated.Enabled {
		t.Error("expected enabled=false after update")
	}

	// Delete
	req = makeReq(t, "DELETE", "/api/v1/integrations/"+created.ID, nil, wsID, "ADMIN")
	req.SetPathValue("integrationId", created.ID)
	rr = httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: status = %d", rr.Code)
	}

	// List again — empty
	req = makeReq(t, "GET", "/api/v1/integrations", nil, wsID, "MEMBER")
	rr = httptest.NewRecorder()
	h.ListWorkspaceIntegrations(rr, req)
	json.NewDecoder(rr.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("expected 0 integrations after delete, got %d", len(list))
	}
}

func TestWorkspaceIntegration_InvalidTransport(t *testing.T) {
	_, h, wsID, _ := setupIntegrationTest(t)

	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "bad", "transport": "grpc",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestWorkspaceIntegration_ForbiddenForMember(t *testing.T) {
	_, h, wsID, _ := setupIntegrationTest(t)

	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "test",
	}, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestCrewIntegrations_CRUD(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")

	// Create crew integration
	req := makeReq(t, "POST", "/api/v1/crews/crew1/integrations", map[string]string{
		"name": "slack", "display_name": "Slack", "transport": "streamable-http",
		"endpoint": "https://mcp.example.com/slack",
	}, wsID, "MANAGER")
	req.SetPathValue("crewId", "crew1")
	rr := httptest.NewRecorder()
	h.CreateCrewIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create crew integration: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var created crewMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&created)
	if created.Name != "slack" {
		t.Errorf("unexpected name: %s", created.Name)
	}

	// List
	req = makeReq(t, "GET", "/api/v1/crews/crew1/integrations", nil, wsID, "MEMBER")
	req.SetPathValue("crewId", "crew1")
	rr = httptest.NewRecorder()
	h.ListCrewIntegrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list crew integrations: status = %d", rr.Code)
	}
	var crewList []crewMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&crewList)
	if len(crewList) != 1 {
		t.Fatalf("expected 1 crew integration, got %d", len(crewList))
	}

	// Delete (requires manage = OWNER/ADMIN)
	req = makeReq(t, "DELETE", "/api/v1/crews/crew1/integrations/"+created.ID, nil, wsID, "ADMIN")
	req.SetPathValue("crewId", "crew1")
	req.SetPathValue("integrationId", created.ID)
	rr = httptest.NewRecorder()
	h.DeleteCrewIntegration(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete crew integration: status = %d", rr.Code)
	}
}

func TestAgentMCPBindings_CRUD(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")
	seedAgent(t, db, "agent1", wsID, "crew1", "Pepa", "pepa")
	seedCredential(t, db, "cred1", wsID, "pepa-gmail-token")

	// Create workspace integration first
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "gmail", "display_name": "Gmail", "transport": "streamable-http",
		"endpoint": "https://mcp.example.com/gmail",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup ws integration: status = %d", rr.Code)
	}
	var wsServer workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&wsServer)

	// Create agent binding with credential
	credID := "cred1"
	req = makeReq(t, "POST", "/api/v1/agents/agent1/integrations", map[string]interface{}{
		"mcp_server_id":    wsServer.ID,
		"mcp_server_scope": "workspace",
		"credential_id":    credID,
	}, wsID, "MANAGER")
	req.SetPathValue("agentId", "agent1")
	rr = httptest.NewRecorder()
	h.CreateAgentBinding(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create binding: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var binding agentMCPBindingResponse
	json.NewDecoder(rr.Body).Decode(&binding)
	if binding.MCPServerID != wsServer.ID || *binding.CredentialID != credID {
		t.Errorf("unexpected binding: %+v", binding)
	}

	// List bindings
	req = makeReq(t, "GET", "/api/v1/agents/agent1/integrations", nil, wsID, "MEMBER")
	req.SetPathValue("agentId", "agent1")
	rr = httptest.NewRecorder()
	h.ListAgentBindings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list bindings: status = %d", rr.Code)
	}
	var bindings []agentMCPBindingResponse
	json.NewDecoder(rr.Body).Decode(&bindings)
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}

	// Delete
	req = makeReq(t, "DELETE", "/api/v1/agents/agent1/integrations/"+binding.ID, nil, wsID, "MANAGER")
	req.SetPathValue("agentId", "agent1")
	req.SetPathValue("integrationId", binding.ID)
	rr = httptest.NewRecorder()
	h.DeleteAgentBinding(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete binding: status = %d", rr.Code)
	}
}

func TestCascadeResolution_TwoAgentsSameServerDifferentCreds(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")
	seedAgent(t, db, "pepa", wsID, "crew1", "Pepa", "pepa")
	seedAgent(t, db, "franta", wsID, "crew1", "Franta", "franta")
	seedCredential(t, db, "cred-pepa", wsID, "pepa-gmail")
	seedCredential(t, db, "cred-franta", wsID, "franta-gmail")


	// Create workspace Gmail integration
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "gmail", "display_name": "Gmail", "transport": "streamable-http",
		"endpoint": "https://mcp.example.com/gmail",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create gmail: %d %s", rr.Code, rr.Body.String())
	}
	var gmail workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&gmail)

	// Bind Pepa to Gmail with pepa's credential
	req = makeReq(t, "POST", "/api/v1/agents/pepa/integrations", map[string]interface{}{
		"mcp_server_id": gmail.ID, "mcp_server_scope": "workspace", "credential_id": "cred-pepa",
	}, wsID, "MANAGER")
	req.SetPathValue("agentId", "pepa")
	rr = httptest.NewRecorder()
	h.CreateAgentBinding(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("bind pepa: %d %s", rr.Code, rr.Body.String())
	}

	// Bind Franta to Gmail with franta's credential
	req = makeReq(t, "POST", "/api/v1/agents/franta/integrations", map[string]interface{}{
		"mcp_server_id": gmail.ID, "mcp_server_scope": "workspace", "credential_id": "cred-franta",
	}, wsID, "MANAGER")
	req.SetPathValue("agentId", "franta")
	rr = httptest.NewRecorder()
	h.CreateAgentBinding(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("bind franta: %d %s", rr.Code, rr.Body.String())
	}

	// Resolve for Pepa — should have Gmail with pepa's credential
	req = makeReq(t, "GET", "/api/v1/agents/pepa/integrations/resolved", nil, wsID, "MEMBER")
	req.SetPathValue("agentId", "pepa")
	rr = httptest.NewRecorder()
	h.ResolveAgentIntegrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve pepa: %d %s", rr.Code, rr.Body.String())
	}
	var pepaResolved []ResolvedIntegration
	json.NewDecoder(rr.Body).Decode(&pepaResolved)
	if len(pepaResolved) != 1 {
		t.Fatalf("expected 1 resolved for pepa, got %d", len(pepaResolved))
	}
	if pepaResolved[0].Name != "gmail" {
		t.Errorf("expected gmail, got %s", pepaResolved[0].Name)
	}
	if pepaResolved[0].CredentialID == nil || *pepaResolved[0].CredentialID != "cred-pepa" {
		t.Errorf("expected pepa credential, got %v", pepaResolved[0].CredentialID)
	}

	// Resolve for Franta — should have Gmail with franta's credential
	req = makeReq(t, "GET", "/api/v1/agents/franta/integrations/resolved", nil, wsID, "MEMBER")
	req.SetPathValue("agentId", "franta")
	rr = httptest.NewRecorder()
	h.ResolveAgentIntegrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve franta: %d", rr.Code)
	}
	var frantaResolved []ResolvedIntegration
	json.NewDecoder(rr.Body).Decode(&frantaResolved)
	if len(frantaResolved) != 1 {
		t.Fatalf("expected 1 resolved for franta, got %d", len(frantaResolved))
	}
	if frantaResolved[0].CredentialID == nil || *frantaResolved[0].CredentialID != "cred-franta" {
		t.Errorf("expected franta credential, got %v", frantaResolved[0].CredentialID)
	}
}

func TestCascadeResolution_CrewOverridesWorkspace(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")
	seedAgent(t, db, "agent1", wsID, "crew1", "Agent1", "agent1")

	// Create workspace Gmail
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "gmail", "display_name": "WS Gmail", "transport": "streamable-http",
		"endpoint": "https://ws.example.com/gmail",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	var wsGmail workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&wsGmail)

	// Create crew Gmail override with different endpoint
	req = makeReq(t, "POST", "/api/v1/crews/crew1/integrations", map[string]interface{}{
		"name": "gmail", "display_name": "Crew Gmail", "transport": "streamable-http",
		"endpoint": "https://crew.example.com/gmail",
		"workspace_mcp_server_id": wsGmail.ID,
	}, wsID, "MANAGER")
	req.SetPathValue("crewId", "crew1")
	rr = httptest.NewRecorder()
	h.CreateCrewIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create crew gmail: %d %s", rr.Code, rr.Body.String())
	}

	// Resolve for agent1 — crew override should win
	req = makeReq(t, "GET", "/api/v1/agents/agent1/integrations/resolved", nil, wsID, "MEMBER")
	req.SetPathValue("agentId", "agent1")
	rr = httptest.NewRecorder()
	h.ResolveAgentIntegrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d", rr.Code)
	}
	var resolved []ResolvedIntegration
	json.NewDecoder(rr.Body).Decode(&resolved)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved, got %d", len(resolved))
	}
	if resolved[0].Scope != "crew" {
		t.Errorf("expected crew scope, got %s", resolved[0].Scope)
	}
	if resolved[0].DisplayName != "Crew Gmail" {
		t.Errorf("expected 'Crew Gmail', got %s", resolved[0].DisplayName)
	}
	if resolved[0].Endpoint == nil || *resolved[0].Endpoint != "https://crew.example.com/gmail" {
		t.Errorf("expected crew endpoint, got %v", resolved[0].Endpoint)
	}
}

func TestCascadeResolution_AgentOptOut(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")
	seedAgent(t, db, "agent1", wsID, "crew1", "Agent1", "agent1")

	// Create workspace integration
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "slack", "display_name": "Slack",
		"transport": "stdio", "command": "npx",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	var slack workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&slack)

	// Agent opts out
	disabledBool := false
	req = makeReq(t, "POST", "/api/v1/agents/agent1/integrations", map[string]interface{}{
		"mcp_server_id": slack.ID, "mcp_server_scope": "workspace", "enabled": disabledBool,
	}, wsID, "MANAGER")
	req.SetPathValue("agentId", "agent1")
	rr = httptest.NewRecorder()
	h.CreateAgentBinding(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("opt-out binding: %d %s", rr.Code, rr.Body.String())
	}

	// Resolve — should return empty (opted out)
	req = makeReq(t, "GET", "/api/v1/agents/agent1/integrations/resolved", nil, wsID, "MEMBER")
	req.SetPathValue("agentId", "agent1")
	rr = httptest.NewRecorder()
	h.ResolveAgentIntegrations(rr, req)
	var resolved []ResolvedIntegration
	json.NewDecoder(rr.Body).Decode(&resolved)
	if len(resolved) != 0 {
		t.Errorf("expected 0 resolved (agent opted out), got %d", len(resolved))
	}
}

func TestDeleteWorkspaceIntegration_Cascades(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")
	seedAgent(t, db, "agent1", wsID, "crew1", "Agent1", "agent1")

	// Create workspace integration
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "gmail", "display_name": "Gmail",
		"endpoint": "https://mcp.example.com/gmail",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	var ws workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&ws)

	// Create agent binding
	req = makeReq(t, "POST", "/api/v1/agents/agent1/integrations", map[string]interface{}{
		"mcp_server_id": ws.ID, "mcp_server_scope": "workspace",
	}, wsID, "MANAGER")
	req.SetPathValue("agentId", "agent1")
	rr = httptest.NewRecorder()
	h.CreateAgentBinding(rr, req)

	// Delete workspace integration — should cascade delete binding
	req = makeReq(t, "DELETE", "/api/v1/integrations/"+ws.ID, nil, wsID, "ADMIN")
	req.SetPathValue("integrationId", ws.ID)
	rr = httptest.NewRecorder()
	h.DeleteWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d", rr.Code)
	}

	// Verify binding is gone
	var bindCount int
	db.QueryRow("SELECT COUNT(*) FROM agent_mcp_bindings WHERE agent_id = 'agent1'").Scan(&bindCount)
	if bindCount != 0 {
		t.Errorf("expected 0 bindings after cascade delete, got %d", bindCount)
	}
}

func TestMigration_CreatesAllTables(t *testing.T) {
	db := setupTestDB(t)

	tables := []string{"workspace_mcp_servers", "crew_mcp_servers", "agent_mcp_bindings", "mcp_tool_calls"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}
}

// Suppress unused import warnings
var _ = context.Background
