package api

// Tests for the §9.6 outcome contract (PRD-ISSUES-AND-ROUTINES-2026, work
// package B6, #2349) as wired into finishAssignment — the accept line's
// three clauses, one test each:
//
//   - NO_CHANGE creates no inbox item
//   - NEEDS_HUMAN creates exactly one item with a valid action contract
//   - a run ending without an outcome is FAILED with the stated reason
//
// Plus coverage for the routing table reaching session state
// (settleSessionForAssignment) and both existing hand-off shapes
// (CHECKPOINT and HANDOFF) carrying the new outcome field.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ── missing outcome defaults to FAILED with a stated reason ─────────────

func TestFinishAssignment_NoOutcomeReported_DefaultsToFailedWithReason(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	_ = crewID
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-outcome-1', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES ('asg-outcome-missing', ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	// A clean completion (no error) with plain prose — no CHECKPOINT, no
	// HANDOFF block, so no outcome is reported at all.
	h.finishAssignment(context.Background(), "asg-outcome-missing", "", chatID, "asg-worker", wsID,
		"I looked into it and everything seems fine.", "", nil)

	var status, outcome, errMsg string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(outcome,''), COALESCE(error_message,'') FROM assignments WHERE id = 'asg-outcome-missing'`,
	).Scan(&status, &outcome, &errMsg); err != nil {
		t.Fatalf("query: %v", err)
	}
	// status stays technical — the run genuinely completed without error.
	if status != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED (unaffected by outcome defaulting)", status)
	}
	if outcome != orchestrator.OutcomeFailed {
		t.Errorf("outcome = %q, want %q (an absent outcome is a bug, not a silent success)", outcome, orchestrator.OutcomeFailed)
	}
	if errMsg != orchestrator.ReasonNoOutcomeReported {
		t.Errorf("error_message = %q, want the stated reason %q", errMsg, orchestrator.ReasonNoOutcomeReported)
	}
}

// The terminal run.* journal entry — the audit-of-record — must carry the
// SAME defaulted reason the DB row does, not the original (empty) errMsg
// parameter. The two must never disagree about why a run's outcome is
// FAILED.
func TestFinishAssignment_NoOutcomeReported_JournalEntryCarriesTheDefaultedReason(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	_ = crewID
	jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)

	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('asg-outcome-journal', ?, ?, ?, ?, 'task', 'RUNNING', datetime('now'))`,
		wsID, chatID, leadID, workerID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	const runID = "run-outcome-journal-1"
	h.finishAssignment(context.Background(), "asg-outcome-journal", runID, chatID, "asg-worker", wsID,
		"Looked into it, seems fine.", "", nil)
	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := journal.List(context.Background(), h.db, journal.Query{
		WorkspaceID: wsID, Types: []journal.EntryType{journal.EntryRunCompleted}, Limit: 50,
	})
	if err != nil {
		t.Fatalf("journal.List: %v", err)
	}
	var found *journal.Entry
	for i := range entries {
		if entries[i].TraceID == runID {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no run.completed entry found for run %s among %d entries", runID, len(entries))
	}
	if got, _ := found.Payload["outcome"].(string); got != orchestrator.OutcomeFailed {
		t.Errorf("payload.outcome = %q, want %q", got, orchestrator.OutcomeFailed)
	}
	if got, _ := found.Payload["error_message"].(string); got != orchestrator.ReasonNoOutcomeReported {
		t.Errorf("payload.error_message = %q, want the stated reason %q (must match the DB row, not the original empty errMsg)",
			got, orchestrator.ReasonNoOutcomeReported)
	}
}

// A run that DID fail (real errMsg) keeps its own real reason — the
// default-reason fallback must never overwrite a genuine failure cause.
func TestFinishAssignment_RealFailure_KeepsItsOwnErrorMessage_OutcomeFailed(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-outcome-2', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES ('asg-outcome-realfail', ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	h.finishAssignment(context.Background(), "asg-outcome-realfail", "", chatID, "asg-worker", wsID,
		"", "execution error: exec agent: context deadline exceeded", nil)

	var status, outcome, errMsg string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(outcome,''), COALESCE(error_message,'') FROM assignments WHERE id = 'asg-outcome-realfail'`,
	).Scan(&status, &outcome, &errMsg); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
	if outcome != orchestrator.OutcomeFailed {
		t.Errorf("outcome = %q, want %q", outcome, orchestrator.OutcomeFailed)
	}
	if errMsg != "execution error: exec agent: context deadline exceeded" {
		t.Errorf("error_message = %q, must keep the REAL failure reason, not the default", errMsg)
	}
}

// ── NO_CHANGE creates no inbox item ──────────────────────────────────────

func TestFinishAssignment_NoChangeOutcome_CreatesNoInboxItem(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-outcome-3', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES ('asg-outcome-nochange', ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	result := "Checked the logs, nothing new since last time.\n\n" +
		"---CHECKPOINT---\n" +
		"done: reviewed the last 24h of logs\n" +
		"next_step: check again tomorrow\n" +
		"confidence: high\n" +
		"outcome: NO_CHANGE\n" +
		"---END CHECKPOINT---\n"
	h.finishAssignment(context.Background(), "asg-outcome-nochange", "", chatID, "asg-worker", wsID, result, "", nil)

	var outcome string
	if err := h.db.QueryRow(
		`SELECT COALESCE(outcome,'') FROM assignments WHERE id = 'asg-outcome-nochange'`,
	).Scan(&outcome); err != nil {
		t.Fatalf("query outcome: %v", err)
	}
	if outcome != orchestrator.OutcomeNoChange {
		t.Fatalf("outcome = %q, want %q", outcome, orchestrator.OutcomeNoChange)
	}

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND source_id = 'asg-outcome-nochange'`,
		inbox.KindRunNeedsHuman).Scan(&n); err != nil {
		t.Fatalf("query inbox: %v", err)
	}
	if n != 0 {
		t.Errorf("inbox items = %d, want 0 (§12: NO_CHANGE never creates an item)", n)
	}
}

