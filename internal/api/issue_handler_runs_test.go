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
		// seedMissionAssignment leaves a.mission_id NULL on purpose (the
		// pre-#2256 row shape, found only via the mission_tasks join), so
		// source must still resolve to "task" from that join alone, with
		// mission_id honestly reported as unknown rather than invented.
		if run.Source != "task" {
			t.Errorf("run %s: source = %q, want %q (found via mission_tasks join)", run.ID, run.Source, "task")
		}
		if run.MissionID != nil {
			t.Errorf("run %s: mission_id = %q, want nil — a.mission_id was never stamped on this row", run.ID, *run.MissionID)
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

// seedMentionAssignment wires an assignment to a comment mention the way a
// real @mention dispatch does (issue_mentions.go): a mission_comments row,
// a mission_comment_mentions row naming it, and the assignment itself
// carrying a.mission_id (#2256 — a mention dispatch has no mission_tasks row
// at all, so a.mission_id is the ONLY thing that attributes it to the
// issue).
func seedMentionAssignment(t *testing.T, h *IssueHandler, wsID, missionID, agentID, assignID, status, result, started, finished string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	chatID := "chat-" + assignID
	commentID := "comment-" + assignID
	execOrFatal(t, h.db, `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission', 'MISSION', 'ACTIVE', ?, ?, ?)`,
		chatID, agentID, wsID, now, now, now)
	execOrFatal(t, h.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id,
		    task, status, started_at, finished_at, result_summary, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'Do the work', ?, ?, ?, ?, ?, ?)`,
		assignID, wsID, chatID, agentID, agentID, status, started, finished, result, now, missionID)
	execOrFatal(t, h.db, `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES (?, ?, 'user', ?, 'over to you @worker', ?, ?)`,
		commentID, missionID, agentID, now, now)
	execOrFatal(t, h.db, `
		INSERT INTO mission_comment_mentions (id, workspace_id, mission_id, comment_id, agent_id,
		    dispatch_state, assignment_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'dispatched', ?, ?)`,
		"mcm-"+assignID, wsID, missionID, commentID, agentID, assignID, now)
}

// seedDelegationAssignment wires a bare assignment carrying only
// a.mission_id — a delegation hop (a sub-agent that called /assign further,
// mid-mission), which has neither a mission_tasks row nor a
// mission_comment_mentions row naming it (assignments_mission_derive_test.go
// covers where a.mission_id itself comes from; this seeds the row directly).
func seedDelegationAssignment(t *testing.T, h *IssueHandler, wsID, missionID, agentID, assignID, status, result, started, finished string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	chatID := "chat-" + assignID
	execOrFatal(t, h.db, `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission', 'MISSION', 'ACTIVE', ?, ?, ?)`,
		chatID, agentID, wsID, now, now, now)
	execOrFatal(t, h.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id,
		    task, status, started_at, finished_at, result_summary, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'Do the work', ?, ?, ?, ?, ?, ?)`,
		assignID, wsID, chatID, agentID, agentID, status, started, finished, result, now, missionID)
}

// seedMissionTaskAssignmentWithMissionID is seedMissionAssignment's
// current-day sibling: mission_tasks.go (#2256, going forward) stamps
// a.mission_id on a task's own assignment too, not just on mention and
// delegation dispatches. seedMissionAssignment deliberately leaves
// a.mission_id NULL to keep covering the pre-#2256 row shape (found ONLY
// via the mission_tasks join); this variant covers the row shape every
// task-run has today.
func seedMissionTaskAssignmentWithMissionID(t *testing.T, h *IssueHandler, wsID, missionID, agentID, assignID, status, result, started, finished string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	chatID := "chat-" + assignID
	execOrFatal(t, h.db, `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission', 'MISSION', 'ACTIVE', ?, ?, ?)`,
		chatID, agentID, wsID, now, now, now)
	execOrFatal(t, h.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id,
		    task, status, started_at, finished_at, result_summary, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'Do the work', ?, ?, ?, ?, ?, ?)`,
		assignID, wsID, chatID, agentID, agentID, status, started, finished, result, now, missionID)
	execOrFatal(t, h.db, `
		INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status,
		    task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES (?, ?, ?, 'Task', '', ?, 1, '[]', ?, ?, ?)`,
		"mt-"+assignID, missionID, agentID, status, assignID, now, now)
}

// TestIssueRuns_MissionID_And_Source is the #2313-item-3 regression test:
// ListRuns' response must carry mission_id and source (task | mention |
// delegation) so a client can tell WHY a run is attributed to an issue,
// not just that it is. Three runs, one per attribution path, all on the
// same issue.
func TestIssueRuns_MissionID_And_Source(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	m1 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	seedMissionTaskAssignmentWithMissionID(t, h, wsID, m1, workerID, "asg_task", "COMPLETED", "task run",
		"2026-06-01T10:00:00Z", "2026-06-01T10:00:30Z")
	seedMentionAssignment(t, h, wsID, m1, workerID, "asg_mention", "COMPLETED", "mention run",
		"2026-06-01T11:00:00Z", "2026-06-01T11:00:30Z")
	seedDelegationAssignment(t, h, wsID, m1, leadID, "asg_delegation", "COMPLETED", "delegation run",
		"2026-06-01T12:00:00Z", "2026-06-01T12:00:30Z")

	rr := httptest.NewRecorder()
	h.ListRuns(rr, issueRunsRequest(t, userID, wsID, crewID, "ENG-1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got []issueRunDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}

	want := map[string]string{
		"asg_task":       "task",
		"asg_mention":    "mention",
		"asg_delegation": "delegation",
	}
	seen := map[string]bool{}
	for _, run := range got {
		wantSource, ok := want[run.ID]
		if !ok {
			continue
		}
		seen[run.ID] = true
		if run.Source != wantSource {
			t.Errorf("run %s: source = %q, want %q", run.ID, run.Source, wantSource)
		}
		if run.MissionID == nil || *run.MissionID != m1 {
			t.Errorf("run %s: mission_id = %v, want %q", run.ID, run.MissionID, m1)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("run %s missing from results; body=%s", id, rr.Body.String())
		}
	}
}
