package api

// issue_session_realtime_test.go — B11 (#2368): `issue.session.state`,
// `issue.checkpoint.written` and `run.outcome`, the two remaining signals
// golden scenario's accept line names ("the board moves without refresh
// for create, status change, comment, session state and outcome").
//
// Per §24.1's own warning ("a test asserting that a component SUBSCRIBES
// to an event is not proof anything repaints"), these drive a REAL ws.Hub
// (Run loop included) and assert on the frame an ws.Observer actually
// receives — the same pattern issue_status_changed_broadcast_test.go uses
// for issue.status_changed. Before this PR none of these three types were
// ever broadcast, so every assertion below timed out against pre-B11 code.

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type sessionStateFrame struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Payload struct {
		MissionID  string `json:"mission_id"`
		Identifier string `json:"identifier"`
		SessionID  string `json:"session_id"`
		AgentID    string `json:"agent_id"`
		State      string `json:"state"`
	} `json:"payload"`
}

// waitForFrameType drains observer frames for up to 3s looking for one of
// the given type. Mirrors nextIssueStatusChangedFrame's "skip anything
// that doesn't match" shape — several call sites broadcast issue.updated
// or other types first.
func waitForFrameType(t *testing.T, o interface{ Frames() <-chan []byte }, wantType string) []byte {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw, ok := <-o.Frames():
			if !ok {
				t.Fatalf("observer closed before a %s frame arrived", wantType)
			}
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				continue
			}
			if probe.Type == wantType {
				return raw
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s frame", wantType)
			return nil
		}
	}
}

type runOutcomeFrame struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Payload struct {
		MissionID    string `json:"mission_id"`
		Identifier   string `json:"identifier"`
		AssignmentID string `json:"assignment_id"`
		Status       string `json:"status"`
		Outcome      string `json:"outcome"`
	} `json:"payload"`
}

