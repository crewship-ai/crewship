package api

// Coverage for the gap left after #2256 (assignments.mission_id): mission
// task dispatch, lead planning, and mention dispatch all stamp mission_id on
// the assignment they create, but the shared /assign door
// (AssignmentHandler.Create, the handler behind POST
// /api/v1/internal/assignments) only reads mission_id from the
// client-supplied body — and neither caller that can reach it mid-mission
// supplies one:
//
//   - the sidecar's handleAssign (internal/sidecar/assignment.go) builds a
//     body with target_slug/task/crew_id/workspace_id/chat_id/actor_agent_id
//     and no mission_id
//   - the routine dispatcher's crewshipBody (internal/api/crewship_actions.go)
//     sets workspace_id and crew_id only
//
// So every delegation hop — a sub-agent calling /assign while it is itself
// running inside a mission task or a mention-dispatched run — created a row
// with mission_id = NULL, and "every run for this issue" (issue_handler_runs
// .go's ListRuns) still missed exactly those hops.
//
// The fix derives mission_id server-side, in Create, from chat_id: every
// synthetic mission chat on this schema is created with the mission's own id
// as the chat's primary key (ensureMissionChat here and in
// internal/orchestrator/mission_tasks.go, plus four more call sites), so
// "does a missions row exist at this chat_id" is the exact FK precondition
// assignments.mission_id needs, not a heuristic.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAssignmentCreate_DerivesMissionIDFromChatID is the gap test: a
// delegated /assign made from inside a mission chat (chat_id names a real
// missions row) must land mission_id on the new row even though the request
// body — shaped exactly like the sidecar's handleAssign — never supplies
// one.
func TestAssignmentCreate_DerivesMissionIDFromChatID(t *testing.T) {
	h, wsID, crewID, leadID, _, _ := covAsgRig(t)

	missionID := "msn-derive-1"
	execOrFatal(t, h.db, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-derive-1', 'Derive test', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID)
	execOrFatal(t, h.db, `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission: derive', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		missionID, leadID, wsID)

	// Shaped exactly like the sidecar's handleAssign / the routine
	// dispatcher's crewshipBody: chat_id set, mission_id deliberately
	// omitted — this is the request body every real delegation hop sends
	// today.
	body := `{"target_slug":"asg-worker","task":"sub-task","crew_id":"` + crewID +
		`","workspace_id":"` + wsID + `","chat_id":"` + missionID + `"}`
	rr := covAsgPost(t, h, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		AssignmentID string `json:"assignment_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
	}

	var gotMissionID sql.NullString
	if err := h.db.QueryRow(`SELECT mission_id FROM assignments WHERE id = ?`, resp.AssignmentID).
		Scan(&gotMissionID); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if !gotMissionID.Valid || gotMissionID.String != missionID {
		t.Errorf("assignments.mission_id = %v, want %q — a delegation hop made from inside a mission "+
			"chat must be attributable to that mission (issue_handler_runs.go's ListRuns)", gotMissionID, missionID)
	}
}

// TestAssignmentCreate_ChatOnlyAssignmentStaysUnattributed proves the other
// half: an assignment dispatched from an ordinary (non-mission) chat must
// NOT be falsely attributed to a mission. Uses covAsgRig's default chat,
// which is deliberately seeded MISSION-mode with no backing missions row —
// exactly the orphan shape a mode-only predicate (chats.mode = 'MISSION')
// would mis-derive from. The existence check must see through it.
func TestAssignmentCreate_ChatOnlyAssignmentStaysUnattributed(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covAsgRig(t)

	var missionRowCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id = ?`, chatID).Scan(&missionRowCount); err != nil {
		t.Fatalf("sanity query: %v", err)
	}
	if missionRowCount != 0 {
		t.Fatalf("test fixture assumption broken: covAsgRig's chat now has a missions row")
	}

	rr := covAsgPost(t, h, `{"target_slug":"asg-worker","task":"just chat work","crew_id":"`+crewID+
		`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		AssignmentID string `json:"assignment_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
	}

	var gotMissionID sql.NullString
	if err := h.db.QueryRow(`SELECT mission_id FROM assignments WHERE id = ?`, resp.AssignmentID).
		Scan(&gotMissionID); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if gotMissionID.Valid {
		t.Errorf("assignments.mission_id = %q, want NULL — a chat-only assignment with no backing "+
			"missions row must not be falsely attributed to one", gotMissionID.String)
	}
}

// TestIssueRuns_IncludesDelegationHopRun exercises the actual reason this
// matters end to end: issue_handler_runs.go's ListRuns finds a run by
// a.mission_id, and a delegation hop created through AssignmentHandler.Create
// with no mission_id in the body must now show up in "every run for this
// issue" the same as the mission task and mention-dispatch runs already do.
func TestIssueRuns_IncludesDelegationHopRun(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-DELEGATE", "IN_PROGRESS")
	execOrFatal(t, h.db, `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission: delegate', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		missionID, leadID, wsID)

	ah := NewAssignmentHandler(h.db, nil, nil, "internal-test-token", newTestLogger())

	body := `{"target_slug":"worker","task":"delegated sub-task","crew_id":"` + crewID +
		`","workspace_id":"` + wsID + `","chat_id":"` + missionID + `"}`
	rr := covAsgPost(t, ah, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		AssignmentID string `json:"assignment_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
	}
	_ = workerID

	rr2 := httptest.NewRecorder()
	h.ListRuns(rr2, issueRunsRequest(t, userID, wsID, crewID, "ENG-DELEGATE"))
	if rr2.Code != http.StatusOK {
		t.Fatalf("ListRuns status = %d; body=%s", rr2.Code, rr2.Body.String())
	}
	var got []issueRunDTO
	if err := json.Unmarshal(rr2.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr2.Body.String())
	}
	found := false
	for _, run := range got {
		if run.ID == resp.AssignmentID {
			found = true
		}
	}
	if !found {
		t.Errorf("ListRuns for ENG-DELEGATE did not include the delegation-hop run %q; got %+v",
			resp.AssignmentID, got)
	}
}
