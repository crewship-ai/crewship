package api

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestCreateRunRetrySingleIdentityAndConflict(t *testing.T) {
	h, ws, agent := covIRunFixture(t)
	writer := wireTestJournalForHandler(t, h.db, h)
	call := func(run, trigger string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": run, "agent_id": agent, "workspace_id": ws, "trigger_type": trigger}))
		rr := httptest.NewRecorder()
		h.CreateRun(rr, req)
		return rr
	}
	// Concurrent retries exercise the persisted primary-key race, not just a
	// check-then-return against one already completed HTTP request.
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call("retry-run", "SCHEDULED") }()
	}
	wg.Wait()
	close(results)
	for rr := range results {
		if rr.Code != 201 {
			t.Fatalf("retry %d %s", rr.Code, rr.Body.String())
		}
	}
	// Model the crash window after durable start but before status projection.
	execOrFatal(t, h.db, `UPDATE agents SET status='IDLE' WHERE id=?`, agent)
	for i := 0; i < 2; i++ {
		if rr := call("retry-run", "SCHEDULED"); rr.Code != 201 {
			t.Fatalf("sequential retry %d %s", rr.Code, rr.Body.String())
		}
	}
	var projected string
	if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, agent).Scan(&projected); err != nil || projected != "RUNNING" {
		t.Fatalf("admission retry did not recover status: %s %v", projected, err)
	}
	if rr := call("retry-run", "WEBHOOK"); rr.Code != 409 {
		t.Fatalf("conflicting admission %d %s", rr.Code, rr.Body.String())
	}
	var starts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='retry-run' AND entry_type='run.started'`).Scan(&starts); err != nil || starts != 1 {
		t.Fatalf("starts %d %v", starts, err)
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: ws, AgentID: agent, Type: journal.EntryRunCompleted, ActorType: journal.ActorOrchestrator, Summary: "completed", TraceID: "retry-run"}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE agents SET status='IDLE' WHERE id=?`, agent)
	if rr := call("retry-run", "SCHEDULED"); rr.Code != 409 {
		t.Fatalf("terminal execution relaunched: %d %s", rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, agent).Scan(&status); err != nil || status != "IDLE" {
		t.Fatalf("terminal retry changed status %s %v", status, err)
	}
}

func TestCreateRunRetryAcceptsSingleLegacyStartRejectsAmbiguity(t *testing.T) {
	h, ws, agent := covIRunFixture(t)
	writer := wireTestJournalForHandler(t, h.db, h)
	var crew string
	if err := h.db.QueryRow(`SELECT COALESCE(crew_id,'') FROM agents WHERE id=?`, agent).Scan(&crew); err != nil {
		t.Fatal(err)
	}
	entry := journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, Type: journal.EntryRunStarted, ActorType: journal.ActorSidecar, Summary: "legacy start", TraceID: "legacy-retry", Payload: map[string]any{"trigger_type": "USER", "host_admitted": true}}
	if _, err := writer.EmitSync(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.CreateRun(rr, httptest.NewRequest("POST", "/", jsonBody(map[string]any{"id": "legacy-retry", "agent_id": agent, "workspace_id": ws})))
		return rr
	}
	if rr := call(); rr.Code != 201 {
		t.Fatalf("legacy identical retry %d %s", rr.Code, rr.Body.String())
	}
	if _, err := writer.EmitSync(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if rr := call(); rr.Code != 409 {
		t.Fatalf("ambiguous legacy identity %d %s", rr.Code, rr.Body.String())
	}
	var starts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='legacy-retry' AND entry_type='run.started'`).Scan(&starts); err != nil || starts != 2 {
		t.Fatalf("retry appended another legacy start %d %v", starts, err)
	}
}

func TestCreateRunRetryMetadataPreservesLargeIntegerIdentity(t *testing.T) {
	h, ws, agent := covIRunFixture(t)
	wireTestJournalForHandler(t, h.db, h)
	call := func(metadata string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.CreateRun(rr, httptest.NewRequest("POST", "/", jsonBody(map[string]any{"id": "integer-identity", "agent_id": agent, "workspace_id": ws, "metadata": json.RawMessage(metadata)})))
		return rr
	}
	for i := 0; i < 2; i++ {
		if rr := call(`{"n":9007199254740992}`); rr.Code != 201 {
			t.Fatalf("same integer retry %d %s", rr.Code, rr.Body.String())
		}
	}
	if rr := call(`{"n":9007199254740993}`); rr.Code != 409 {
		t.Fatalf("distinct integer accepted: %d %s", rr.Code, rr.Body.String())
	}
	var starts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='integer-identity' AND entry_type='run.started'`).Scan(&starts); err != nil || starts != 1 {
		t.Fatalf("start count %d %v", starts, err)
	}
}

func TestCreateRunRetryRejectsRetainedTerminalWithoutStart(t *testing.T) {
	h, ws, agent := covIRunFixture(t)
	writer := wireTestJournalForHandler(t, h.db, h)
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: ws, AgentID: agent, Type: journal.EntryRunCompleted, ActorType: journal.ActorOrchestrator, Summary: "Terminal retained after start pruning", TraceID: "terminal-only"}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.CreateRun(rr, httptest.NewRequest("POST", "/", jsonBody(map[string]any{"id": "terminal-only", "agent_id": agent, "workspace_id": ws})))
	if rr.Code != 409 {
		t.Fatalf("terminal-only identity re-admitted %d %s", rr.Code, rr.Body.String())
	}
	var starts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='terminal-only' AND entry_type='run.started'`).Scan(&starts); err != nil || starts != 0 {
		t.Fatalf("start appended after terminal %d %v", starts, err)
	}
}
