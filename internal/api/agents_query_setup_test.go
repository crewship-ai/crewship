package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The onboarding Guide lives in a crew of kind='setup'. `GET /crews` has
// always hidden that crew; `GET /agents` did not hide its agent, so every
// roster in the product — the chat column, the Agents facet, the mention
// picker, routine reach — offered a "Crewship Guide" nobody hired, and /chat
// opened on it because its seeded conversation was the freshest.
//
// Exclusion is the API's default. `include_setup=1` is the explicit opt-in
// for the one caller that wants it (a deep link into the Guide's own chat).

func setupTListAgents(t *testing.T, h *AgentHandler, wsID, query string) []agentResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/agents"+query, nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list%s status = %d, body=%s", query, rr.Code, rr.Body.String())
	}
	return uxDecode[[]agentResponse](t, rr)
}

func setupTSlugs(agents []agentResponse) map[string]bool {
	out := map[string]bool{}
	for _, a := range agents {
		out[a.Slug] = true
	}
	return out
}

func TestAgentsList_HidesSetupCrewAgentsByDefault(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('setupt-ops', ?, 'Ops', 'setupt-ops')`, wsID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug, kind) VALUES ('setupt-guide', ?, 'Setup', '_crewship-setup', 'setup')`, wsID)
	seedAgentRow(t, db, "setupt-riley", wsID, "setupt-ops", "Riley", "riley", "AGENT")
	seedAgentRow(t, db, "setupt-guide-ag", wsID, "setupt-guide", "Crewship Guide", "_crewship-setup-guide", "AGENT")
	// An agent with no crew at all is not a setup agent and must survive the
	// filter — the join is LEFT for a reason.
	seedAgentRow(t, db, "setupt-loner", wsID, "", "Loner", "loner", "AGENT")

	h := NewAgentHandler(db, newTestLogger())

	got := setupTSlugs(setupTListAgents(t, h, wsID, ""))
	if !got["riley"] || !got["loner"] {
		t.Errorf("default list = %v, want riley and loner", got)
	}
	if got["_crewship-setup-guide"] {
		t.Errorf("default list = %v, must not carry the setup crew's agent", got)
	}

	got = setupTSlugs(setupTListAgents(t, h, wsID, "?include_setup=1"))
	if !got["_crewship-setup-guide"] || !got["riley"] || !got["loner"] {
		t.Errorf("include_setup=1 list = %v, want all three", got)
	}

	// The crew filter path composes with the exclusion: asking for the setup
	// crew by id without the opt-in answers nothing rather than leaking it.
	got = setupTSlugs(setupTListAgents(t, h, wsID, "?crew_id=setupt-guide"))
	if len(got) != 0 {
		t.Errorf("crew_id=setup crew without opt-in = %v, want empty", got)
	}
	got = setupTSlugs(setupTListAgents(t, h, wsID, "?crew_id=setupt-guide&include_setup=1"))
	if !got["_crewship-setup-guide"] || len(got) != 1 {
		t.Errorf("crew_id=setup crew with opt-in = %v, want just the guide", got)
	}
}
