package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// seedMissionAssignment wires a mission task to an assignment (the agent
// task-run) the way the mission engine does, so ListRuns has something to
// join: mission_tasks.assignment_id → assignments.
func seedMissionAssignment(t *testing.T, h *IssueHandler, wsID, missionID, agentID, assignID, status, result, started, finished string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	chatID := "chat-" + assignID
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission', 'MISSION', 'ACTIVE', ?, ?, ?)`,
		chatID, agentID, wsID, now, now, now); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id,
		    task, status, started_at, finished_at, result_summary, created_at)
		VALUES (?, ?, ?, ?, ?, 'Do the work', ?, ?, ?, ?, ?)`,
		assignID, wsID, chatID, agentID, agentID, status, started, finished, result, now); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status,
		    task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES (?, ?, ?, 'Task', '', ?, 1, '[]', ?, ?, ?)`,
		"mt-"+assignID, missionID, agentID, status, assignID, now, now); err != nil {
		t.Fatalf("seed mission_task: %v", err)
	}
}

func issueRunsRequest(t *testing.T, userID, wsID, crewID, ident string) *http.Request {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/issues/"+ident+"/runs", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", ident)
	return req
}

// TestIssueRuns_UnknownIssue_Returns404 — an identifier with no mission row
// 404s rather than returning an empty list.
func TestIssueRuns_UnknownIssue_Returns404(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)
	rr := httptest.NewRecorder()
	h.ListRuns(rr, issueRunsRequest(t, userID, wsID, crewID, "ENG-999"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestIssueRuns_ReturnsAssignmentRuns — the issue's agent task-runs come
// back (joined via mission_tasks → assignments), newest-first, with agent
// name + duration + result. A run from another issue must NOT leak in.
func TestIssueRuns_ReturnsAssignmentRuns(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	m1 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	m2 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "IN_PROGRESS")

	// Two runs for ENG-1 (distinct created order via started times) + one
	// for ENG-2 which must not leak.
	seedMissionAssignment(t, h, wsID, m1, workerID, "asg_a", "COMPLETED", "wrote report",
		"2026-06-01T10:00:00Z", "2026-06-01T10:00:30Z")
	seedMissionAssignment(t, h, wsID, m1, leadID, "asg_b", "FAILED", "",
		"2026-06-01T11:00:00Z", "2026-06-01T11:00:05Z")
	seedMissionAssignment(t, h, wsID, m2, workerID, "asg_c", "COMPLETED", "other issue",
		"2026-06-01T12:00:00Z", "2026-06-01T12:00:10Z")

	rr := httptest.NewRecorder()
	h.ListRuns(rr, issueRunsRequest(t, userID, wsID, crewID, "ENG-1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got []issueRunDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (ENG-1 runs only); body=%s", len(got), rr.Body.String())
	}
	for _, run := range got {
		if run.ID == "asg_c" {
			t.Fatalf("ENG-2's run leaked into ENG-1 list")
		}
		if run.AgentName == "" {
			t.Fatalf("agent_name not resolved: %+v", run)
		}
	}
	// The COMPLETED run carries a computed duration (30s) + result summary.
	var completed *issueRunDTO
	for i := range got {
		if got[i].ID == "asg_a" {
			completed = &got[i]
		}
	}
	if completed == nil {
		t.Fatalf("asg_a missing from results")
	}
	if completed.DurationMs != 30000 {
		t.Fatalf("duration = %d ms, want 30000", completed.DurationMs)
	}
	if completed.ResultSummary != "wrote report" {
		t.Fatalf("result_summary = %q, want 'wrote report'", completed.ResultSummary)
	}
}
