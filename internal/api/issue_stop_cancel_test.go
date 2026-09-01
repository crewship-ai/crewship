package api

// Coverage for PRD-ISSUES-AND-ROUTINES-2026 work package A1 ("Stop actually
// stops (Tier 1), and terminal states hold"):
//
//   - IssueHandler.Stop now stamps cancel_requested_at on the issue's live
//     (PENDING/RUNNING) assignment rows, in the same transaction as the
//     existing mission_tasks/missions CANCELLED writes.
//   - runAssignment (assignments_run.go) checks cancel_requested_at BEFORE
//     spending anything on the assignment — before the pre_task_delegation
//     hook, before flipping the row to RUNNING, before any container exec —
//     and finishes it as CANCELLED instead. This is the "a stopped run
//     starts no further step" behavior.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIssue_Stop_StampsCancelRequestedOnLiveAssignments(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	now := time.Now().UTC().Format(time.RFC3339)
	// assignments.chat_id is FK'd to chats(id). Mission dispatches satisfy
	// this via ensureMissionChat, which lazily inserts a synthetic chat row
	// keyed by the mission's own id (internal/orchestrator/mission_tasks.go)
	// before ChatID: ms.ID is ever written onto an assignment.
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission: ENG-1', 'MISSION', 'ACTIVE', ?, ?, ?)`,
		id, leadID, wsID, now, now, now); err != nil {
		t.Fatalf("seed mission chat: %v", err)
	}
	// A RUNNING assignment dispatched for this issue's task — mission
	// dispatches stamp both chat_id and group_id with the mission id (see
	// scheduleTask in internal/orchestrator/mission_tasks.go).
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at, started_at)
		VALUES ('a-running', ?, ?, ?, ?, 'do work', 'RUNNING', ?, ?, ?)`,
		wsID, id, leadID, workerID, id, now, now); err != nil {
		t.Fatalf("seed running assignment: %v", err)
	}
	// A terminal assignment from an earlier, already-finished task on the
	// same issue — must NOT be touched (it is not "live").
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at, finished_at)
		VALUES ('a-done', ?, ?, ?, ?, 'already done', 'COMPLETED', ?, ?, ?)`,
		wsID, id, leadID, workerID, id, now, now); err != nil {
		t.Fatalf("seed completed assignment: %v", err)
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Stop(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var cancelAt, status string
	if err := h.db.QueryRow(`SELECT COALESCE(cancel_requested_at,''), status FROM assignments WHERE id='a-running'`).Scan(&cancelAt, &status); err != nil {
		t.Fatalf("query a-running: %v", err)
	}
	if cancelAt == "" {
		t.Errorf("a-running.cancel_requested_at not stamped by Stop")
	}
	// Tier 1: no kill primitive, so the row's status itself is left alone —
	// the runner (runAssignment/finishAssignment) is what turns the flag
	// into a terminal write once the run actually finishes.
	if status != "RUNNING" {
		t.Errorf("a-running.status = %q, want unchanged RUNNING (Stop does not itself terminate the row)", status)
	}

	var doneCancelAt string
	if err := h.db.QueryRow(`SELECT COALESCE(cancel_requested_at,'') FROM assignments WHERE id='a-done'`).Scan(&doneCancelAt); err != nil {
		t.Fatalf("query a-done: %v", err)
	}
	if doneCancelAt != "" {
		t.Errorf("a-done.cancel_requested_at = %q, want empty (already-terminal rows must not be touched)", doneCancelAt)
	}

	var missionStatus string
	h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, id).Scan(&missionStatus)
	if missionStatus != "CANCELLED" {
		t.Errorf("mission status = %q, want CANCELLED", missionStatus)
	}
}

// TestRunAssignment_CancelRequested_StartsNoFurtherStep is the "a stopped
// run starts no further step" test: an assignment whose cancel_requested_at
// was already stamped (by Stop, before this dispatch goroutine got to run —
// the PENDING-but-not-yet-executing case) must not be flipped to RUNNING,
// must not reach the orchestrator, and must finish as CANCELLED.
func TestRunAssignment_CancelRequested_StartsNoFurtherStep(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)

	now := time.Now().UTC().Format(time.RFC3339)
	assignmentID := "a-precancelled"
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at, cancel_requested_at, cancel_reason)
		VALUES (?, ?, ?, ?, ?, 'do work', 'PENDING', ?, ?, 'issue stopped')`,
		assignmentID, wsID, chatID, leadID, workerID, now, now); err != nil {
		t.Fatalf("seed pre-cancelled assignment: %v", err)
	}

	body := createAssignmentBody{
		TargetSlug:  "asg-worker",
		Task:        "do work",
		CrewID:      crewID,
		WorkspaceID: wsID,
		ChatID:      chatID,
	}
	target := targetAgentInfo{
		ID: workerID, Slug: "asg-worker", Name: "Worker",
		CLIAdapter: "CLAUDE_CODE", CrewSlug: "asg",
	}

	// h.orch is nil in covAsgRig — if the early cancel-check did NOT short
	// circuit, runAssignment would still reach "if h.orch == nil" and finish
	// the row FAILED("orchestrator not available") having already flipped it
	// to RUNNING first. Asserting CANCELLED *and* started_at IS NULL below
	// tells the two apart: only the early check produces both.
	h.runAssignment(context.Background(), assignmentID, body, target)

	var status, errMsg string
	var startedAt, finishedAt *string
	if err := h.db.QueryRow(`SELECT status, COALESCE(error_message,''), started_at, finished_at FROM assignments WHERE id=?`, assignmentID).
		Scan(&status, &errMsg, &startedAt, &finishedAt); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "CANCELLED" {
		t.Fatalf("status = %q, want CANCELLED (errMsg=%q)", status, errMsg)
	}
	if startedAt != nil {
		t.Errorf("started_at = %q, want NULL — the run must never have been flipped to RUNNING", *startedAt)
	}
	if finishedAt == nil {
		t.Errorf("finished_at is NULL, want set — the assignment must have reached a terminal write")
	}
	if errMsg != "" {
		t.Errorf("error_message = %q, want empty — this is a cancellation, not a failure", errMsg)
	}

	// The resolver would only be consulted once dispatch actually tries to
	// build the run request (buildAssignmentRunRequest) — a step strictly
	// after the RUNNING flip. Confirming it was never asked to resolve
	// anything is a second, independent signal that no further step ran.
	if fake, ok := h.resolver.(*fakeAgentResolver); ok && fake.gotAgentID != "" {
		t.Errorf("agent resolver was consulted (agent_id=%q) — dispatch proceeded past the cancel check", fake.gotAgentID)
	}
}
