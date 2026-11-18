package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestKeeperDecisionDurablyCorrelatesCallingRun(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedKeeperFixture(t, db)
	writer := newKeeperTestJournal(t, db)
	const run = "keeper-fixture-run"
	if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture run started"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "fixture", RiskScore: 1}}
	h := NewKeeperHandler(db, "synthetic-internal", gk, keeperEventsLogger())
	h.journal = writer
	body, _ := json.Marshal(map[string]string{"workspace_id": ws, "requesting_crew_id": crew, "requesting_agent_id": agent, "requesting_run_id": run, "credential_id": credential, "intent": "fixture access"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.HandleRequest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("request returned %d: %s", w.Code, w.Body.String())
	}
	id := latestRequestID(t, db)
	var recorded string
	if err := db.QueryRow(`SELECT COALESCE(requesting_run_id,'') FROM keeper_requests WHERE id=?`, id).Scan(&recorded); err != nil || recorded != run {
		t.Fatalf("request lost calling run: %q, %v", recorded, err)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_request_events WHERE request_id=? AND requesting_run_id=?`, id, run).Scan(&events); err != nil || events != 2 {
		t.Fatalf("run-correlated pending/decision events=%d, %v", events, err)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type=? AND agent_id=? AND trace_id=?`, string(journal.EntryKeeperDecision), agent, run).Scan(&events); err != nil || events != 1 {
		t.Fatalf("journal lost decision correlation: %d, %v", events, err)
	}
}
