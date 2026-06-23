package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// internal_runs_cov_test.go covers the remaining CreateRun / UpdateRun
// branches: DB-error 500s, journal-emit failures (the not-wired
// noopEmitter rejects run.* entries), non-fatal agent-status update
// failures, the token workspace-scope guard, and terminalEntryType's
// trailing arms. Helpers are prefixed covIRun.

func covIRunFixture(t *testing.T) (*InternalHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := seedAgentRow(t, db, "covirun-ag", wsID, "", "Runner", "covirun-ag", "AGENT")
	h := NewInternalHandler(db, "tok", newTestLogger())
	return h, wsID, agentID
}

func TestCovIRun_CreateRun_AgentCheckDBError_500(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	h.db.Close()
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{
		"id": "covirun-r1", "agent_id": agentID, "workspace_id": wsID,
	}))
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovIRun_CreateRun_JournalNotWired_500 — without SetJournal the
// noopEmitter refuses run.* entries, and CreateRun must fail loudly
// rather than flip the agent to RUNNING with no trace.
func TestCovIRun_CreateRun_JournalNotWired_500(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{
		"id": "covirun-r2", "agent_id": agentID, "workspace_id": wsID,
	}))
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if status == "RUNNING" {
		t.Errorf("agent flipped to RUNNING despite failed run.started emit")
	}
}

// TestCovIRun_CreateRun_AgentStatusUpdateFailure_NonFatal — the journal
// entry is durable, so a failing agents UPDATE only logs.
func TestCovIRun_CreateRun_AgentStatusUpdateFailure_NonFatal(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	wireTestJournalForHandler(t, h.db, h)
	execOrFatal(t, h.db, `CREATE TRIGGER covirun_block_upd BEFORE UPDATE ON agents
		BEGIN SELECT RAISE(ABORT, 'covirun forced'); END`)

	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{
		"id": "covirun-r3", "agent_id": agentID, "workspace_id": wsID,
	}))
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"status":"RUNNING"`) {
		t.Errorf("body = %s, want RUNNING ack", rr.Body.String())
	}
}

func TestCovIRun_UpdateRun_LookupDBError_500(t *testing.T) {
	h, _, _ := covIRunFixture(t)
	h.db.Close()
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/covirun-rx", jsonBody(map[string]any{
		"status": "COMPLETED",
	}))
	req.SetPathValue("runId", "covirun-rx")
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovIRun_UpdateRun_WorkspaceScopeMismatch_404 — a workspace-bound
// internal token may not finalize another tenant's run.
func TestCovIRun_UpdateRun_WorkspaceScopeMismatch_404(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	seedRunFixture(t, h.db, "covirun-r4", agentID, wsID, "", "USER", "")

	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/covirun-r4", jsonBody(map[string]any{
		"status": "COMPLETED",
	}))
	req.SetPathValue("runId", "covirun-r4")
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, "someone-elses-ws"))
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no cross-tenant finalize); body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovIRun_UpdateRun_TerminalEmitFailure_500 — run.started exists
// but the journal isn't wired, so the terminal emit fails.
func TestCovIRun_UpdateRun_TerminalEmitFailure_500(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	seedRunFixture(t, h.db, "covirun-r5", agentID, wsID, "", "USER", "")

	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/covirun-r5", jsonBody(map[string]any{
		"status": "FAILED",
	}))
	req.SetPathValue("runId", "covirun-r5")
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovIRun_UpdateRun_AgentUpdateFailure_NonFatal_WithMetadata — the
// terminal journal write succeeds; a blocked agents UPDATE is logged
// only. The metadata payload must round-trip into the terminal entry.
func TestCovIRun_UpdateRun_AgentUpdateFailure_NonFatal_WithMetadata(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	w := wireTestJournalForHandler(t, h.db, h)
	seedRunFixture(t, h.db, "covirun-r6", agentID, wsID, "", "USER", "")
	execOrFatal(t, h.db, `CREATE TRIGGER covirun_block_upd2 BEFORE UPDATE ON agents
		BEGIN SELECT RAISE(ABORT, 'covirun forced'); END`)

	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/covirun-r6", jsonBody(map[string]any{
		"status":   "COMPLETED",
		"metadata": map[string]any{"tokens": 123},
	}))
	req.SetPathValue("runId", "covirun-r6")
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	_ = w.Flush(context.Background())
	var payload string
	if err := h.db.QueryRow(`SELECT payload FROM journal_entries
		WHERE trace_id = 'covirun-r6' AND entry_type = 'run.completed'`).Scan(&payload); err != nil {
		t.Fatalf("terminal entry missing: %v", err)
	}
	if !strings.Contains(payload, `"tokens":123`) {
		t.Errorf("payload = %s, want metadata.tokens=123", payload)
	}
}

func TestCovIRun_TerminalEntryType_AllArms(t *testing.T) {
	cases := map[string]journal.EntryType{
		"COMPLETED":    journal.EntryRunCompleted,
		"FAILED":       journal.EntryRunFailed,
		"CANCELLED":    journal.EntryRunCancelled,
		"TIMEOUT":      journal.EntryRunTimeout,
		"NOT-A-STATUS": journal.EntryRunFailed, // defensive default
	}
	for in, want := range cases {
		if got := terminalEntryType(in); got != want {
			t.Errorf("terminalEntryType(%q) = %v, want %v", in, got, want)
		}
	}
}
