package api

// Coverage for assignments_run.go — Create's validation/lookup/insert
// pipeline, the runAssignment fallback when no orchestrator is wired, and
// finishAssignment's terminal-state bookkeeping including the
// mission-linked completion comment.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// covAsgRig: workspace + crew + 2 agents (lead assigns to worker) + chat
// anchored on the lead.
func covAsgRig(t *testing.T) (h *AssignmentHandler, wsID, crewID, leadID, workerID, chatID string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "crew-asg", wsID, "ASG", "asg")
	leadID = seedAgentRow(t, db, "agent-asg-lead", wsID, crewID, "Lead", "asg-lead", "LEAD")
	workerID = seedAgentRow(t, db, "agent-asg-worker", wsID, crewID, "Worker", "asg-worker", "AGENT")
	chatID = "chat-asg"
	if _, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'asg', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, leadID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h = NewAssignmentHandler(db, nil, nil, "internal-test-token", newTestLogger())
	return
}

func covAsgPost(t *testing.T, h *AssignmentHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/assignments", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestAssignmentCreateCov_Validation(t *testing.T) {
	h, _, _, _, _, _ := covAsgRig(t)

	t.Run("invalid json", func(t *testing.T) {
		if rr := covAsgPost(t, h, "{nope"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("missing fields", func(t *testing.T) {
		if rr := covAsgPost(t, h, `{"target_slug":"x"}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

func TestAssignmentCreateCov_ChatNotFound(t *testing.T) {
	h, wsID, crewID, _, _, _ := covAsgRig(t)
	rr := covAsgPost(t, h, `{"target_slug":"asg-worker","task":"t","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"ghost-chat"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAssignmentCreateCov_TargetAgentNotFound(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covAsgRig(t)
	rr := covAsgPost(t, h, `{"target_slug":"no-such-slug","task":"t","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAssignmentCreateCov_CrossCrewWithoutConnection403(t *testing.T) {
	h, wsID, _, _, _, chatID := covAsgRig(t)
	otherCrew := seedCrewRow(t, h.db, "crew-asg-other", wsID, "Other", "asg-other")
	seedAgentRow(t, h.db, "agent-asg-foreign", wsID, otherCrew, "F", "asg-foreign", "AGENT")
	rr := covAsgPost(t, h, `{"target_slug":"asg-foreign","task":"t","crew_id":"`+otherCrew+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not connected") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestAssignmentCreateCov_HappyPath_NoOrchestrator(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	longTask := strings.Repeat("T", 150) // exercises the summary truncation
	rr := covAsgPost(t, h, `{"target_slug":"asg-worker","task":"`+longTask+`","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"status":"PENDING"`) {
		t.Errorf("body = %q", rr.Body.String())
	}

	// Row landed attributed to the right agents.
	var byID, toID, status string
	if err := h.db.QueryRow(
		`SELECT assigned_by_id, assigned_to_id, status FROM assignments WHERE chat_id = ?`, chatID).
		Scan(&byID, &toID, &status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if byID != leadID || toID != workerID {
		t.Errorf("by=%q to=%q", byID, toID)
	}

	// The spawned runAssignment goroutine has no orchestrator → the
	// assignment must converge to FAILED. Poll briefly.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := h.db.QueryRow(`SELECT status FROM assignments WHERE chat_id = ?`, chatID).Scan(&status); err != nil {
			t.Fatalf("poll: %v", err)
		}
		if status == "FAILED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("assignment never failed (status=%s)", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var errMsg string
	if err := h.db.QueryRow(`SELECT COALESCE(error_message,'') FROM assignments WHERE chat_id = ?`, chatID).Scan(&errMsg); err != nil {
		t.Fatalf("query error_message: %v", err)
	}
	if !strings.Contains(errMsg, "orchestrator not available") {
		t.Errorf("error_message = %q", errMsg)
	}
}

func TestAssignmentCreateCov_MissionLinked_CommentAndAssignee(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	// A missions row whose id == chat_id activates the issue-mirroring path.
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-1', 'Mission X', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}

	rr := covAsgPost(t, h, `{"target_slug":"asg-worker","task":"build the thing","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var commentBody string
	if err := h.db.QueryRow(
		`SELECT body FROM mission_comments WHERE mission_id = ? AND author_id = ?`, chatID, leadID).
		Scan(&commentBody); err != nil {
		t.Fatalf("query mission comment: %v", err)
	}
	if !strings.Contains(commentBody, "assigned work to Worker") {
		t.Errorf("comment = %q", commentBody)
	}
	var assigneeID, assigneeType string
	if err := h.db.QueryRow(
		`SELECT COALESCE(assignee_id,''), COALESCE(assignee_type,'') FROM missions WHERE id = ?`, chatID).
		Scan(&assigneeID, &assigneeType); err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if assigneeID != workerID || assigneeType != "agent" {
		t.Errorf("assignee = %q/%q, want %q/agent", assigneeID, assigneeType, workerID)
	}
	var activityAction string
	if err := h.db.QueryRow(
		`SELECT action FROM mission_activity WHERE mission_id = ?`, chatID).Scan(&activityAction); err != nil {
		t.Fatalf("query activity: %v", err)
	}
	if activityAction != "assignee_changed" {
		t.Errorf("activity = %q", activityAction)
	}
}

// ---- runAssignment / finishAssignment (called synchronously) ----

func TestRunAssignment_NoOrchestrator_FailsAssignment(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	insertAssignment(t, h.db, "asg-run-1", wsID, chatID, leadID, workerID, "PENDING")

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	h.runAssignment(context.Background(), "asg-run-1", body, target, nil)

	var status, errMsg string
	var started, finished string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(error_message,''), COALESCE(started_at,''), COALESCE(finished_at,'')
		 FROM assignments WHERE id = 'asg-run-1'`).Scan(&status, &errMsg, &started, &finished); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" || errMsg != "orchestrator not available" {
		t.Errorf("status=%q err=%q", status, errMsg)
	}
	if started == "" || finished == "" {
		t.Errorf("timestamps not set: started=%q finished=%q", started, finished)
	}
}

// A mission (issue) dispatch carries body.MissionID; the run-scoped journal
// entries (run.started + assignment.running) must stamp it so a client can
// fetch the issue's full run timeline via `?mission_id={missionID}`.
func TestRunAssignment_MissionScoped_StampsMissionID(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)

	// A real missions row is the FK target for journal_entries.mission_id.
	missionID := "mission-asg-1"
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-m', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	insertAssignment(t, h.db, "asg-run-m", wsID, chatID, leadID, workerID, "PENDING")

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID,
		ChatID: chatID, MissionID: missionID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	// No orchestrator → fails fast, but run.started + assignment.running emit first.
	h.runAssignment(context.Background(), "asg-run-m", body, target, nil)

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	for _, et := range []string{"run.started", "assignment.running"} {
		var mid string
		if err := h.db.QueryRow(
			`SELECT COALESCE(mission_id,'') FROM journal_entries WHERE entry_type = ? AND workspace_id = ?`,
			et, wsID).Scan(&mid); err != nil {
			t.Fatalf("query %s: %v", et, err)
		}
		if mid != missionID {
			t.Errorf("%s mission_id = %q, want %q", et, mid, missionID)
		}
	}
}

// A chat-only run (lead's curl /assign) has no missions row and an empty
// body.MissionID. run.started must still persist — nullable() stores NULL, so
// the missions FK is never tripped — and mission_id stays empty.
func TestRunAssignment_ChatOnly_NoMissionID_NoFKViolation(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)
	insertAssignment(t, h.db, "asg-run-chat", wsID, chatID, leadID, workerID, "PENDING")

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
		// MissionID intentionally empty — chat-only run, no missions row.
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	h.runAssignment(context.Background(), "asg-run-chat", body, target, nil)

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var mid string
	if err := h.db.QueryRow(
		`SELECT COALESCE(mission_id,'') FROM journal_entries WHERE entry_type='run.started' AND workspace_id=?`,
		wsID).Scan(&mid); err != nil {
		t.Fatalf("run.started must persist with no FK violation: %v", err)
	}
	if mid != "" {
		t.Errorf("chat-only run.started mission_id = %q, want empty", mid)
	}
}

func TestRunAssignment_ContainerError_FailsAssignment(t *testing.T) {
	// A real orchestrator without a container provider: runAssignment gets
	// past the nil-orch gate and fails at GetOrCreateContainer instead.
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	h.orch = orchestrator.New(nil, nil, newTestLogger())
	insertAssignment(t, h.db, "asg-run-2", wsID, chatID, leadID, workerID, "PENDING")

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}
	target := targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"}
	h.runAssignment(context.Background(), "asg-run-2", body, target, nil)

	var status, errMsg string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = 'asg-run-2'`).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" || !strings.Contains(errMsg, "container error") {
		t.Errorf("status=%q err=%q", status, errMsg)
	}
}

func TestFinishAssignment_CompletedWithMissionComment(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	_ = crewID
	// missions row matching group_id (chat_id) and NO mission_tasks link →
	// the completion-comment block fires.
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-2', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES ('asg-fin-1', ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	h.finishAssignment(context.Background(), "asg-fin-1", "", chatID, "asg-worker", wsID,
		"the work result", "")

	var status, result string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(result_summary,'') FROM assignments WHERE id = 'asg-fin-1'`).
		Scan(&status, &result); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "COMPLETED" || result != "the work result" {
		t.Errorf("status=%q result=%q", status, result)
	}
	var commentBody string
	if err := h.db.QueryRow(
		`SELECT body FROM mission_comments WHERE mission_id = ? AND author_id = ?`, chatID, workerID).
		Scan(&commentBody); err != nil {
		t.Fatalf("query comment: %v", err)
	}
	if !strings.Contains(commentBody, "completed their work") || !strings.Contains(commentBody, "the work result") {
		t.Errorf("comment = %q", commentBody)
	}
	var action string
	if err := h.db.QueryRow(
		`SELECT action FROM mission_activity WHERE mission_id = ?`, chatID).Scan(&action); err != nil {
		t.Fatalf("query activity: %v", err)
	}
	if action != "task_completed" {
		t.Errorf("action = %q, want task_completed", action)
	}
}

func TestFinishAssignment_FailedWritesIssueComment(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg-3', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES ('asg-fin-2', ?, ?, ?, ?, 'task', 'RUNNING', ?, datetime('now'))`,
		wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	h.finishAssignment(context.Background(), "asg-fin-2", "", chatID, "asg-worker", wsID,
		"", "container exploded")

	var status, errMsg string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = 'asg-fin-2'`).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" || errMsg != "container exploded" {
		t.Errorf("status=%q err=%q", status, errMsg)
	}
	var commentBody string
	if err := h.db.QueryRow(
		`SELECT body FROM mission_comments WHERE mission_id = ?`, chatID).Scan(&commentBody); err != nil {
		t.Fatalf("query comment: %v", err)
	}
	if !strings.Contains(commentBody, "encountered an issue") || !strings.Contains(commentBody, "container exploded") {
		t.Errorf("comment = %q", commentBody)
	}
	var action string
	if err := h.db.QueryRow(
		`SELECT action FROM mission_activity WHERE mission_id = ?`, chatID).Scan(&action); err != nil {
		t.Fatalf("query activity: %v", err)
	}
	if action != "task_failed" {
		t.Errorf("action = %q, want task_failed", action)
	}
}
