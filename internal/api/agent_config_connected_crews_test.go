package api

import (
	"database/sql"
	"net/http/httptest"
	"testing"
)

// The agent-config payload is where a LEAD learns what it can orchestrate.
// It listed the lead's own crew and nothing else, so a linked crew was
// invisible to the model — the reason a live delegation to a linked crew came
// back "not found in crew" while the link was active the whole time.

func connectedCrewsRig(t *testing.T) (h *InternalHandler, db *sql.DB, wsID, engID, opsID string) {
	t.Helper()
	db = setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	engID = seedCrewRow(t, db, "crew-eng", wsID, "Engineering", "engineering")
	opsID = seedCrewRow(t, db, "crew-ops", wsID, "Ops", "ops")
	seedAgentRow(t, db, "agent-morgan", wsID, opsID, "Morgan", "morgan", "LEAD")
	seedAgentRow(t, db, "agent-riley", wsID, opsID, "Riley", "riley", "AGENT")
	h = &InternalHandler{db: db, logger: newTestLogger()}
	return
}

func linkCrews(t *testing.T, db *sql.DB, id, wsID, from, to, direction string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'active', datetime('now'), datetime('now'))`,
		id, wsID, from, to, direction); err != nil {
		t.Fatalf("link crews: %v", err)
	}
}

func TestResolveConnectedCrews_ListsTheLinkedCrewAndItsAgents(t *testing.T) {
	h, db, wsID, engID, opsID := connectedCrewsRig(t)
	linkCrews(t, db, "cc-1", wsID, engID, opsID, "bidirectional")

	got := h.resolveConnectedCrews(httptest.NewRequest("GET", "/x", nil), wsID, engID)
	if len(got) != 1 {
		t.Fatalf("got %d connected crews, want 1: %+v", len(got), got)
	}
	if got[0].Slug != "ops" || got[0].Direction != "bidirectional" {
		t.Fatalf("crew = %+v, want ops/bidirectional", got[0])
	}
	if len(got[0].Agents) != 2 {
		t.Fatalf("got %d agents, want 2: %+v", len(got[0].Agents), got[0].Agents)
	}
	var sawLead bool
	for _, a := range got[0].Agents {
		if a.Slug == "morgan" && a.IsLead {
			sawLead = true
		}
	}
	if !sawLead {
		t.Errorf("the other crew's LEAD is not marked as such: %+v", got[0].Agents)
	}
}

// A link stored the other way round is still a link — for an inbound-only one
// the caller may NOT dispatch, and the direction has to say so or the prompt
// would advertise a door that answers 403.
func TestResolveConnectedCrews_ReportsDirectionFromThisCrewsSide(t *testing.T) {
	h, db, wsID, engID, opsID := connectedCrewsRig(t)
	// ops → engineering, one-way: Ops may dispatch INTO Engineering.
	linkCrews(t, db, "cc-in", wsID, opsID, engID, "unidirectional")

	fromEng := h.resolveConnectedCrews(httptest.NewRequest("GET", "/x", nil), wsID, engID)
	if len(fromEng) != 1 || fromEng[0].Direction != "inbound" {
		t.Fatalf("engineering side = %+v, want one entry with direction inbound", fromEng)
	}

	fromOps := h.resolveConnectedCrews(httptest.NewRequest("GET", "/x", nil), wsID, opsID)
	if len(fromOps) != 1 || fromOps[0].Direction != "unidirectional" {
		t.Fatalf("ops side = %+v, want one entry with direction unidirectional", fromOps)
	}
}

func TestResolveConnectedCrews_SkipsDeletedCrewsAndInactiveLinks(t *testing.T) {
	h, db, wsID, engID, opsID := connectedCrewsRig(t)
	linkCrews(t, db, "cc-dead", wsID, engID, opsID, "bidirectional")
	if _, err := db.Exec(`UPDATE crews SET deleted_at = datetime('now') WHERE id = ?`, opsID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if got := h.resolveConnectedCrews(httptest.NewRequest("GET", "/x", nil), wsID, engID); len(got) != 0 {
		t.Fatalf("got %+v, want none — the linked crew is deleted", got)
	}
}

func TestResolveConnectedCrews_NoLinks_ReturnsEmpty(t *testing.T) {
	h, _, wsID, engID, _ := connectedCrewsRig(t)
	if got := h.resolveConnectedCrews(httptest.NewRequest("GET", "/x", nil), wsID, engID); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}