// ── NEEDS_HUMAN creates exactly one item with a valid action contract ───

func TestFinishAssignment_NeedsHumanOutcome_CreatesExactlyOneInboxItemWithActionContract(t *testing.T) {
	f := setupMentionFixture(t)
	sessionID := "sess_needs_human"
	assignmentID := "asg_needs_human"
	seedSession(t, f, sessionID, "active")
	seedSessionAssignment(t, f, assignmentID, sessionID, "RUNNING")
	execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET active_run_id = ? WHERE id = ?`, assignmentID, sessionID)
	// The mission's owner (Track A10) — createOutcomeInboxItem should
	// address the item at them rather than a blanket role.
	execOrFatal(t, f.db, `UPDATE missions SET owner_user_id = ? WHERE id = ?`, f.userID, f.missionID)

	result := "Ready to deploy but I don't have the staging credential.\n\n" +
		"---CHECKPOINT---\n" +
		"done: built and tested the change locally\n" +
		"blockers: missing the staging deploy credential\n" +
		"next_step: deploy once the credential is provided\n" +
		"confidence: high\n" +
		"outcome: NEEDS_HUMAN\n" +
		"---END CHECKPOINT---\n"
	f.assign.finishAssignment(context.Background(), assignmentID, "", f.missionID, "target-agent", f.wsID, result, "", nil)

	var outcome string
	if err := f.db.QueryRow(
		`SELECT COALESCE(outcome,'') FROM assignments WHERE id = ?`, assignmentID,
	).Scan(&outcome); err != nil {
		t.Fatalf("query outcome: %v", err)
	}
	if outcome != orchestrator.OutcomeNeedsHuman {
		t.Fatalf("outcome = %q, want %q", outcome, orchestrator.OutcomeNeedsHuman)
	}

	// Exactly one item (§18 scenario 15).
	var n int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND source_id = ?`,
		inbox.KindRunNeedsHuman, assignmentID).Scan(&n); err != nil {
		t.Fatalf("query inbox count: %v", err)
	}
	if n != 1 {
		t.Fatalf("inbox items = %d, want exactly 1", n)
	}

	// The item carries a valid §12 action contract. attention_class/
	// thread_key/actions are real columns now (B10, #2364) — payload only
	// still carries who_can_act/context, which have no dedicated column.
	var payloadRaw, targetUserID, targetRole, blocking, bodyMD, attentionClass, threadKey, actionsRaw string
	if err := f.db.QueryRow(
		`SELECT payload_json, COALESCE(target_user_id,''), COALESCE(target_role,''), blocking, COALESCE(body_md,''),
		        COALESCE(attention_class,''), COALESCE(thread_key,''), actions_json
		   FROM inbox_items WHERE kind = ? AND source_id = ?`,
		inbox.KindRunNeedsHuman, assignmentID,
	).Scan(&payloadRaw, &targetUserID, &targetRole, &blocking, &bodyMD, &attentionClass, &threadKey, &actionsRaw); err != nil {
		t.Fatalf("query inbox row: %v", err)
	}
	// The card's body must come from the run's own checkpoint (its
	// blockers, here) — not the generic fallback and not empty.
	if !strings.Contains(bodyMD, "missing the staging deploy credential") {
		t.Errorf("body_md = %q, want it to carry the checkpoint's blockers", bodyMD)
	}
	if blocking != "1" {
		t.Errorf("blocking = %q, want 1 (a NEEDS_HUMAN item requires action)", blocking)
	}
	if targetUserID != f.userID {
		t.Errorf("target_user_id = %q, want the issue owner %q", targetUserID, f.userID)
	}
	if targetRole != "" {
		t.Errorf("target_role = %q, want empty (owner takes precedence over a role)", targetRole)
	}

	var payload struct {
		WhoCanAct []string       `json:"who_can_act"`
		Context   map[string]any `json:"context"`
	}
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v; raw=%s", err, payloadRaw)
	}
	var actions []map[string]any
	if err := json.Unmarshal([]byte(actionsRaw), &actions); err != nil {
		t.Fatalf("unmarshal actions: %v; raw=%s", err, actionsRaw)
	}
	if attentionClass != "input" {
		t.Errorf("attention_class = %q, want %q", attentionClass, "input")
	}
	if threadKey == "" {
		t.Error("thread_key is empty, want a server-side thread key")
	}
	if len(payload.WhoCanAct) == 0 {
		t.Error("who_can_act is empty, want at least one entry")
	}
	if len(actions) == 0 {
		t.Fatal("actions is empty, want at least one actionable action")
	}
	for _, a := range actions {
		if a["id"] == "" || a["id"] == nil {
			t.Errorf("action missing id: %+v", a)
		}
		if a["label"] == "" || a["label"] == nil {
			t.Errorf("action missing label: %+v", a)
		}
	}
	if payload.Context["issue"] == nil {
		t.Error("context.issue is missing")
	}

	// The session moved to awaiting_input — the transition B4 could not
	// reach without outcome (issue_session_state.go's own doc comment).
	state, _ := sessionState(t, f, sessionID)
	if state != "awaiting_input" {
		t.Errorf("session state = %q, want awaiting_input", state)
	}
}

