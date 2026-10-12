package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

func assignmentProjectionFixture(t *testing.T) (*AssignmentRuntime, *work.Store, *work.Claimed) {
	t.Helper()
	f := setupDelegationFixture(t)
	f.insertRootDispatch(t, "projection", f.lead, f.work1, "PENDING")
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := acceptAssignmentTx(t.Context(), tx, "projection")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	f.h.SetJournal(writer)
	store := work.NewStore(f.db)
	a, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "test", WorkID: receipt.WorkID})
	if err != nil {
		t.Fatal(err)
	}
	return &AssignmentRuntime{h: f.h, base: &WebhookRuntime{}}, store, a
}
func TestAssignmentProjectionWaitsForAuthorityAndReplaysReceipt(t *testing.T) {
	rt, store, a := assignmentProjectionFixture(t)
	code := 0
	result := work.RunResult{ExitCode: &code, Metadata: map[string]any{"assignment_result": "---HANDOFF---\nsummary: done\nconfidence: high\noutcome: SUCCEEDED\n---END HANDOFF---"}}
	if err := store.StageRunResult(t.Context(), a.Item.ID, a.RunID, a.Generation, result); err != nil {
		t.Fatal(err)
	}
	// An observed output is not a terminal authority while the worker holds it.
	if err := rt.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := rt.h.db.QueryRow(`SELECT status FROM assignments WHERE id='projection'`).Scan(&status); err != nil || status != "PENDING" {
		t.Fatalf("premature status=%s err=%v", status, err)
	}
	if err := store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(t.Context(), work.TransitionRequest{WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: work.StateSucceeded, Reason: "actual inspected exit"}); err != nil {
		t.Fatal(err)
	}
	// Settlement owns the historical accepted scope even if current target
	// and crew change before outbox projection. They cannot revoke an ended run.
	execOrFatal(t, rt.h.db, `UPDATE assignments SET assigned_to_id=(SELECT assigned_by_id FROM assignments WHERE id='projection') WHERE id='projection'`)
	execOrFatal(t, rt.h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('projection-new-crew',?,'New crew','projection-new-crew')`, a.Item.WorkspaceID)
	execOrFatal(t, rt.h.db, `UPDATE agents SET crew_id='projection-new-crew' WHERE id=?`, a.Item.AgentID)
	// Lost acknowledgement after domain+journal commit is a replay, not rerun.
	execOrFatal(t, rt.h.db, `CREATE TRIGGER lose_projection_ack BEFORE UPDATE OF work_projected_at ON assignments BEGIN SELECT RAISE(ABORT,'lost ACK'); END`)
	if err := rt.Flush(t.Context()); err == nil {
		t.Fatal("lost acknowledgement accepted")
	}
	execOrFatal(t, rt.h.db, `DROP TRIGGER lose_projection_ack`)
	execOrFatal(t, rt.h.db, `UPDATE agents SET slug=slug||'-renamed' WHERE id=?`, a.Item.AgentID)
	for range 3 {
		if err := rt.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := rt.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id='assignment-terminal:projection'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("terminal receipts=%d err=%v", n, err)
	}
	if err := rt.h.db.QueryRow(`SELECT status FROM assignments WHERE id='projection'`).Scan(&status); err != nil || status != "COMPLETED" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}
func TestAssignmentProjectionQueuedCancellationHasNoAttempt(t *testing.T) {
	f := setupDelegationFixture(t)
	f.insertRootDispatch(t, "cancel", f.lead, f.work1, "PENDING")
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = acceptAssignmentTx(t.Context(), tx, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	w := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = w.Close() })
	f.h.SetJournal(w)
	execOrFatal(t, f.db, `UPDATE assignments SET cancel_requested_at=datetime('now') WHERE id='cancel'`)
	rt := &AssignmentRuntime{h: f.h}
	for range 2 {
		if err = rt.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var attempts int
	if err = f.db.QueryRow(`SELECT a.status,w.attempts FROM assignments a JOIN work_items w ON w.domain_id=a.id WHERE a.id='cancel'`).Scan(&status, &attempts); err != nil || status != "CANCELLED" || attempts != 0 {
		t.Fatalf("status=%s attempts=%d err=%v", status, attempts, err)
	}
}
func TestAssignmentRuntimeDoesNotInventExitStatus(t *testing.T) {
	rt, store, a := assignmentProjectionFixture(t)
	execution := &assignmentExecution{assignment: dispatch.Assignment{Item: a.Item, RunID: a.RunID, Generation: a.Generation}, launch: &webhookLaunch{store: store, workID: a.Item.ID, runID: a.RunID, generation: a.Generation}}
	ctx := context.WithValue(t.Context(), assignmentExecutionKey{}, execution)
	if !rt.h.stageAssignmentResult(ctx, "projection", a.RunID, "", "synthetic failure text", nil) {
		t.Fatal("not staged")
	}
	p, owned, err := store.RunProjection(t.Context(), a.RunID)
	if err != nil || !owned || p.Ready {
		t.Fatalf("projection=%+v owned=%v err=%v", p, owned, err)
	}
	var raw string
	if err = rt.h.db.QueryRow(`SELECT run_result_json FROM work_attempts WHERE run_id=?`, a.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result work.RunResult
	if err = json.Unmarshal([]byte(raw), &result); err != nil || result.ExitCode != nil {
		t.Fatalf("invented exit raw=%s err=%v", raw, err)
	}
	if execution.runErr == nil || errors.Is(execution.runErr, context.Canceled) {
		t.Fatalf("lost failure %v", execution.runErr)
	}
}

func TestAssignmentProjectionInboxReceiptSurvivesAckLoss(t *testing.T) {
	rt, store, a := assignmentProjectionFixture(t)
	code := 0
	result := work.RunResult{ExitCode: &code, Metadata: map[string]any{"assignment_result": "---HANDOFF---\nsummary: need a decision\nconfidence: high\noutcome: NEEDS_HUMAN\n---END HANDOFF---"}}
	if err := store.StageRunResult(t.Context(), a.Item.ID, a.RunID, a.Generation, result); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(t.Context(), work.TransitionRequest{WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: work.StateSucceeded, Reason: "actual exit"}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, rt.h.db, `CREATE TRIGGER refuse_inbox BEFORE INSERT ON inbox_items BEGIN SELECT RAISE(ABORT,'inbox unavailable'); END`)
	if err := rt.Flush(t.Context()); err == nil {
		t.Fatal("inbox failure acknowledged")
	}
	var ack int
	if err := rt.h.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id='projection' AND work_projected_at IS NOT NULL`).Scan(&ack); err != nil || ack != 0 {
		t.Fatalf("premature ACK=%d err=%v", ack, err)
	}
	execOrFatal(t, rt.h.db, `DROP TRIGGER refuse_inbox`)
	execOrFatal(t, rt.h.db, `CREATE TRIGGER lose_inbox_ack BEFORE UPDATE OF work_projected_at ON assignments BEGIN SELECT RAISE(ABORT,'lost final ACK'); END`)
	if err := rt.Flush(t.Context()); err == nil {
		t.Fatal("lost final ACK accepted")
	}
	execOrFatal(t, rt.h.db, `UPDATE inbox_items SET state='resolved',read_at=datetime('now') WHERE source_id='projection'`)
	execOrFatal(t, rt.h.db, `DROP TRIGGER lose_inbox_ack`)
	for range 2 {
		if err := rt.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	var state string
	if err := rt.h.db.QueryRow(`SELECT COUNT(*),MIN(state) FROM inbox_items WHERE source_id='projection'`).Scan(&n, &state); err != nil || n != 1 || state != "resolved" {
		t.Fatalf("inbox n=%d state=%s err=%v", n, state, err)
	}
}

func TestAssignmentMissingTerminalAfterCreationRemainsUnclear(t *testing.T) {
	rt, store, a := assignmentProjectionFixture(t)
	assignment := dispatch.Assignment{Item: a.Item, RunID: a.RunID, Generation: a.Generation}
	launch := &webhookLaunch{store: store, workID: a.Item.ID, runID: a.RunID, generation: a.Generation}
	if err := store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(orchestrator.RunLocation{RunID: a.RunID}); err != nil {
		t.Fatal(err)
	}
	if err := launch.RequestCreation(t.Context()); err != nil {
		t.Fatal(err)
	}
	execution := &assignmentExecution{assignment: assignment, launch: launch}
	ctx := context.WithValue(t.Context(), assignmentExecutionKey{}, execution)
	rt.h.stageAssignmentResult(ctx, "projection", a.RunID, "", "", nil)
	if execution.stageErr != nil {
		t.Fatal(execution.stageErr)
	}
	runtime := &agentWorkRuntime{WebhookRuntime: rt.base, assignment: rt}
	if got := runtime.Classify(assignment, execution.runErr); got != dispatch.OutcomeUnclear {
		t.Fatalf("requested execution without terminal evidence classified %s", got)
	}
}