func TestBroadcast_IssueSessionState_OnActivate(t *testing.T) {
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	f.assign.hub = hub
	seedSession(t, f, "sess_rt_1", "pending")
	seedSessionAssignment(t, f, "asg_rt_1", "sess_rt_1", "RUNNING")

	obs := hub.AddObserver("workspace:"+f.wsID, "u-session-state-activate", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	activateSessionForAssignment(context.Background(), f.db, hub, f.wsID, "asg_rt_1")

	raw := waitForFrameType(t, obs, "issue.session.state")
	var frame sessionStateFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Payload.SessionID != "sess_rt_1" {
		t.Errorf("session_id = %q, want sess_rt_1", frame.Payload.SessionID)
	}
	if frame.Payload.State != "active" {
		t.Errorf("state = %q, want active", frame.Payload.State)
	}
	if frame.Payload.MissionID != f.missionID {
		t.Errorf("mission_id = %q, want %q", frame.Payload.MissionID, f.missionID)
	}
	if frame.Payload.Identifier != f.ident {
		t.Errorf("identifier = %q, want %q", frame.Payload.Identifier, f.ident)
	}
}

func TestBroadcast_IssueSessionState_OnSettle(t *testing.T) {
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	f.assign.hub = hub
	seedSession(t, f, "sess_rt_2", "active")
	seedSessionAssignment(t, f, "asg_rt_2", "sess_rt_2", "RUNNING")
	if _, err := f.db.Exec(`UPDATE issue_agent_sessions SET active_run_id = ? WHERE id = ?`, "asg_rt_2", "sess_rt_2"); err != nil {
		t.Fatalf("seed active_run_id: %v", err)
	}

	obs := hub.AddObserver("workspace:"+f.wsID, "u-session-state-settle", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	settleSessionForAssignment(context.Background(), f.db, hub, f.wsID, "asg_rt_2", "SUCCEEDED")

	raw := waitForFrameType(t, obs, "issue.session.state")
	var frame sessionStateFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Payload.SessionID != "sess_rt_2" {
		t.Errorf("session_id = %q, want sess_rt_2", frame.Payload.SessionID)
	}
	if frame.Payload.State != "idle" {
		t.Errorf("state = %q, want idle (SUCCEEDED routes to idle)", frame.Payload.State)
	}
}

func TestBroadcast_IssueSessionState_NoBroadcastWhenNothingChanged(t *testing.T) {
	// A closed session never moves (activateSessionForAssignment's WHERE
	// excludes it) — no frame should be sent claiming otherwise.
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	f.assign.hub = hub
	seedSession(t, f, "sess_rt_3", "closed")
	seedSessionAssignment(t, f, "asg_rt_3", "sess_rt_3", "RUNNING")

	obs := hub.AddObserver("workspace:"+f.wsID, "u-session-state-noop", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	activateSessionForAssignment(context.Background(), f.db, hub, f.wsID, "asg_rt_3")

	select {
	case raw := <-obs.Frames():
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Type == "issue.session.state" {
			t.Fatalf("unexpected issue.session.state broadcast for a closed session: %s", raw)
		}
	case <-time.After(300 * time.Millisecond):
		// no frame at all — expected.
	}
}

func TestBroadcast_RunOutcome(t *testing.T) {
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	if _, err := f.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'test task', 'RUNNING', 1, datetime('now'), ?, NULL)`,
		"asg_rt_outcome", f.wsID, f.missionID, f.target, f.target, f.missionID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	obs := hub.AddObserver("workspace:"+f.wsID, "u-run-outcome", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	broadcastRunOutcome(context.Background(), f.db, hub, f.wsID, "asg_rt_outcome", "COMPLETED", "SUCCEEDED")

	raw := waitForFrameType(t, obs, "run.outcome")
	var frame runOutcomeFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Payload.AssignmentID != "asg_rt_outcome" {
		t.Errorf("assignment_id = %q, want asg_rt_outcome", frame.Payload.AssignmentID)
	}
	if frame.Payload.Outcome != "SUCCEEDED" {
		t.Errorf("outcome = %q, want SUCCEEDED", frame.Payload.Outcome)
	}
	if frame.Payload.Status != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED", frame.Payload.Status)
	}
	if frame.Payload.MissionID != f.missionID {
		t.Errorf("mission_id = %q, want %q", frame.Payload.MissionID, f.missionID)
	}
}

func TestBroadcast_RunOutcome_NoMissionID_NoBroadcast(t *testing.T) {
	// A root /assign with no mission_id has no board to repaint.
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	if _, err := f.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'test task', 'RUNNING', 1, datetime('now'), NULL, NULL)`,
		"asg_rt_no_mission", f.wsID, f.missionID, f.target, f.target); err != nil {
		t.Fatalf("seed assignment with no mission_id: %v", err)
	}

	obs := hub.AddObserver("workspace:"+f.wsID, "u-run-outcome-no-mission", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	broadcastRunOutcome(context.Background(), f.db, hub, f.wsID, "asg_rt_no_mission", "COMPLETED", "SUCCEEDED")

	select {
	case raw := <-obs.Frames():
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Type == "run.outcome" {
			t.Fatalf("unexpected run.outcome broadcast for an assignment with no mission_id: %s", raw)
		}
	case <-time.After(300 * time.Millisecond):
		// no frame — expected.
	}
}

func TestBroadcast_IssueCheckpointWritten(t *testing.T) {
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	seedSession(t, f, "sess_rt_cp", "active")

	obs := hub.AddObserver("workspace:"+f.wsID, "u-checkpoint-written", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	broadcastIssueCheckpointWritten(context.Background(), f.db, hub, f.wsID, "sess_rt_cp")

	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw, ok := <-obs.Frames():
			if !ok {
				t.Fatal("observer closed before an issue.checkpoint.written frame arrived")
			}
			var probe struct {
				Type    string `json:"type"`
				Payload struct {
					SessionID string `json:"session_id"`
					MissionID string `json:"mission_id"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				continue
			}
			if probe.Type != "issue.checkpoint.written" {
				continue
			}
			if probe.Payload.SessionID != "sess_rt_cp" {
				t.Errorf("session_id = %q, want sess_rt_cp", probe.Payload.SessionID)
			}
			if probe.Payload.MissionID != f.missionID {
				t.Errorf("mission_id = %q, want %q", probe.Payload.MissionID, f.missionID)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for an issue.checkpoint.written frame")
		}
	}
}
