package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// A chat/sidecar run must record its agent's crew on run.started: the crew
// overview's run metrics query by recorded crew ownership, and before this
// every run.started carried crew_id NULL, so every crew showed "No runs".
func TestCreateRun_RecordsAgentCrewForCrewInsights(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-runs", wsID, "Runs", "runs")
	agentID := seedAgentRow(t, db, "agent-runs", wsID, "crew-runs", "Runner", "runner", "AGENT")
	h := NewInternalHandler(db, "tok", newTestLogger())
	w := wireTestJournalForHandler(t, db, h)

	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{
		"id": "run-crew-1", "agent_id": agentID, "workspace_id": wsID, "trigger_type": "USER",
	}))
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	_ = w.Flush(context.Background())

	var crewID string
	if err := db.QueryRow(`SELECT COALESCE(crew_id, '') FROM journal_entries WHERE trace_id = 'run-crew-1' AND entry_type = 'run.started'`).Scan(&crewID); err != nil {
		t.Fatalf("read run.started: %v", err)
	}
	if crewID != "crew-runs" {
		t.Fatalf("run.started crew_id = %q, want crew-runs", crewID)
	}
	byCrew, err := journal.RunInsightsScoped(context.Background(), db, wsID, journal.RunWindow7d, "", "crew-runs")
	if err != nil {
		t.Fatal(err)
	}
	if byCrew.Total != 1 {
		t.Fatalf("crew-scoped insights total = %d, want 1", byCrew.Total)
	}
}
