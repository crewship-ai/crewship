package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedComposioServer inserts a workspace_mcp_servers row shaped like the
// Composio bind flow's output (icon='composio', "Composio: <toolkit> · Full"
// display) and binds the agent to it. Returns the server id.
func seedComposioServer(t *testing.T, db *sql.DB, wsID, agentID, toolkit string) {
	t.Helper()
	serverID := "wsmcp_" + agentID + "_" + toolkit
	name := "composio-" + agentID + "-" + toolkit
	display := "Composio: " + toolkit + " · Full"
	if _, err := db.Exec(`INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, icon, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'streamable-http', 'https://mcp.example/x', 'composio', 1, datetime('now'), datetime('now'))`,
		serverID, wsID, name, display); err != nil {
		t.Fatalf("seed composio server: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, cred_type, cred_header, enabled, created_at)
		VALUES (?, ?, ?, 'workspace', 'api_key', 'x-api-key', 1, datetime('now'))`,
		"bind_"+serverID, agentID, serverID); err != nil {
		t.Fatalf("seed composio binding: %v", err)
	}
}

// seedPipelineWithAuthorCrew inserts a pipelines row with an author crew so
// the run gate has a crew to resolve integration availability against.
func seedPipelineWithAuthorCrew(t *testing.T, db *sql.DB, wsID, id, slug, def, crewID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO pipelines
		(id, workspace_id, slug, name, definition_json, definition_hash, author_crew_id, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, ?, 'hash', ?, datetime('now'), datetime('now'), datetime('now'))`,
		id, wsID, slug, slug, def, crewID); err != nil {
		t.Fatalf("seed pipeline %s: %v", slug, err)
	}
}

func TestComposioToolkitFromServer(t *testing.T) {
	cases := []struct {
		display, name, want string
	}{
		{"Composio: slack · Full", "composio-ag1-slack", "slack"},
		{"Composio: GitHub · Read-only", "composio-ag1-github", "github"},
		{"", "composio-ag1-notion", "notion"},
		{"Some other server", "legacy-server", ""},
	}
	for _, c := range cases {
		if got := composioToolkitFromServer(c.display, c.name); got != c.want {
			t.Errorf("composioToolkitFromServer(%q,%q) = %q, want %q", c.display, c.name, got, c.want)
		}
	}
}

func TestResolveCrewIntegrations_ExplicitBindings(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_ig1", wsID, "Marketing", "marketing")
	agentID := seedAgentRow(t, h.db, "ag_ig1", wsID, crewID, "Eva", "eva", "LEAD")
	seedComposioServer(t, h.db, wsID, agentID, "slack")

	set, err := resolveCrewIntegrations(context.Background(), h.db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !set["slack"] {
		t.Fatalf("expected slack in set, got %#v", set)
	}
	if set["github"] {
		t.Fatalf("did not expect github, got %#v", set)
	}
	if set[crewIntegrationsWildcard] {
		t.Fatalf("did not expect wildcard, got %#v", set)
	}
}

func TestResolveCrewIntegrations_DefaultConnectorWildcard(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_ig2", wsID, "Ops", "ops")
	_ = seedAgentRow(t, h.db, "ag_ig2", wsID, crewID, "Max", "max", "LEAD")
	if _, err := h.db.Exec(`INSERT INTO composio_settings (workspace_id, encrypted_api_key, default_user_id, default_mcp_server_id)
		VALUES (?, 'enc', 'user-x', 'srv-x')`, wsID); err != nil {
		t.Fatalf("seed composio settings: %v", err)
	}
	set, err := resolveCrewIntegrations(context.Background(), h.db, wsID, crewID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !set[crewIntegrationsWildcard] {
		t.Fatalf("expected wildcard under default connector, got %#v", set)
	}
}

// runGateBody builds a pipeline definition declaring the given integrations.
func gateDef(integrations ...string) string {
	js, _ := json.Marshal(integrations)
	return `{"dsl_version":"1.0","name":"gate-routine","integrations_required":` + string(js) +
		`,"steps":[{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`
}

func TestRunGate_BlocksWhenIntegrationMissing(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_blk", wsID, "Marketing", "marketing")
	_ = seedAgentRow(t, h.db, "ag_blk", wsID, crewID, "Eva", "eva", "LEAD")
	// Crew connected nothing → declaring "slack" must block.
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_blk", "blk", gateDef("slack"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "blk"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	var prob struct {
		Detail              string   `json:"detail"`
		MissingIntegrations []string `json:"missing_integrations"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(prob.MissingIntegrations) != 1 || prob.MissingIntegrations[0] != "slack" {
		t.Fatalf("missing_integrations = %#v, want [slack]", prob.MissingIntegrations)
	}
	if !strings.Contains(prob.Detail, "slack") || !strings.Contains(prob.Detail, "Marketing") {
		t.Errorf("detail = %q, want mention of slack + crew name", prob.Detail)
	}
	if runner.calls != 0 {
		t.Errorf("runner was invoked %d times; the run must NOT execute when blocked", runner.calls)
	}
}

func TestRunGate_PassesWhenIntegrationConnected(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_ok", wsID, "Marketing", "marketing")
	agentID := seedAgentRow(t, h.db, "ag_ok", wsID, crewID, "Eva", "eva", "LEAD")
	seedComposioServer(t, h.db, wsID, agentID, "slack")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_ok", "okp", gateDef("slack"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "okp"))
	if rr.Code == http.StatusUnprocessableEntity {
		t.Fatalf("run was blocked but the integration is connected; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRunGate_NoOpWhenNoneDeclared(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_noop", wsID, "Marketing", "marketing")
	_ = seedAgentRow(t, h.db, "ag_noop", wsID, crewID, "Eva", "eva", "LEAD")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_noop", "noopp", gateDef(), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "noopp"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRunGate_FailOpenOnResolverError(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_fo", wsID, "Marketing", "marketing")
	_ = seedAgentRow(t, h.db, "ag_fo", wsID, crewID, "Eva", "eva", "LEAD")
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_fo", "fop", gateDef("slack"), crewID)
	// Force the resolver query to error without breaking the executor's
	// agent_run path (stubRunner bypasses MCP resolution): drop the table
	// the gate's resolver joins against.
	if _, err := h.db.Exec(`DROP TABLE workspace_mcp_servers`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "fop"))
	if rr.Code == http.StatusUnprocessableEntity {
		t.Fatalf("fail-open expected: a resolver error must allow the run, got 422; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open); body=%s", rr.Code, rr.Body.String())
	}
}

func TestTestRunGate_BlocksWhenIntegrationMissing(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_tr", wsID, "Marketing", "marketing")
	_ = seedAgentRow(t, h.db, "ag_tr", wsID, crewID, "Eva", "eva", "LEAD")

	body := `{"definition":` + gateDef("slack") + `,"author_crew_id":"` + crewID + `","sample_inputs":{}}`
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, "OWNER")
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	h.TestRun(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing_integrations") {
		t.Errorf("body missing the machine-readable missing_integrations: %s", rr.Body.String())
	}
	if runner.calls != 0 {
		t.Errorf("runner invoked %d times; test_run must not execute when blocked", runner.calls)
	}
}
