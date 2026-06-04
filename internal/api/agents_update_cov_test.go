package api

// Coverage tests for agents_update.go — the AgentHandler.Update method.
//
// Update was only ~31% covered; these tests walk every validation and
// error branch plus the happy update path, asserting the persisted DB
// row actually changes.
//
// Skipped intentionally: the scheduleUpdater callback branches (Docker /
// scheduler-side effects, h.scheduleUpdater is nil in these tests) and
// the internal-server-error branches that require a closed/broken DB —
// those are exercised elsewhere and don't reflect Update's validation
// logic.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// covAUHandler builds an AgentHandler over a fresh test DB and returns
// it alongside a seeded owner user + workspace.
func covAUHandler(t *testing.T) (*AgentHandler, string, string) {
	t.Helper()
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	return h, userID, wsID
}

// covAUPatch issues a PATCH against Update with the given body string and
// caller role, returning the recorder.
func covAUPatch(t *testing.T, h *AgentHandler, userID, wsID, role, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID, nil)
	} else {
		r = httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID, strings.NewReader(body))
	}
	r.SetPathValue("agentId", agentID)
	r = withWorkspaceUser(r, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Update(rr, r)
	return rr
}

// ---- auth / role gate (canEditAgent) ----

func TestCovAUForbiddenViewer(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-1", wsID, "", "Ann", "ann", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "VIEWER", "ag-1", `{"name":"New"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("VIEWER update = %d, want 403", rr.Code)
	}
}

func TestCovAUForbiddenManagerNotOwner(t *testing.T) {
	// MANAGER passes the create-tier check but seedAgentRow leaves
	// created_by_user_id NULL, so the ownership branch refuses.
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-mgr", wsID, "", "Mgr", "mgr", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "MANAGER", "ag-mgr", `{"name":"New"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("MANAGER (non-owner) update = %d, want 403", rr.Code)
	}
}

// ---- not found ----

func TestCovAUNotFoundUnknownID(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "missing", `{"name":"New"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown agent update = %d, want 404", rr.Code)
	}
}

func TestCovAUNotFoundCrossWorkspace(t *testing.T) {
	// Agent exists but in a different workspace — agentExists is
	// workspace-scoped, so Update must 404 (the edit gate is not
	// workspace-scoped, hence the 404 comes from agentExists).
	h, userID, wsA := covAUHandler(t)
	wsB := "ws-foreign-au"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-au')`, wsB); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedAgentRow(t, h.db, "ag-foreign", wsB, "", "Foreign", "foreign", "AGENT")

	rr := covAUPatch(t, h, userID, wsA, "OWNER", "ag-foreign", `{"name":"New"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace update = %d, want 404", rr.Code)
	}
}

// ---- invalid JSON ----

func TestCovAUInvalidJSON(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-json", wsID, "", "Json", "json", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-json", `{not valid`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON update = %d, want 400", rr.Code)
	}
}

// ---- slug validation ----

func TestCovAUSlugTooShort(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-slug1", wsID, "", "S", "sone", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-slug1", `{"slug":"a"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("short slug = %d, want 400", rr.Code)
	}
}

func TestCovAUSlugBadFormat(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-slug2", wsID, "", "S", "stwo", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-slug2", `{"slug":"Has Spaces"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad slug format = %d, want 400", rr.Code)
	}
}

// ---- agent_role validation + role transitions ----

func TestCovAUInvalidAgentRole(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-role1", wsID, "", "R", "rone", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-role1", `{"agent_role":"WIZARD"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid agent_role = %d, want 400", rr.Code)
	}
}

func TestCovAULeadRequiresCrew(t *testing.T) {
	// Promoting an agent with no crew_id to LEAD must be rejected.
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-lead-nocrew", wsID, "", "L", "lnocrew", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-lead-nocrew", `{"agent_role":"LEAD"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("LEAD without crew = %d, want 400", rr.Code)
	}
}

func TestCovAUPromoteToLeadAutoDemotes(t *testing.T) {
	// Promoting an agent to LEAD inside a crew that already has a LEAD
	// must atomically demote the existing lead to AGENT.
	h, userID, wsID := covAUHandler(t)
	crewID := seedCrewRow(t, h.db, "crew-au", wsID, "Crew", "crew-au")
	seedAgentRow(t, h.db, "ag-oldlead", wsID, crewID, "Old", "oldlead", "LEAD")
	seedAgentRow(t, h.db, "ag-newlead", wsID, crewID, "New", "newlead", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-newlead", `{"agent_role":"LEAD"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("promote to LEAD = %d, want 200", rr.Code)
	}

	var oldRole, newRole string
	if err := h.db.QueryRow(`SELECT agent_role FROM agents WHERE id = 'ag-oldlead'`).Scan(&oldRole); err != nil {
		t.Fatalf("readback old lead: %v", err)
	}
	if err := h.db.QueryRow(`SELECT agent_role FROM agents WHERE id = 'ag-newlead'`).Scan(&newRole); err != nil {
		t.Fatalf("readback new lead: %v", err)
	}
	if oldRole != "AGENT" {
		t.Errorf("existing lead not demoted: got %q, want AGENT", oldRole)
	}
	if newRole != "LEAD" {
		t.Errorf("new lead not promoted: got %q, want LEAD", newRole)
	}
}

// ---- lead_mode validation ----

func TestCovAUInvalidLeadMode(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-lm", wsID, "", "LM", "lmode", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-lm", `{"lead_mode":"turbo"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid lead_mode = %d, want 400", rr.Code)
	}
}

func TestCovAULeadModeNonString(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-lm2", wsID, "", "LM", "lmode2", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-lm2", `{"lead_mode":42}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("non-string lead_mode = %d, want 400", rr.Code)
	}
}

