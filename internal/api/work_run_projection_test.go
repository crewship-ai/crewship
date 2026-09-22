package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/work"
)

// Uses the real internal handler and journal boundary, without a network fixture
// or an agent. A retry of projection must never reach RunAgent.
type outcomeProjectionResolver struct {
	chatbridge.ChatResolver
	handler   *InternalHandler
	loseReply atomic.Bool
}

func (r *outcomeProjectionResolver) UpdateRun(ctx context.Context, id, status string, exit *int, message *string, meta map[string]interface{}) error {
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/"+id, jsonBody(map[string]any{"status": status, "exit_code": exit, "error_message": message, "metadata": meta})).WithContext(ctx)
	req.SetPathValue("runId", id)
	rec := httptest.NewRecorder()
	r.handler.UpdateRun(rec, req)
	if rec.Code != http.StatusOK {
		return fmt.Errorf("projection HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if r.loseReply.Swap(false) {
		return errors.New("injected lost acknowledgement after commit")
	}
	return nil
}

type failingOutcomeJournal struct{ journal.SyncEmitter }

func (f failingOutcomeJournal) EmitSync(context.Context, journal.Entry) (string, error) {
	return "", errors.New("injected journal write failure")
}

func outcomeFixture(t *testing.T) (*InternalHandler, *work.Store, *work.Claimed, *journal.Writer) {
	t.Helper()
	h, workspace, agent := covIRunFixture(t)
	writer := wireTestJournalForHandler(t, h.db, h)
	store := work.NewStore(h.db)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: workspace, AgentID: agent, Source: work.SourceWebhook, Class: work.ClassBackground}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "test"})
	if err != nil {
		t.Fatal(err)
	}
	seedRunFixture(t, h.db, a.RunID, agent, workspace, "RUNNING", "WEBHOOK", "")
	if err := store.StageRunResult(t.Context(), a.Item.ID, a.RunID, a.Generation, work.RunResult{Metadata: map[string]any{"total_cost_usd": 0.25}}); err != nil {
		t.Fatal(err)
	}
	return h, store, a, writer
}

func settleOutcome(t *testing.T, s *work.Store, a *work.Claimed, to work.State) {
	t.Helper()
	if err := s.Transition(t.Context(), work.TransitionRequest{WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: to, Reason: "provider confirmed stop"}); err != nil {
		t.Fatal(err)
	}
}

