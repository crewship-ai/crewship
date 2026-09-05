package pipeline

// Tests for the §9.6 outcome contract on pipeline_runs (work package B6,
// #2349) — the routine-run twin of internal/api/assignments_run_outcome_test.go.
// Same three accept clauses: NO_CHANGE creates no inbox item, NEEDS_HUMAN
// creates exactly one with a valid action contract, and a run ending
// without an outcome is FAILED with the stated reason.

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestMarkTerminal_NoOutcomeReported_DefaultsToFailedWithReason(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_no_outcome", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo"}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := store.MarkTerminal(ctx, MarkTerminalInput{
		RunID:  "run_no_outcome",
		Status: RunStatusCompleted,
		Output: "everything ran fine, nothing special to report",
	}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}

	got, err := store.Get(ctx, "run_no_outcome")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RunStatusCompleted {
		t.Errorf("status = %q, want completed (unaffected by outcome defaulting)", got.Status)
	}
	if got.Outcome != orchestrator.OutcomeFailed {
		t.Errorf("outcome = %q, want %q", got.Outcome, orchestrator.OutcomeFailed)
	}
	if got.ErrorMessage != orchestrator.ReasonNoOutcomeReported {
		t.Errorf("error_message = %q, want the stated reason %q", got.ErrorMessage, orchestrator.ReasonNoOutcomeReported)
	}
}

func TestMarkTerminal_RealFailure_KeepsOwnErrorMessage(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_real_fail", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.MarkTerminal(ctx, MarkTerminalInput{
		RunID:        "run_real_fail",
		Status:       RunStatusFailed,
		ErrorMessage: "step \"deploy\" exited 1",
	}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	got, err := store.Get(ctx, "run_real_fail")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome != orchestrator.OutcomeFailed {
		t.Errorf("outcome = %q, want %q", got.Outcome, orchestrator.OutcomeFailed)
	}
	if got.ErrorMessage != `step "deploy" exited 1` {
		t.Errorf("error_message = %q, must keep the REAL failure reason", got.ErrorMessage)
	}
}

func TestMarkTerminal_CancelledStatus_OutcomeCancelled(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_cancel", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_cancel", Status: RunStatusCancelled}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	got, err := store.Get(ctx, "run_cancel")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome != orchestrator.OutcomeCancelled {
		t.Errorf("outcome = %q, want %q", got.Outcome, orchestrator.OutcomeCancelled)
	}
}

func TestMarkTerminal_DryRun_OutcomeNoChange(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_dry", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo", Mode: ModeDryRun}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_dry", Status: RunStatusDryRunOK}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	got, err := store.Get(ctx, "run_dry")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome != orchestrator.OutcomeNoChange {
		t.Errorf("outcome = %q, want %q (a dry run makes no real change)", got.Outcome, orchestrator.OutcomeNoChange)
	}
}

func TestMarkTerminal_CheckpointOutcomeInOutput_IsRespected(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_checkpoint_out", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	output := "Rotated the API key.\n\n---CHECKPOINT---\ndone: rotated key\nnext_step: none\nconfidence: high\noutcome: SUCCEEDED\n---END CHECKPOINT---\n"
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_checkpoint_out", Status: RunStatusCompleted, Output: output}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	got, err := store.Get(ctx, "run_checkpoint_out")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome != orchestrator.OutcomeSucceeded {
		t.Errorf("outcome = %q, want %q", got.Outcome, orchestrator.OutcomeSucceeded)
	}
	if got.ErrorMessage != "" {
		t.Errorf("error_message = %q, want empty (a reported outcome needs no default reason)", got.ErrorMessage)
	}
}

// ── NO_CHANGE creates no inbox item; NEEDS_HUMAN creates exactly one ────

// openRunsTestDBWithInbox layers a minimal inbox_items table onto
// openRunsTestDB's schema — inbox.Insert's own target — so MarkTerminal's
// outcome-routed inbox write has somewhere to land.
func openRunsTestDBWithInbox(t *testing.T) (*RunStore, *sql.DB) {
	t.Helper()
	store, db := openRunsTestDB(t)
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE inbox_items (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    kind                TEXT NOT NULL,
    source_id           TEXT NOT NULL,
    target_user_id      TEXT,
    target_role         TEXT,
    title               TEXT NOT NULL,
    body_md             TEXT,
    sender_type         TEXT,
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL DEFAULT 'unread',
    priority            TEXT NOT NULL DEFAULT 'medium',
    blocking            INTEGER NOT NULL DEFAULT 1,
    payload_json        TEXT NOT NULL DEFAULT '{}',
    read_at TEXT, read_by_user_id TEXT, resolved_at TEXT, resolved_by_user_id TEXT, resolved_action TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_inbox_items_kind_source ON inbox_items (kind, source_id);
`); err != nil {
		t.Fatalf("inbox schema: %v", err)
	}
	return store, db
}

func TestMarkTerminal_NoChangeOutcome_CreatesNoInboxItem(t *testing.T) {
	store, db := openRunsTestDBWithInbox(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_ibx_nochange", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	output := "---CHECKPOINT---\ndone: checked\nnext_step: none\nconfidence: high\noutcome: NO_CHANGE\n---END CHECKPOINT---\n"
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_ibx_nochange", Status: RunStatusCompleted, Output: output}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND source_id = 'run_ibx_nochange'`, inbox.KindRunNeedsHuman).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("inbox items = %d, want 0", n)
	}
}

func TestMarkTerminal_NeedsHumanOutcome_CreatesExactlyOneInboxItemWithActionContract(t *testing.T) {
	store, db := openRunsTestDBWithInbox(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{
		ID: "run_ibx_needs_human", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "nightly-sync",
		TriggeredVia: TriggeredViaIssue, TriggeredByID: "ENG-42",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	output := "---CHECKPOINT---\ndone: half the sync\nblockers: rate limited by the upstream API\nnext_step: retry after cooldown\nconfidence: medium\noutcome: NEEDS_HUMAN\n---END CHECKPOINT---\n"
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_ibx_needs_human", Status: RunStatusCompleted, Output: output}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND source_id = 'run_ibx_needs_human'`, inbox.KindRunNeedsHuman).Scan(&n); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if n != 1 {
		t.Fatalf("inbox items = %d, want exactly 1", n)
	}

	var payloadRaw string
	if err := db.QueryRow(`SELECT payload_json FROM inbox_items WHERE kind = ? AND source_id = 'run_ibx_needs_human'`, inbox.KindRunNeedsHuman).Scan(&payloadRaw); err != nil {
		t.Fatalf("query payload: %v", err)
	}
	var payload struct {
		AttentionClass string           `json:"attention_class"`
		ThreadKey      string           `json:"thread_key"`
		WhoCanAct      []string         `json:"who_can_act"`
		Actions        []map[string]any `json:"actions"`
		Context        map[string]any   `json:"context"`
	}
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatalf("unmarshal: %v; raw=%s", err, payloadRaw)
	}
	if payload.AttentionClass != "input" {
		t.Errorf("attention_class = %q, want input", payload.AttentionClass)
	}
	if payload.ThreadKey == "" {
		t.Error("thread_key is empty")
	}
	if len(payload.WhoCanAct) == 0 {
		t.Error("who_can_act is empty")
	}
	if len(payload.Actions) == 0 {
		t.Fatal("actions is empty")
	}
	if payload.Context["issue"] != "ENG-42" {
		t.Errorf("context.issue = %v, want ENG-42 (from triggered_by_id)", payload.Context["issue"])
	}
}
