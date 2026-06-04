package api

// DB-error (500) branch coverage for the crew / agent / credential /
// workspace handler surface. Each case builds a request valid enough to
// clear the 400/403 guards, then closes the DB immediately before
// invoking the handler so the FIRST query the handler issues fails with
// "sql: database is closed" — exercising the otherwise-unreachable
// Internal-Server-Error branch.
//
// All helpers are prefixed covDBC; all test funcs TestCovDBC.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// covDBCInvoke closes db, then runs fn against a recorder built from req
// (with userID/wsID/OWNER context) and asserts the handler returns 500.
func covDBCInvoke(t *testing.T, name string, closeDB func(), req *http.Request, fn func(http.ResponseWriter, *http.Request)) {
	t.Helper()
	closeDB()
	rr := httptest.NewRecorder()
	fn(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d, want 500, body: %s", name, rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// crews_query.go / crews_create.go / crews_update.go / crews.go
// ---------------------------------------------------------------------------

func TestCovDBC_CrewHandler(t *testing.T) {
	type tc struct {
		name   string
		method string
		path   string
		body   any
		pv     map[string]string
		call   func(*CrewHandler) func(http.ResponseWriter, *http.Request)
	}
	cases := []tc{
		{"List", "GET", "/api/v1/crews", nil, nil,
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.List }},
		{"Get", "GET", "/api/v1/crews/crew-1", nil, map[string]string{"crewId": "crew-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.Get }},
		{"Create", "POST", "/api/v1/crews",
			map[string]any{"name": "New Crew", "slug": "new-crew"}, nil,
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.Create }},
		{"Update", "PATCH", "/api/v1/crews/crew-1",
			map[string]any{"name": "Renamed"}, map[string]string{"crewId": "crew-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.Update }},
		{"Delete", "DELETE", "/api/v1/crews/crew-1", nil, map[string]string{"crewId": "crew-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.Delete }},
		{"ApplyAvatarStyle", "POST", "/api/v1/crews/crew-1/avatar-style",
			map[string]any{"avatar_style": "pixel-art"}, map[string]string{"crewId": "crew-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.ApplyAvatarStyle }},
		{"ListMembers", "GET", "/api/v1/crews/crew-1/members", nil, map[string]string{"crewId": "crew-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.ListMembers }},
		{"AddMember", "POST", "/api/v1/crews/crew-1/members",
			map[string]any{"user_id": "test-user-id"}, map[string]string{"crewId": "crew-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.AddMember }},
		{"RemoveMember", "DELETE", "/api/v1/crews/crew-1/members/mem-1", nil,
			map[string]string{"crewId": "crew-1", "memberId": "mem-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.RemoveMember }},
		{"UpdateMemberRole", "PATCH", "/api/v1/crews/crew-1/members/mem-1",
			map[string]any{"role": "ADMIN"}, map[string]string{"crewId": "crew-1", "memberId": "mem-1"},
			func(h *CrewHandler) func(http.ResponseWriter, *http.Request) { return h.UpdateMemberRole }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := setupTestDB(t)
			logger := newTestLogger()
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			seedCrewRow(t, db, "crew-1", wsID, "Crew One", "crew-one")

			h := NewCrewHandler(db, logger)
			var body any = c.body
			req := httptest.NewRequest(c.method, c.path, jsonBody(body))
			for k, v := range c.pv {
				req.SetPathValue(k, v)
			}
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			covDBCInvoke(t, c.name, func() { db.Close() }, req, c.call(h))
		})
	}
}

// ---------------------------------------------------------------------------
// agents_query.go / agents_create.go / agents_update.go / agent_skills.go
// ---------------------------------------------------------------------------

func TestCovDBC_AgentHandler(t *testing.T) {
	type tc struct {
		name   string
		method string
		path   string
		body   any
		pv     map[string]string
		call   func(*AgentHandler) func(http.ResponseWriter, *http.Request)
	}
	cases := []tc{
		{"List", "GET", "/api/v1/agents", nil, nil,
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.List }},
		{"Get", "GET", "/api/v1/agents/agent-1", nil, map[string]string{"agentId": "agent-1"},
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.Get }},
		{"Load", "GET", "/api/v1/agent-load", nil, nil,
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.Load }},
		{"Create", "POST", "/api/v1/agents",
			map[string]any{"name": "New Agent", "slug": "new-agent"}, nil,
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.Create }},
		{"Update", "PATCH", "/api/v1/agents/agent-1",
			map[string]any{"name": "Renamed Agent"}, map[string]string{"agentId": "agent-1"},
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.Update }},
		{"Delete", "DELETE", "/api/v1/agents/agent-1", nil, map[string]string{"agentId": "agent-1"},
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.Delete }},
		{"ListSkills", "GET", "/api/v1/agents/agent-1/skills", nil, map[string]string{"agentId": "agent-1"},
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.ListSkills }},
		{"AddSkill", "POST", "/api/v1/agents/agent-1/skills",
			map[string]any{"skill_id": "skill-1"}, map[string]string{"agentId": "agent-1"},
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.AddSkill }},
		{"RemoveSkill", "DELETE", "/api/v1/agents/agent-1/skills/skill-1", nil,
			map[string]string{"agentId": "agent-1", "skillId": "skill-1"},
			func(h *AgentHandler) func(http.ResponseWriter, *http.Request) { return h.RemoveSkill }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := setupTestDB(t)
			logger := newTestLogger()
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			seedCrewRow(t, db, "crew-1", wsID, "Crew One", "crew-one")
			seedAgentRow(t, db, "agent-1", wsID, "crew-1", "Agent One", "agent-one", "AGENT")

			h := NewAgentHandler(db, logger)
			req := httptest.NewRequest(c.method, c.path, jsonBody(c.body))
			for k, v := range c.pv {
				req.SetPathValue(k, v)
			}
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			covDBCInvoke(t, c.name, func() { db.Close() }, req, c.call(h))
		})
	}
}

// ---------------------------------------------------------------------------
// credentials.go / credentials_mutate.go / credentials_test_endpoint.go
// ---------------------------------------------------------------------------

func TestCovDBC_CredentialHandler(t *testing.T) {
	type tc struct {
		name   string
		method string
		path   string
		body   any
		pv     map[string]string
		call   func(*CredentialHandler) func(http.ResponseWriter, *http.Request)
	}
	cases := []tc{
		{"List", "GET", "/api/v1/credentials", nil, nil,
			func(h *CredentialHandler) func(http.ResponseWriter, *http.Request) { return h.List }},
		{"Get", "GET", "/api/v1/credentials/cred-1", nil, map[string]string{"credentialId": "cred-1"},
			func(h *CredentialHandler) func(http.ResponseWriter, *http.Request) { return h.Get }},
		// Create: crew_ids forces crewExists() to be the first query, so
		// it trips on the closed DB before reaching the encryption step.
		{"Create", "POST", "/api/v1/credentials",
			map[string]any{"name": "New Cred", "value": "secret-val", "type": "SECRET", "crew_ids": []string{"crew-1"}},
			nil,
			func(h *CredentialHandler) func(http.ResponseWriter, *http.Request) { return h.Create }},
		{"Update", "PATCH", "/api/v1/credentials/cred-1",
			map[string]any{"description": "patched"}, map[string]string{"credentialId": "cred-1"},
			func(h *CredentialHandler) func(http.ResponseWriter, *http.Request) { return h.Update }},
		{"TestStored", "POST", "/api/v1/credentials/cred-1/test", nil, map[string]string{"credentialId": "cred-1"},
			func(h *CredentialHandler) func(http.ResponseWriter, *http.Request) { return h.TestStored }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := setupTestDB(t)
			logger := newTestLogger()
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			seedCrew(t, db, "crew-1", wsID, "Crew One", "crew-one")
			seedCredential(t, db, "cred-1", wsID, "Cred One")

			h := NewCredentialHandler(db, logger)
			req := httptest.NewRequest(c.method, c.path, jsonBody(c.body))
			for k, v := range c.pv {
				req.SetPathValue(k, v)
			}
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			covDBCInvoke(t, c.name, func() { db.Close() }, req, c.call(h))
		})
	}
}

// ---------------------------------------------------------------------------
// workspaces.go / workspaces_mutate.go
// ---------------------------------------------------------------------------

func TestCovDBC_WorkspaceHandler(t *testing.T) {
	type tc struct {
		name   string
		method string
		path   string
		body   any
		pv     map[string]string
		call   func(*WorkspaceHandler) func(http.ResponseWriter, *http.Request)
	}
	cases := []tc{
		{"List", "GET", "/api/v1/workspaces", nil, nil,
			func(h *WorkspaceHandler) func(http.ResponseWriter, *http.Request) { return h.List }},
		{"Get", "GET", "/api/v1/workspaces/test-workspace-id", nil, nil,
			func(h *WorkspaceHandler) func(http.ResponseWriter, *http.Request) { return h.Get }},
		{"Create", "POST", "/api/v1/workspaces",
			map[string]any{"name": "New WS", "slug": "new-ws"}, nil,
			func(h *WorkspaceHandler) func(http.ResponseWriter, *http.Request) { return h.Create }},
		{"Update", "PATCH", "/api/v1/workspaces/test-workspace-id",
			map[string]any{"name": "Renamed WS"}, nil,
			func(h *WorkspaceHandler) func(http.ResponseWriter, *http.Request) { return h.Update }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := setupTestDB(t)
			logger := newTestLogger()
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			h := NewWorkspaceHandler(db, logger)
			req := httptest.NewRequest(c.method, c.path, jsonBody(c.body))
			for k, v := range c.pv {
				req.SetPathValue(k, v)
			}
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			covDBCInvoke(t, c.name, func() { db.Close() }, req, c.call(h))
		})
	}
}
