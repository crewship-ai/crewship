package api

// Tests for the Watch Roster read endpoint.
//
// Coverage focus:
//   - list returns only the caller's workspace
//   - cross-workspace crew filter 404s (no existence leak)

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/presence"
)

func TestPresenceRoster_WorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-p1", wsID, "Presence Crew", "presence-crew")
	agentID := seedAgentRow(t, db, "agent-p1", wsID, crewID, "A", "a", "AGENT")

	// Upsert a snapshot for this agent; this is what the roster should surface.
	if err := presence.Upsert(context.Background(), db, noopEmitter{}, presence.Snapshot{
		AgentID:     agentID,
		WorkspaceID: wsID,
		CrewID:      crewID,
		Status:      presence.StatusOnline,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Seed an unrelated workspace + snapshot that must NOT leak.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	otherCrew := seedCrewRow(t, db, "crew-p2", otherWS, "Other", "other")
	otherAgent := seedAgentRow(t, db, "agent-p2", otherWS, otherCrew, "O", "o", "AGENT")
	if err := presence.Upsert(context.Background(), db, noopEmitter{}, presence.Snapshot{
		AgentID:     otherAgent,
		WorkspaceID: otherWS,
		CrewID:      otherCrew,
		Status:      presence.StatusBusy,
	}); err != nil {
		t.Fatalf("upsert other: %v", err)
	}

	h := NewPresenceHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/presence/roster", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Roster(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows  []rosterRow `json:"rows"`
		Count int         `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1 (other workspace leaked?)", resp.Count)
	}
	if resp.Rows[0].AgentID != agentID {
		t.Errorf("agent_id = %q, want %q", resp.Rows[0].AgentID, agentID)
	}
	if resp.Rows[0].Status != "online" {
		t.Errorf("status = %q, want online", resp.Rows[0].Status)
	}
}

func TestPresenceRoster_CrossTenantCrewFilter_404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Create a crew in another workspace.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreignCrew := seedCrewRow(t, db, "crew-foreign", otherWS, "F", "f")

	h := NewPresenceHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/presence/roster?crew_id="+foreignCrew, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Roster(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant crew filter status = %d, want 404", rr.Code)
	}
}
