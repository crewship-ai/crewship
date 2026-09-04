package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// issue_status_changed_broadcast_test.go — #2257: the issues board never
// moved on its own because nothing told a connected browser that an issue's
// STATUS changed. `issue.updated` was (and still is) broadcast on every
// mutation, but its payload is a bare `{id}` — a subscriber can't move a
// card between board columns without a full refetch of the issue. This adds
// a distinct `issue.status_changed` event, carrying `crew_id`/`status`/
// `from`/`to`, from every status-transition endpoint (human PATCH, agent
// PATCH, review approve/request-changes, stop) so a board can reconcile a
// status move without guessing.
//
// Each test starts a REAL ws.Hub (Run loop included — Hub.Broadcast only
// enqueues onto an unbuffered channel; nothing drains it without Run) and
// asserts on the frame an ws.Observer receives on "workspace:<wsID>",
// mirroring the pattern in assignments_run_cancel_leak_test.go.

type issueStatusChangedFrame struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Payload struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		CrewID     string `json:"crew_id"`
		Status     string `json:"status"`
		From       string `json:"from"`
		To         string `json:"to"`
	} `json:"payload"`
}

func startedTestHub(t *testing.T) *ws.Hub {
	t.Helper()
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return hub
}

// nextIssueStatusChangedFrame drains observer frames until it finds an
// "issue.status_changed" frame (or a non-matching frame arrives 3 times in a
// row's worth of wait, in which case the test times out) — several of these
// handlers broadcast issue.updated first and issue.status_changed second, so
// a naive "read exactly one frame" would flake on ordering.
func nextIssueStatusChangedFrame(t *testing.T, o *ws.Observer) issueStatusChangedFrame {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw, ok := <-o.Frames():
			if !ok {
				t.Fatal("observer closed before an issue.status_changed frame arrived")
			}
			var f issueStatusChangedFrame
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("unmarshal frame: %v (raw=%s)", err, raw)
			}
			if f.Type == "issue.status_changed" {
				return f
			}
		case <-deadline:
			t.Fatal("timed out waiting for an issue.status_changed frame")
			return issueStatusChangedFrame{}
		}
	}
}

func TestIssueStatusChangedBroadcast_HumanUpdate(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	hub := startedTestHub(t)
	h := NewIssueHandler(db, hub, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	obs := hub.AddObserver("workspace:"+wsID, "u-status-changed-human", 8)
	defer hub.RemoveObserver("workspace:"+wsID, obs)

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"status": "TODO"})
	if rr.Code != 200 {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	frame := nextIssueStatusChangedFrame(t, obs)
	if frame.Channel != "workspace:"+wsID {
		t.Errorf("channel = %q, want workspace:%s", frame.Channel, wsID)
	}
	if frame.Payload.Identifier != "ENG-1" {
		t.Errorf("payload.identifier = %q, want ENG-1", frame.Payload.Identifier)
	}
	if frame.Payload.CrewID != crewID {
		t.Errorf("payload.crew_id = %q, want %q", frame.Payload.CrewID, crewID)
	}
	if frame.Payload.From != "BACKLOG" || frame.Payload.To != "TODO" {
		t.Errorf("payload from/to = %q/%q, want BACKLOG/TODO", frame.Payload.From, frame.Payload.To)
	}
	if frame.Payload.Status != "TODO" {
		t.Errorf("payload.status = %q, want TODO", frame.Payload.Status)
	}
}

func TestIssueStatusChangedBroadcast_HumanUpdate_NoStatusField_NoEvent(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	hub := startedTestHub(t)
	h := NewIssueHandler(db, hub, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	obs := hub.AddObserver("workspace:"+wsID, "u-status-changed-human-noop", 8)
	defer hub.RemoveObserver("workspace:"+wsID, obs)

	// A title-only PATCH still broadcasts issue.updated (record() always
	// does), but must NOT also claim a status change that never happened.
	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"title": "Renamed"})
	if rr.Code != 200 {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	select {
	case raw := <-obs.Frames():
		var f issueStatusChangedFrame
		_ = json.Unmarshal(raw, &f)
		if f.Type == "issue.status_changed" {
			t.Fatalf("unexpected issue.status_changed broadcast for a title-only PATCH: %s", raw)
		}
	case <-time.After(300 * time.Millisecond):
		// no frame at all is also fine — issue.updated may or may not have
		// been observed depending on timing; the assertion is specifically
		// that no status_changed frame shows up.
	}
}

