package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCrewCapabilities_AggregatesEverything(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Crew with a devcontainer feature (→ terraform tool) so container caps aren't empty.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?, ?)`,
		"crew-cap", wsID, `{"features":{"ghcr.io/devcontainers/features/terraform:1":{}}}`, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	covCISeedAgent(t, db, "ag-parse", wsID, "crew-cap") // slug ag-ag-parse per helper

	// One enabled integration with one enabled + one disabled tool binding.
	srv := covIntCrewServer(t, db, "srv-gmail", "crew-cap", "gmail", "streamable-http", "https://mcp.example/gmail")
	if _, err := db.Exec(`INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled, created_at, updated_at)
		VALUES ('b1', ?, 'crew', 'GMAIL_FETCH_EMAIL', 1, ?, ?), ('b2', ?, 'crew', 'GMAIL_SEND', 0, ?, ?)`,
		srv, now, now, srv, now, now); err != nil {
		t.Fatalf("seed tool bindings: %v", err)
	}

	h := NewCrewHandler(db, newTestLogger())
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/crews/crew-cap/capabilities", nil), wsID)
	req.SetPathValue("crewId", "crew-cap")
	w := httptest.NewRecorder()
	h.Capabilities(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out crewCapabilitiesResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.CrewSlug != "acct" || out.CrewID != "crew-cap" {
		t.Errorf("crew identity wrong: %+v", out)
	}
	// Container tools resolved from the devcontainer feature.
	if !containsToolCap(out.Container.Tools, "terraform") {
		t.Errorf("container tools missing terraform: %+v", out.Container.Tools)
	}
	// Integration with only the ENABLED tool name (disabled GMAIL_SEND excluded).
	if len(out.Integrations) != 1 {
		t.Fatalf("expected 1 integration, got %d", len(out.Integrations))
	}
	if got := out.Integrations[0].Tools; len(got) != 1 || got[0] != "GMAIL_FETCH_EMAIL" {
		t.Errorf("integration tools = %v, want [GMAIL_FETCH_EMAIL] (disabled excluded)", got)
	}
	// Agents surfaced.
	if len(out.Agents) != 1 || out.Agents[0].Slug != "ag-ag-parse" {
		t.Errorf("agents = %+v", out.Agents)
	}
	// Runtimes are the wired truth.
	if len(out.Runtimes.Code.Wired) != 2 || out.Runtimes.Code.Wired[0] != "cel" {
		t.Errorf("wired runtimes = %v", out.Runtimes.Code.Wired)
	}
	if out.Runtimes.ScriptInterpreters[".py"] != "python3" {
		t.Errorf("script interpreters missing .py: %v", out.Runtimes.ScriptInterpreters)
	}
	// Schema is a real nested JSON object, not empty / not a string.
	var schema map[string]any
	if err := json.Unmarshal(out.Schema, &schema); err != nil || len(schema) == 0 {
		t.Errorf("schema not a JSON object: err=%v", err)
	}
}

func TestCrewCapabilities_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCrewHandler(db, newTestLogger())
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "ghost")
	w := httptest.NewRecorder()
	h.Capabilities(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown crew, got %d", w.Code)
	}
}

func containsToolCap(tools []ToolCap, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
