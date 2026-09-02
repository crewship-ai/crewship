package api

// Coverage for the "late failure leaks past cancel_requested_at" defect
// documented in TestFinishAssignment_CancelRequested_OverridesLateCompletion
// (issue_stop_cancel_test.go): that test proves the STATUS column is
// protected, but flags (as a FINDING, not an assertion) that a late FAILURE
// report still reached the websocket broadcast and the mission comment as a
// failure. These two tests turn those findings into red-then-green
// assertions for the two surfaces this fix corrects; the DB column itself
// (assignments.error_message) is a deliberate exception — see the report for
// this change — and stays covered by the existing test's own assertion that
// it retains the raw late-report text.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// cancelLeakRunningHub starts a real Hub.Run loop so BroadcastChannel
// actually dispatches to observers — Hub.Broadcast only enqueues onto
// h.broadcast; nothing drains it without Run (see internal/ws/observer_test.go
// receiveFrame's comment). Modeled on run_stream_tenancy_test.go's
// streamStatusAs helper.
func cancelLeakRunningHub(t *testing.T) *ws.Hub {
	t.Helper()
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return hub
}

type cancelLeakFrame struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

func cancelLeakNextFrame(t *testing.T, o *ws.Observer) cancelLeakFrame {
	t.Helper()
	select {
	case raw, ok := <-o.Frames():
		if !ok {
			t.Fatal("observer closed before a frame arrived")
		}
		var f cancelLeakFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a broadcast frame")
		return cancelLeakFrame{}
	}
}

// TestFinishAssignment_CancelRequested_LateFailure_BroadcastsCancelled
// proves surface (1) from the audit: a RUNNING assignment with
// cancel_requested_at set that reports a late FAILURE must broadcast
// "assignment_cancelled" on the session channel, not "assignment_failed".
// Before the fix, the switch in finishAssignment tests `errMsg != ""` before
// `status == "CANCELLED"`, so this failed with Type == "assignment_failed".
func TestFinishAssignment_CancelRequested_LateFailure_BroadcastsCancelled(t *testing.T) {
	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	h.hub = cancelLeakRunningHub(t)

	obs := h.hub.AddObserver("session:"+chatID, "u-cancel-leak", 8)
	defer h.hub.RemoveObserver("session:"+chatID, obs)

	now := time.Now().UTC().Format(time.RFC3339)
	assignmentID := "a-cancel-leak-broadcast"
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at, started_at, cancel_requested_at, cancel_reason)
		VALUES (?, ?, ?, ?, ?, 'do work', 'RUNNING', ?, ?, ?, 'issue stopped')`,
		assignmentID, wsID, chatID, leadID, workerID, now, now, now); err != nil {
		t.Fatalf("seed live-then-stopped assignment: %v", err)
	}

	if ok := h.finishAssignment(context.Background(), assignmentID, "", chatID, "asg-worker", wsID,
		"", "container blew up, arrived after stop", nil); !ok {
		t.Fatal("finishAssignment should have won the terminal CAS")
	}

	frame := cancelLeakNextFrame(t, obs)
	if frame.Type != "assignment_cancelled" {
		t.Errorf("broadcast type = %q, want %q — a late failure after Stop must read as cancelled, not failed",
			frame.Type, "assignment_cancelled")
	}
}

// TestFinishAssignment_CancelRequested_LateFailure_MissionCommentIsCancellation
// proves surface (2) from the audit: the mission-comment block must post a
// cancellation-shaped comment (and NOT the "encountered an issue" failure
// framing with the raw error text) when the row resolved to CANCELLED. Before
// the fix, this block only branches on `errMsg != ""`, so it posted
// "**Worker encountered an issue.** container blew up...".
func TestFinishAssignment_CancelRequested_LateFailure_MissionCommentIsCancellation(t *testing.T) {
	h, wsID, crewID, leadID, workerID, _ := covAsgRig(t)

	missionID := "chat-cancel-leak-mission"
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'm', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		missionID, leadID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-cancel-leak', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	assignmentID := "a-cancel-leak-comment"
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at, started_at, cancel_requested_at, cancel_reason)
		VALUES (?, ?, ?, ?, ?, 'do work', 'RUNNING', ?, ?, ?, ?, 'issue stopped')`,
		assignmentID, wsID, missionID, leadID, workerID, missionID, now, now, now); err != nil {
		t.Fatalf("seed live-then-stopped mission-linked assignment: %v", err)
	}

	lateErr := "container blew up, arrived after stop"
	h.finishAssignment(context.Background(), assignmentID, "", missionID, "asg-worker", wsID, "", lateErr, nil)

	var commentBody string
	if err := h.db.QueryRow(`SELECT body FROM mission_comments WHERE mission_id = ?`, missionID).Scan(&commentBody); err != nil {
		t.Fatalf("query comment: %v", err)
	}
	if strings.Contains(commentBody, "encountered an issue") {
		t.Errorf("comment = %q, must not use the failure framing for a cancelled run", commentBody)
	}
	if strings.Contains(commentBody, lateErr) {
		t.Errorf("comment = %q, must not leak the raw late-failure text into a cancellation notice", commentBody)
	}
	if !strings.Contains(strings.ToLower(commentBody), "cancel") {
		t.Errorf("comment = %q, want a cancellation-shaped notice", commentBody)
	}
}