func TestIssueStatusChangedBroadcast_AgentUpdateStatus(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	hub := startedTestHub(t)
	h.hub = hub
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	obs := hub.AddObserver("workspace:"+wsID, "u-status-changed-agent", 8)
	defer hub.RemoveObserver("workspace:"+wsID, obs)

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","status":"IN_PROGRESS"}`,
		crewBoundCtx1186(wsID, crewID)))
	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	frame := nextIssueStatusChangedFrame(t, obs)
	if frame.Payload.CrewID != crewID {
		t.Errorf("payload.crew_id = %q, want %q", frame.Payload.CrewID, crewID)
	}
	if frame.Payload.From != "BACKLOG" || frame.Payload.To != "IN_PROGRESS" {
		t.Errorf("payload from/to = %q/%q, want BACKLOG/IN_PROGRESS", frame.Payload.From, frame.Payload.To)
	}
}

func TestIssueStatusChangedBroadcast_ReviewApprove(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	hub := startedTestHub(t)
	h := NewIssueHandler(db, hub, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "REVIEW")

	obs := hub.AddObserver("workspace:"+wsID, "u-status-changed-review", 8)
	defer hub.RemoveObserver("workspace:"+wsID, obs)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/issues/ENG-1/review", jsonBody(map[string]any{"action": "approve"})),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Review(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	frame := nextIssueStatusChangedFrame(t, obs)
	if frame.Payload.From != "REVIEW" || frame.Payload.To != "DONE" {
		t.Errorf("payload from/to = %q/%q, want REVIEW/DONE", frame.Payload.From, frame.Payload.To)
	}
	if frame.Payload.CrewID != crewID {
		t.Errorf("payload.crew_id = %q, want %q", frame.Payload.CrewID, crewID)
	}
}

func TestIssueStatusChangedBroadcast_Stop(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	hub := startedTestHub(t)
	h := NewIssueHandler(db, hub, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	obs := hub.AddObserver("workspace:"+wsID, "u-status-changed-stop", 8)
	defer hub.RemoveObserver("workspace:"+wsID, obs)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/issues/ENG-1/stop", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Stop(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	frame := nextIssueStatusChangedFrame(t, obs)
	if frame.Payload.From != "IN_PROGRESS" || frame.Payload.To != "CANCELLED" {
		t.Errorf("payload from/to = %q/%q, want IN_PROGRESS/CANCELLED", frame.Payload.From, frame.Payload.To)
	}
}

// TestIssueCreatedBroadcast_CarriesCrewID guards the crew_id enrichment on
// issue.created — needed so a subscriber that already knows which crew it's
// showing can skip a refetch for a create it can prove is off-screen
// (components/features/orchestration/issue-realtime.ts on the frontend).
func TestIssueCreatedBroadcast_CarriesCrewID(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, _, _ := seedIssueFixtures(t, db)
	hub := startedTestHub(t)
	h := NewIssueHandler(db, hub, nil, newTestLogger())

	obs := hub.AddObserver("workspace:"+wsID, "u-created-crew-id", 8)
	defer hub.RemoveObserver("workspace:"+wsID, obs)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/issues", jsonBody(map[string]any{"title": "New issue"})),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw, ok := <-obs.Frames():
			if !ok {
				t.Fatal("observer closed before an issue.created frame arrived")
			}
			var f issueStatusChangedFrame
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("unmarshal frame: %v (raw=%s)", err, raw)
			}
			if f.Type != "issue.created" {
				continue
			}
			if f.Payload.CrewID != crewID {
				t.Errorf("payload.crew_id = %q, want %q", f.Payload.CrewID, crewID)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for an issue.created frame")
		}
	}
}
