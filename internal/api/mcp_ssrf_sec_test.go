package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecMCPSSRF_* verify that MCP integration CRUD handlers reject
// endpoints pointing at internal/loopback/link-local addresses (SSRF),
// while still accepting legitimate public https endpoints.

func wsIntegrationCount(t *testing.T, h *IntegrationHandler) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM workspace_mcp_servers").Scan(&n); err != nil {
		t.Fatalf("count workspace_mcp_servers: %v", err)
	}
	return n
}

func crewIntegrationCount(t *testing.T, h *IntegrationHandler) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM crew_mcp_servers").Scan(&n); err != nil {
		t.Fatalf("count crew_mcp_servers: %v", err)
	}
	return n
}

func TestSecMCPSSRF_CreateWorkspaceRejectsInternalEndpoint(t *testing.T) {
	_, h, wsID, _ := setupIntegrationTest(t)

	for _, endpoint := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://127.0.0.1:6379",
	} {
		req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
			"name": "evil", "transport": "streamable-http", "endpoint": endpoint,
		}, wsID, "ADMIN")
		rr := httptest.NewRecorder()
		h.CreateWorkspaceIntegration(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("endpoint %q: expected 400, got %d (%s)", endpoint, rr.Code, rr.Body.String())
		}
	}
	if n := wsIntegrationCount(t, h); n != 0 {
		t.Fatalf("expected no rows inserted for SSRF endpoints, got %d", n)
	}
}

func TestSecMCPSSRF_CreateWorkspaceAllowsPublicEndpoint(t *testing.T) {
	_, h, wsID, _ := setupIntegrationTest(t)

	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "good", "transport": "streamable-http", "endpoint": "https://mcp.example.com",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for public endpoint, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := wsIntegrationCount(t, h); n != 1 {
		t.Fatalf("expected 1 row inserted, got %d", n)
	}
}

func TestSecMCPSSRF_UpdateWorkspaceRejectsInternalEndpoint(t *testing.T) {
	_, h, wsID, _ := setupIntegrationTest(t)

	// Seed a valid integration first.
	req := makeReq(t, "POST", "/api/v1/integrations", map[string]string{
		"name": "good", "transport": "streamable-http", "endpoint": "https://mcp.example.com",
	}, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.CreateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup: %d (%s)", rr.Code, rr.Body.String())
	}
	var created workspaceMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&created)

	// Attempt to repoint at metadata endpoint.
	bad := "http://169.254.169.254/latest/meta-data"
	req = makeReq(t, "PATCH", "/api/v1/integrations/"+created.ID, map[string]interface{}{
		"endpoint": bad,
	}, wsID, "ADMIN")
	req.SetPathValue("integrationId", created.ID)
	rr = httptest.NewRecorder()
	h.UpdateWorkspaceIntegration(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on SSRF update, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Endpoint must be unchanged in the DB.
	var stored string
	if err := h.db.QueryRow("SELECT endpoint FROM workspace_mcp_servers WHERE id = ?", created.ID).Scan(&stored); err != nil {
		t.Fatalf("read back endpoint: %v", err)
	}
	if stored != "https://mcp.example.com" {
		t.Fatalf("endpoint was mutated to %q despite rejected update", stored)
	}
}

func TestSecMCPSSRF_CreateCrewRejectsInternalEndpoint(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")

	for _, endpoint := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://127.0.0.1:6379",
	} {
		req := makeReq(t, "POST", "/api/v1/crews/crew1/integrations", map[string]string{
			"name": "evil", "transport": "streamable-http", "endpoint": endpoint,
		}, wsID, "MANAGER")
		req.SetPathValue("crewId", "crew1")
		rr := httptest.NewRecorder()
		h.CreateCrewIntegration(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("endpoint %q: expected 400, got %d (%s)", endpoint, rr.Code, rr.Body.String())
		}
	}
	if n := crewIntegrationCount(t, h); n != 0 {
		t.Fatalf("expected no crew rows inserted for SSRF endpoints, got %d", n)
	}
}

func TestSecMCPSSRF_CreateCrewAllowsPublicEndpoint(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")

	req := makeReq(t, "POST", "/api/v1/crews/crew1/integrations", map[string]string{
		"name": "good", "transport": "streamable-http", "endpoint": "https://mcp.example.com",
	}, wsID, "MANAGER")
	req.SetPathValue("crewId", "crew1")
	rr := httptest.NewRecorder()
	h.CreateCrewIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for public crew endpoint, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := crewIntegrationCount(t, h); n != 1 {
		t.Fatalf("expected 1 crew row inserted, got %d", n)
	}
}

func TestSecMCPSSRF_UpdateCrewRejectsInternalEndpoint(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crew1", wsID, "Finance", "finance")

	req := makeReq(t, "POST", "/api/v1/crews/crew1/integrations", map[string]string{
		"name": "good", "transport": "streamable-http", "endpoint": "https://mcp.example.com",
	}, wsID, "MANAGER")
	req.SetPathValue("crewId", "crew1")
	rr := httptest.NewRecorder()
	h.CreateCrewIntegration(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup crew: %d (%s)", rr.Code, rr.Body.String())
	}
	var created crewMCPServerResponse
	json.NewDecoder(rr.Body).Decode(&created)

	bad := "http://127.0.0.1:6379"
	req = makeReq(t, "PATCH", "/api/v1/crews/crew1/integrations/"+created.ID, map[string]interface{}{
		"endpoint": bad,
	}, wsID, "ADMIN")
	req.SetPathValue("crewId", "crew1")
	req.SetPathValue("integrationId", created.ID)
	rr = httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on SSRF crew update, got %d (%s)", rr.Code, rr.Body.String())
	}

	var stored string
	if err := h.db.QueryRow("SELECT endpoint FROM crew_mcp_servers WHERE id = ?", created.ID).Scan(&stored); err != nil {
		t.Fatalf("read back crew endpoint: %v", err)
	}
	if stored != "https://mcp.example.com" {
		t.Fatalf("crew endpoint was mutated to %q despite rejected update", stored)
	}
}