// A retried/duplicate call must not double the card — inbox.Insert's
// (kind, source_id) unique index, keyed on the assignment id, is what
// keeps "exactly one" true even under a second write attempt.
func TestCreateOutcomeInboxItem_SecondCallForSameRun_StaysExactlyOne(t *testing.T) {
	f := setupMentionFixture(t)
	sessionID := "sess_needs_human_dup"
	assignmentID := "asg_needs_human_dup"
	seedSession(t, f, sessionID, "active")
	seedSessionAssignment(t, f, assignmentID, sessionID, "COMPLETED")

	f.assign.createOutcomeInboxItem(context.Background(), assignmentID, f.wsID, "target-agent", "blocked, need a decision")
	f.assign.createOutcomeInboxItem(context.Background(), assignmentID, f.wsID, "target-agent", "blocked, need a decision")

	var n int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND source_id = ?`,
		inbox.KindRunNeedsHuman, assignmentID).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Errorf("inbox items after two calls = %d, want 1", n)
	}
}

// ── outcome reaches through the HANDOFF block too (mission-task path) ───

func TestFinishAssignment_HandoffOutcome_AlsoParsed(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-outcome-4', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES ('asg-outcome-handoff', ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	result := "Updated the doc.\n\n" +
		"---HANDOFF---\n" +
		"summary: Updated the onboarding doc with the new steps.\n" +
		"confidence: high\n" +
		"artifacts: docs/onboarding.md\n" +
		"outcome: WORK_CREATED\n" +
		"---END HANDOFF---\n"
	h.finishAssignment(context.Background(), "asg-outcome-handoff", "", chatID, "asg-worker", wsID, result, "", nil)

	var outcome string
	if err := h.db.QueryRow(
		`SELECT COALESCE(outcome,'') FROM assignments WHERE id = 'asg-outcome-handoff'`,
	).Scan(&outcome); err != nil {
		t.Fatalf("query: %v", err)
	}
	if outcome != orchestrator.OutcomeWorkCreated {
		t.Errorf("outcome = %q, want %q", outcome, orchestrator.OutcomeWorkCreated)
	}
}

// ── a Tier 1 stop overrides any self-reported outcome ────────────────────

func TestFinishAssignment_CancelledRun_OutcomeIsCancelledRegardlessOfHandoff(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, cancel_requested_at, created_at)
		VALUES ('asg-outcome-cancel', ?, ?, ?, ?, 'task', 'RUNNING', datetime('now'), datetime('now'))`,
		wsID, chatID, leadID, workerID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	_ = crewID

	result := "---CHECKPOINT---\ndone: partial work\nnext_step: resume\nconfidence: low\noutcome: NEEDS_HUMAN\n---END CHECKPOINT---\n"
	h.finishAssignment(context.Background(), "asg-outcome-cancel", "", chatID, "asg-worker", wsID, result, "", nil)

	var status, outcome string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(outcome,'') FROM assignments WHERE id = 'asg-outcome-cancel'`,
	).Scan(&status, &outcome); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "CANCELLED" {
		t.Fatalf("status = %q, want CANCELLED", status)
	}
	if outcome != orchestrator.OutcomeCancelled {
		t.Errorf("outcome = %q, want %q (Tier 1 stop wins over any self-reported outcome)", outcome, orchestrator.OutcomeCancelled)
	}
}