// ---- enum-column validation: cli_adapter / llm_provider / tool_profile ----

func TestCovAUCLIAdapterEmpty(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-cli1", wsID, "", "C", "cli1", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-cli1", `{"cli_adapter":""}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty cli_adapter = %d, want 400", rr.Code)
	}
}

func TestCovAUCLIAdapterUnknown(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-cli2", wsID, "", "C", "cli2", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-cli2", `{"cli_adapter":"BORG_CLI"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown cli_adapter = %d, want 400", rr.Code)
	}
}

func TestCovAULLMProviderEmpty(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-llm1", wsID, "", "P", "llm1", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-llm1", `{"llm_provider":""}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty llm_provider = %d, want 400", rr.Code)
	}
}

func TestCovAULLMProviderUnknown(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-llm2", wsID, "", "P", "llm2", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-llm2", `{"llm_provider":"SKYNET"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown llm_provider = %d, want 400", rr.Code)
	}
}

func TestCovAUToolProfileEmpty(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-tp1", wsID, "", "T", "tp1", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-tp1", `{"tool_profile":""}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty tool_profile = %d, want 400", rr.Code)
	}
}

func TestCovAUToolProfileUnknown(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-tp2", wsID, "", "T", "tp2", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-tp2", `{"tool_profile":"GODMODE"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown tool_profile = %d, want 400", rr.Code)
	}
}

// ---- mcp_config_json validation ----

func TestCovAUMCPConfigInvalidJSON(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-mcp1", wsID, "", "M", "mcp1", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-mcp1", `{"mcp_config_json":"{not json"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid mcp_config_json = %d, want 400", rr.Code)
	}
}

func TestCovAUMCPConfigMissingServers(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-mcp2", wsID, "", "M", "mcp2", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-mcp2", `{"mcp_config_json":"{\"other\":1}"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mcp_config_json without mcpServers = %d, want 400", rr.Code)
	}
}

func TestCovAUMCPConfigValid(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-mcp3", wsID, "", "M", "mcp3", "AGENT")

	body := `{"mcp_config_json":"{\"mcpServers\":{}}"}`
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-mcp3", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid mcp_config_json = %d, want 200", rr.Code)
	}
}

// ---- no fields to update ----

func TestCovAUNoFields(t *testing.T) {
	// A body with no recognised keys produces an empty update builder.
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-empty", wsID, "", "E", "empty", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-empty", `{"unknown_field":"x"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("no recognised fields = %d, want 400", rr.Code)
	}
}

// ---- happy path: row actually changes ----

func TestCovAUHappyUpdate(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-happy", wsID, "", "Before", "before", "AGENT")

	body := `{"name":"After","description":"updated desc","memory_enabled":true}`
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-happy", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("happy update = %d, want 200", rr.Code)
	}

	var name, desc string
	var mem int
	if err := h.db.QueryRow(
		`SELECT name, description, memory_enabled FROM agents WHERE id = 'ag-happy'`).
		Scan(&name, &desc, &mem); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "After" {
		t.Errorf("name = %q, want After", name)
	}
	if desc != "updated desc" {
		t.Errorf("description = %q, want 'updated desc'", desc)
	}
	if mem != 1 {
		t.Errorf("memory_enabled = %d, want 1 (bool true coerced to int)", mem)
	}
}
