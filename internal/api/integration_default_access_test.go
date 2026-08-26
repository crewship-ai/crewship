package api

// #2072 — an integration's audience is a stored fact, not a row count.
//
// Before this, "available to every agent" was inferred from
// `SELECT ... FROM agent_mcp_bindings ... HAVING COUNT(*) > 0` being empty:
// bind ONE agent anywhere in the workspace and every other agent silently
// lost the server. The table below is the contract that replaces it —
// `default_access` decides, and a binding for somebody else never appears in
// the answer for this agent.
//
// Both resolvers are driven from the SAME table on purpose. There are two
// implementations of the cascade — ResolveAgentIntegrations serves the UI and
// `crewship integration resolve`, resolveAgentMCPServers is what the running
// container actually gets — and a fix applied to only one of them is a fix
// that shows the operator an access list the agent does not have.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type defaultAccessCase struct {
	name string
	// access is the value stored in workspace_mcp_servers.default_access.
	access string
	// bindOther binds a DIFFERENT agent in the same workspace to the server.
	bindOther bool
	// bindSelf binds the agent under test.
	bindSelf bool
	// wantVisible is whether the agent under test should resolve the server.
	wantVisible bool
	why         string
}

var defaultAccessCases = []defaultAccessCase{
	{
		name: "open_no_bindings", access: "all",
		wantVisible: true,
		why:         "a server open to everyone and bound to nobody is the base case",
	},
	{
		name: "open_bound_to_someone_else", access: "all",
		bindOther: true, wantVisible: true,
		why: "#2072: another agent's binding must not revoke this agent's access",
	},
	{
		name: "open_bound_to_self", access: "all",
		bindSelf: true, wantVisible: true,
		why: "an explicit binding is an addition (credential, config), never a fence around the server",
	},
	{
		name: "restricted_unbound", access: "bound-only",
		wantVisible: false,
		why:         "bound-only with no binding for this agent is the deliberate opt-in state",
	},
	{
		name: "restricted_bound_to_someone_else", access: "bound-only",
		bindOther: true, wantVisible: false,
		why: "somebody else's binding is not this agent's binding",
	},
	{
		name: "restricted_bound_to_self", access: "bound-only",
		bindSelf: true, wantVisible: true,
		why: "the binding is what opts this agent in",
	},
}

// daFixture seeds a workspace, a crew, the agent under test, a second agent,
// and one workspace MCP server named "github" with the given default_access
// plus whichever bindings the case asks for.
func daFixture(t *testing.T, c defaultAccessCase) (db *sql.DB, userID, wsID, crewID, agentID string) {
	t.Helper()
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "da-crew", wsID, "Crew", "da-crew")
	agentID = seedAgentRow(t, db, "da-agent", wsID, crewID, "Agent", "da-agent", "AGENT")
	otherID := seedAgentRow(t, db, "da-other", wsID, crewID, "Other", "da-other", "AGENT")

	execOrFatal(t, db, `INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, enabled, default_access)
		VALUES ('da-srv', ?, 'github', 'GitHub', 'streamable-http', 'https://mcp.example.com/gh', 1, ?)`,
		wsID, c.access)

	if c.bindOther {
		execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
			(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
			VALUES ('da-b-other', ?, 'da-srv', 'workspace', 1)`, otherID)
	}
	if c.bindSelf {
		execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
			(id, agent_id, mcp_server_id, mcp_server_scope, enabled)
			VALUES ('da-b-self', ?, 'da-srv', 'workspace', 1)`, agentID)
	}
	return db, userID, wsID, crewID, agentID
}

// TestResolveAgentIntegrations_DefaultAccess drives the HTTP resolver —
// GET /api/v1/agents/{agentId}/integrations/resolved.
func TestResolveAgentIntegrations_DefaultAccess(t *testing.T) {
	for _, c := range defaultAccessCases {
		t.Run(c.name, func(t *testing.T) {
			db, userID, wsID, _, agentID := daFixture(t, c)
			h := NewIntegrationHandler(db, newTestLogger())

			req := withWorkspaceUser(
				httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/integrations/resolved", nil),
				userID, wsID, "OWNER")
			req.SetPathValue("agentId", agentID)
			rr := httptest.NewRecorder()
			h.ResolveAgentIntegrations(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			var got []ResolvedIntegration
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
			}
			visible := false
			for _, s := range got {
				if s.Name == "github" {
					visible = true
				}
			}
			if visible != c.wantVisible {
				t.Errorf("github visible = %v, want %v — %s", visible, c.wantVisible, c.why)
			}
		})
	}
}

// TestResolveAgentMCPServers_DefaultAccess drives the runtime resolver, the
// one whose answer is written into the container's MCP config.
func TestResolveAgentMCPServers_DefaultAccess(t *testing.T) {
	for _, c := range defaultAccessCases {
		t.Run(c.name, func(t *testing.T) {
			setTestEncryptionKey(t)
			db, _, _, _, agentID := daFixture(t, c)
			h := covCfgHandler(db)
			req := httptest.NewRequest("GET", "/", nil)
			d, err := h.loadAgentData(req, agentID)
			if err != nil {
				t.Fatalf("loadAgentData: %v", err)
			}

			visible := false
			for _, s := range h.resolveAgentMCPServers(req, d, agentID) {
				if s.Name == "github" {
					visible = true
				}
			}
			if visible != c.wantVisible {
				t.Errorf("github visible = %v, want %v — %s", visible, c.wantVisible, c.why)
			}
		})
	}
}

// TestBindingDoesNotChangeAnotherAgentsAccess is the issue's own scenario, end
// to end through the API that caused it: two agents, one open workspace
// server, then a POST /api/v1/agents/{other}/integrations. Before #2072 the
// second resolve came back empty and nothing told anyone.
func TestBindingDoesNotChangeAnotherAgentsAccess(t *testing.T) {
	db, userID, wsID, crewID, agentID := daFixture(t, defaultAccessCase{access: "all"})
	h := NewIntegrationHandler(db, newTestLogger())
	otherID := seedAgentRow(t, db, "da-newcomer", wsID, crewID, "New", "da-newcomer", "AGENT")

	resolves := func() int {
		t.Helper()
		req := withWorkspaceUser(
			httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/integrations/resolved", nil),
			userID, wsID, "OWNER")
		req.SetPathValue("agentId", agentID)
		rr := httptest.NewRecorder()
		h.ResolveAgentIntegrations(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("resolve status = %d: %s", rr.Code, rr.Body.String())
		}
		var got []ResolvedIntegration
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return len(got)
	}

	if before := resolves(); before != 1 {
		t.Fatalf("before binding: %d integrations resolved, want 1", before)
	}

	bindReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/agents/"+otherID+"/integrations",
			jsonBody(map[string]any{"mcp_server_id": "da-srv", "mcp_server_scope": "workspace"})),
		userID, wsID, "OWNER")
	bindReq.SetPathValue("agentId", otherID)
	bindRR := httptest.NewRecorder()
	h.CreateAgentBinding(bindRR, bindReq)
	if bindRR.Code != http.StatusCreated {
		t.Fatalf("bind status = %d, want 201; body=%s", bindRR.Code, bindRR.Body.String())
	}

	if after := resolves(); after != 1 {
		t.Errorf("after binding another agent: %d integrations resolved, want 1 — "+
			"one agent's grant must never be another agent's revocation (#2072)", after)
	}
}