func outcomeTerminalCount(t *testing.T, h *InternalHandler, runID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM journal_entries WHERE trace_id = ? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorkRunProjection_RejectsPrematureAndCrossWorkspaceTerminalWrites(t *testing.T) {
	h, s, a, _ := outcomeFixture(t)
	if _, err := s.RequestCancel(t.Context(), a.Item.ID, "operator", "stop"); err != nil {
		t.Fatal(err)
	}
	request := func(ctx context.Context, want int) {
		t.Helper()
		r := httptest.NewRequest("PATCH", "/", jsonBody(map[string]any{"status": "CANCELLED"})).WithContext(ctx)
		r.SetPathValue("runId", a.RunID)
		w := httptest.NewRecorder()
		h.UpdateRun(w, r)
		if w.Code != want {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	request(t.Context(), http.StatusConflict)
	request(context.WithValue(t.Context(), ctxInternalTokenWS, "foreign-workspace"), http.StatusNotFound)
	if n := outcomeTerminalCount(t, h, a.RunID); n != 0 {
		t.Fatalf("unconfirmed/cross-tenant write emitted %d terminals", n)
	}
	settleOutcome(t, s, a, work.StateCancelled)
	// Even a caller claiming COMPLETED with invented usage cannot override the ledger.
	r := outcomeProjectionResolver{handler: h}
	if err := r.UpdateRun(t.Context(), a.RunID, "COMPLETED", nil, nil, map[string]interface{}{"total_cost_usd": 999}); err != nil {
		t.Fatal(err)
	}
	var kind, payload string
	if err := h.db.QueryRowContext(t.Context(), `SELECT entry_type,payload FROM journal_entries WHERE trace_id=? AND entry_type='run.cancelled'`, a.RunID).Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "run.cancelled" {
		t.Fatal(kind)
	}
	p, _, err := s.RunProjection(t.Context(), a.RunID)
	if err != nil || p.Result.Metadata["total_cost_usd"] != 0.25 {
		t.Fatalf("caller overrode stored result: %+v %v", p, err)
	}
}

func TestWorkRunProjection_RestartRetriesFailedWriteAndLostAckWithoutExecution(t *testing.T) {
	h, s, a, writer := outcomeFixture(t)
	settleOutcome(t, s, a, work.StateCancelled)
	resolver := &outcomeProjectionResolver{handler: h}
	process := newFakeAgentProcess()
	handler := &WebhookHandler{db: h.db, resolver: resolver, orch: process}
	h.SetJournal(failingOutcomeJournal{writer})
	if err := NewWebhookRuntime(handler).FlushRunOutcomes(t.Context()); err == nil {
		t.Fatal("write failure was acknowledged")
	}
	if n := outcomeTerminalCount(t, h, a.RunID); n != 0 {
		t.Fatal("failed write emitted a terminal")
	}
	if ids, err := s.PendingRunProjections(t.Context(), 10); err != nil || len(ids) != 1 {
		t.Fatalf("failed write lost durable retry: %v %v", ids, err)
	}
	h.SetJournal(writer)
	resolver.loseReply.Store(true)
	if err := NewWebhookRuntime(handler).FlushRunOutcomes(t.Context()); err == nil {
		t.Fatal("lost IPC acknowledgement did not fail")
	}
	if n := outcomeTerminalCount(t, h, a.RunID); n != 1 {
		t.Fatalf("committed before lost ack: %d", n)
	}
	// A fresh runtime/store has no memory of either prior attempt.
	if err := NewWebhookRuntime(handler).FlushRunOutcomes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := outcomeTerminalCount(t, h, a.RunID); n != 1 {
		t.Fatalf("retry duplicated terminal: %d", n)
	}
	if ids, err := work.NewStore(h.db).PendingRunProjections(t.Context(), 10); err != nil || len(ids) != 0 {
		t.Fatalf("outbox not acknowledged: %v %v", ids, err)
	}
	if n := len(process.runsStarted()); n != 0 {
		t.Fatalf("projection re-executed agent %d times", n)
	}
}

func TestWorkRunProjection_ConcurrentWritersEmitOneTerminal(t *testing.T) {
	h, s, a, _ := outcomeFixture(t)
	settleOutcome(t, s, a, work.StateCancelled)
	resolver := &outcomeProjectionResolver{handler: h}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = resolver.UpdateRun(t.Context(), a.RunID, "FAILED", nil, nil, nil) }()
	}
	wg.Wait()
	if err := resolver.UpdateRun(t.Context(), a.RunID, "FAILED", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := outcomeTerminalCount(t, h, a.RunID); n != 1 {
		t.Fatalf("concurrent projection emitted %d terminal entries", n)
	}
}

func TestWorkRunProjection_LegacyTerminalRetryDoesNotRequireBackfill(t *testing.T) {
	h, _, a, writer := outcomeFixture(t)
	if _, err := h.db.ExecContext(t.Context(), `UPDATE work_attempts SET run_result_json = NULL, run_status = '' WHERE run_id = ?`, a.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: a.Item.WorkspaceID, AgentID: a.Item.AgentID, Type: journal.EntryRunFailed, Severity: journal.SeverityError, ActorType: journal.ActorSystem, Summary: "legacy failure", TraceID: a.RunID}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PATCH", "/", jsonBody(map[string]any{"status": "CANCELLED"}))
	r.SetPathValue("runId", a.RunID)
	w := httptest.NewRecorder()
	h.UpdateRun(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"FAILED"`) {
		t.Fatalf("legacy retry: %d %s", w.Code, w.Body.String())
	}
	if n := outcomeTerminalCount(t, h, a.RunID); n != 1 {
		t.Fatalf("legacy retry appended %d terminals", n)
	}
}

func TestWorkRunProjection_ConflictingHistoricalTerminalStaysVisible(t *testing.T) {
	h, s, a, writer := outcomeFixture(t)
	settleOutcome(t, s, a, work.StateCancelled)
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: a.Item.WorkspaceID, AgentID: a.Item.AgentID, Type: journal.EntryRunFailed, Severity: journal.SeverityError, ActorType: journal.ActorSystem, Summary: "old incorrect failure", TraceID: a.RunID}); err != nil {
		t.Fatal(err)
	}
	rt := NewWebhookRuntime(&WebhookHandler{db: h.db, resolver: &outcomeProjectionResolver{handler: h}})
	if err := rt.FlushRunOutcomes(t.Context()); err == nil {
		t.Fatal("contradictory terminal acknowledged as corrected")
	}
	if ids, err := s.PendingRunProjections(t.Context(), 10); err != nil || len(ids) != 1 {
		t.Fatalf("conflict lost retry/evidence: %v %v", ids, err)
	}
	if n := outcomeTerminalCount(t, h, a.RunID); n != 1 {
		t.Fatalf("immutable history rewritten: %d", n)
	}
}
