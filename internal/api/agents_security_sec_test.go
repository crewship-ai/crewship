package api

// Security regression tests for the agent create/update handlers.
//
// TestSecAgentCrewIDForeignWorkspace pins FIX 1 (IDOR): a PATCH must not
// be able to reassign an agent into a crew that lives in another
// workspace — crew_id has to be validated against the caller's workspace
// just like every other relational field.
//
// TestSecAgentDuplicateLead pins FIX 2 (TOCTOU duplicate LEAD): a second
// LEAD in the same crew must be rejected with 409, including under
// concurrent creates (the partial unique index from migration v110 is the
// backstop the check-then-act SELECT can race past).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// secAgentCreate posts a create body as the given role and returns the
// recorder.
func secAgentCreate(t *testing.T, h *AgentHandler, userID, wsID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader(body))
	r = withWorkspaceUser(r, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Create(rr, r)
	return rr
}

// TestSecAgentCrewIDForeignWorkspace — FIX 1. Seed a crew in workspace B,
// an agent in workspace A, then PATCH the agent's crew_id to the B crew.
// The handler must reject it with 400 and leave the agent's crew_id
// unchanged. (RED before the crewExists validation lands.)
func TestSecAgentCrewIDForeignWorkspace(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	// Workspace B with its own crew — the cross-tenant target.
	wsB := "ws-foreign-sec"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-sec')`, wsB); err != nil {
		t.Fatalf("seed ws B: %v", err)
	}
	foreignCrew := seedCrewRow(t, h.db, "crew-b", wsB, "B Crew", "b-crew")

	// Agent in workspace A, crewless to start.
	agentID := seedAgentRow(t, h.db, "ag-victim", wsA, "", "Victim", "victim", "AGENT")

	r := httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID, strings.NewReader(`{"crew_id":"`+foreignCrew+`"}`))
	r.SetPathValue("agentId", agentID)
	r = withWorkspaceUser(r, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, r)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-workspace crew_id PATCH = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}

	// The agent's crew_id must NOT have changed.
	var crewID interface{}
	if err := h.db.QueryRow(`SELECT crew_id FROM agents WHERE id = ?`, agentID).Scan(&crewID); err != nil {
		t.Fatalf("read agent crew_id: %v", err)
	}
	if crewID != nil {
		t.Fatalf("agent crew_id was modified to %v, want NULL (unchanged)", crewID)
	}
}

// TestSecAgentCrewIDSameWorkspace — guard rail for FIX 1: a crew_id that
// DOES belong to the caller's workspace must still be accepted.
func TestSecAgentCrewIDSameWorkspace(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ok", wsID, "OK Crew", "ok-crew")
	agentID := seedAgentRow(t, h.db, "ag-move", wsID, "", "Mover", "mover", "AGENT")

	r := httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID, strings.NewReader(`{"crew_id":"`+crewID+`"}`))
	r.SetPathValue("agentId", agentID)
	r = withWorkspaceUser(r, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("same-workspace crew_id PATCH = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got interface{}
	if err := h.db.QueryRow(`SELECT crew_id FROM agents WHERE id = ?`, agentID).Scan(&got); err != nil {
		t.Fatalf("read agent crew_id: %v", err)
	}
	if got != crewID {
		t.Fatalf("agent crew_id = %v, want %s", got, crewID)
	}
}

// TestSecAgentDuplicateLead — FIX 2. First LEAD in a crew succeeds; a
// second LEAD in the same crew must be rejected with 409.
func TestSecAgentDuplicateLead(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-lead", wsID, "Lead Crew", "lead-crew")

	first := secAgentCreate(t, h, userID, wsID, "OWNER",
		`{"name":"Lead One","slug":"lead-one","agent_role":"LEAD","crew_id":"`+crewID+`"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first LEAD = %d, want 201; body: %s", first.Code, first.Body.String())
	}

	second := secAgentCreate(t, h, userID, wsID, "OWNER",
		`{"name":"Lead Two","slug":"lead-two","agent_role":"LEAD","crew_id":"`+crewID+`"}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("second LEAD = %d, want 409; body: %s", second.Code, second.Body.String())
	}

	// Exactly one live LEAD must exist in the crew.
	var leads int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL`,
		crewID).Scan(&leads); err != nil {
		t.Fatalf("count leads: %v", err)
	}
	if leads != 1 {
		t.Fatalf("live LEAD count = %d, want 1", leads)
	}
}

// TestSecAgentDuplicateLeadConcurrent — FIX 2 under contention. Fire N
// concurrent LEAD creates at the same empty crew; the DB-level partial
// unique index must guarantee exactly one winner regardless of how the
// check-then-act SELECT interleaves.
func TestSecAgentDuplicateLeadConcurrent(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-race", wsID, "Race Crew", "race-crew")

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slug := "race-lead-" + string(rune('a'+i))
			body := `{"name":"Race Lead","slug":"` + slug + `","agent_role":"LEAD","crew_id":"` + crewID + `"}`
			<-start
			rr := secAgentCreate(t, h, userID, wsID, "OWNER", body)
			codes[i] = rr.Code
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
			continue
		}
		// Every loser must be a clean 409 (constraint translated), never
		// a raw 500 leaking the UNIQUE violation.
		if c != http.StatusConflict {
			t.Fatalf("concurrent LEAD loser returned %d, want 409 (codes: %v)", c, codes)
		}
	}
	if created != 1 {
		t.Fatalf("concurrent LEAD creates produced %d successes, want exactly 1 (codes: %v)", created, codes)
	}

	var leads int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL`,
		crewID).Scan(&leads); err != nil {
		t.Fatalf("count leads: %v", err)
	}
	if leads != 1 {
		t.Fatalf("live LEAD count = %d, want 1", leads)
	}
}
