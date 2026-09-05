package api

// issue_terminal_children_test.go — §10.4's terminal-children rule (fixes
// F10, work package B11, #2368), and golden scenario 8 (§18): "Parent issue
// with an open child → cannot be marked DONE without force; force writes a
// receipt."
//
// RED-FIRST: before issue_terminal_children.go and the Update handler's
// gating existed, PATCHing a parent to DONE over an open sub-issue or an
// open mission_task succeeded unconditionally — sub_issues_count was
// display-only (issue_handler.go's own comment says so) and nothing ever
// read parent_issue_id before a status write. Every "blocked" test below
// failed against that code (200, not 409) before this PR.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssue_Update_TerminalTransition_BlockedByOpenSubIssue(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-50", "IN_PROGRESS")
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-51", "TODO")
	if _, err := h.db.ExecContext(context.Background(), `UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("link child: %v", err)
	}

	body := bytes.NewBufferString(`{"status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-50")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ENG-51") {
		t.Errorf("409 body does not name the open child: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "force=true") {
		t.Errorf("409 body does not mention the force override: %s", rr.Body.String())
	}

	// The parent's status must NOT have moved.
	var status string
	if err := h.db.QueryRowContext(context.Background(), `SELECT status FROM missions WHERE id = ?`, parentID).Scan(&status); err != nil {
		t.Fatalf("read parent status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("parent status = %q, want unchanged IN_PROGRESS", status)
	}
}

func TestIssue_Update_TerminalTransition_BlockedByOpenMissionTask(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-52", "IN_PROGRESS")
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO mission_tasks (id, mission_id, title, status, created_at, updated_at)
		 VALUES ('task_open_1', ?, 'Write the migration', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		parentID); err != nil {
		t.Fatalf("seed mission task: %v", err)
	}

	body := bytes.NewBufferString(`{"status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-52")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Write the migration") {
		t.Errorf("409 body does not name the open task: %s", rr.Body.String())
	}
}

func TestIssue_Update_TerminalTransition_ForceSucceedsAndWritesReceipt(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-53", "IN_PROGRESS")
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-54", "IN_PROGRESS")
	if _, err := h.db.ExecContext(context.Background(), `UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("link child: %v", err)
	}

	body := bytes.NewBufferString(`{"status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/?force=true", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-53")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with ?force=true; body=%s", rr.Code, rr.Body.String())
	}
	var resp issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "DONE" {
		t.Errorf("status = %q, want DONE", resp.Status)
	}

	// The receipt: a mission_activity row (already hash-chained into the
	// journal via issueEvents.log — F42) whose payload names the actor
	// (ActorType/ActorID, standard on every issueEvent) and the open
	// child it forced past.
	var payloadJSON string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM mission_activity WHERE mission_id = ? AND action = 'status_changed' ORDER BY seq DESC LIMIT 1`,
		parentID).Scan(&payloadJSON); err != nil {
		t.Fatalf("read receipt row: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if forced, _ := payload["forced"].(bool); !forced {
		t.Errorf("receipt payload forced = %v, want true: %s", payload["forced"], payloadJSON)
	}
	openChildren, _ := payload["open_children"].([]any)
	if len(openChildren) != 1 || !strings.Contains(openChildren[0].(string), "ENG-54") {
		t.Errorf("receipt payload open_children = %v, want it to name ENG-54", payload["open_children"])
	}

	// actor_id on the SAME row names WHO forced it.
	var actorID string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT actor_id FROM mission_activity WHERE mission_id = ? AND action = 'status_changed' ORDER BY seq DESC LIMIT 1`,
		parentID).Scan(&actorID); err != nil {
		t.Fatalf("read receipt actor: %v", err)
	}
	if actorID != userID {
		t.Errorf("receipt actor_id = %q, want %q (who forced it)", actorID, userID)
	}
}

func TestIssue_Update_TerminalTransition_AllowedWhenChildrenAreTerminal(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-55", "IN_PROGRESS")
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-56", "DONE")
	if _, err := h.db.ExecContext(context.Background(), `UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("link child: %v", err)
	}
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO mission_tasks (id, mission_id, title, status, created_at, updated_at)
		 VALUES ('task_done_1', ?, 'Already finished', 'COMPLETED', datetime('now'), datetime('now'))`,
		parentID); err != nil {
		t.Fatalf("seed mission task: %v", err)
	}

	body := bytes.NewBufferString(`{"status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-55")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (children already terminal); body=%s", rr.Code, rr.Body.String())
	}

	// No forced receipt when nothing was forced.
	var payloadJSON string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(payload_json, '{}') FROM mission_activity WHERE mission_id = ? AND action = 'status_changed' ORDER BY seq DESC LIMIT 1`,
		parentID).Scan(&payloadJSON); err != nil {
		t.Fatalf("read event row: %v", err)
	}
	if strings.Contains(payloadJSON, `"forced"`) {
		t.Errorf("payload should not claim a force that never happened: %s", payloadJSON)
	}
}

func TestIssue_Update_TerminalTransition_NoOpTransitionNotGated(t *testing.T) {
	// Setting status to the SAME value it already is must not trip the
	// guard — the transition validator itself already treats a same-status
	// PATCH as a normal no-op field set elsewhere; a spurious 409 here
	// would be its own regression.
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-57", "DONE")
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-58", "IN_PROGRESS")
	if _, err := h.db.ExecContext(context.Background(), `UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("link child: %v", err)
	}

	body := bytes.NewBufferString(`{"priority":"high"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-57")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no status field in this PATCH at all); body=%s", rr.Code, rr.Body.String())
	}
}

// TestOpenChildBlockers_WorksInsideATransaction pins the interface change
// (review on #2377) that closes the check-then-act race between reading
// "no open children" and writing the terminal-status UPDATE: openChildBlockers
// must run identically against a *sql.Tx as it does against *sql.DB, because
// issue_handler_update.go's Update re-runs this SAME check inside the very
// transaction that performs the write — SQLite's immediate-lock semantics
// (database.Open's `_txlock=immediate`) then make the read-then-write atomic
// against a concurrent writer, the same property missionactivity.Emit's seq
// allocation already relies on. A regression that narrowed openChildBlockers'
// parameter back to *sql.DB would make this call site fail to compile.
func TestOpenChildBlockers_WorksInsideATransaction(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-60", "IN_PROGRESS")
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-61", "TODO")
	if _, err := h.db.ExecContext(context.Background(), `UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("link child: %v", err)
	}

	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	blockers, err := openChildBlockers(context.Background(), tx, parentID)
	if err != nil {
		t.Fatalf("openChildBlockers via *sql.Tx: %v", err)
	}
	if len(blockers) != 1 || !strings.Contains(blockers[0], "ENG-61") {
		t.Errorf("blockers via tx = %v, want exactly one naming ENG-61", blockers)
	}
}

// TestIssue_Update_TerminalTransition_ForceSkipsTheAdvisoryPreCheck proves
// the transactional re-check — not the advisory pre-check — is the actual
// enforcement point when ?force=true is set: the pre-check branch is
// skipped entirely whenever force is requested (issue_handler_update.go),
// so the ONLY code path that can see the open child at all here is the
// one running inside the transaction alongside the write.
func TestIssue_Update_TerminalTransition_ForceSkipsTheAdvisoryPreCheck(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-62", "IN_PROGRESS")
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-63", "TODO")
	if _, err := h.db.ExecContext(context.Background(), `UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("link child: %v", err)
	}

	body := bytes.NewBufferString(`{"status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/?force=true", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-62")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRowContext(context.Background(), `SELECT status FROM missions WHERE id = ?`, parentID).Scan(&status); err != nil {
		t.Fatalf("read parent status: %v", err)
	}
	if status != "DONE" {
		t.Errorf("parent status = %q, want DONE", status)
	}
}
