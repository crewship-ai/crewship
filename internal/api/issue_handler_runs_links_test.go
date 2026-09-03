package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedRunStarted writes the journal entry assignments_run.go emits when an
// assignment is dispatched: trace_id = run id, mission_id = the issue,
// payload.assignment_id = the row. It is the only link between an
// assignment and its run, so the issue-runs endpoint has to read it.
func seedRunStarted(t *testing.T, h *IssueHandler, wsID, missionID, agentID, assignID, runID, ts string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO journal_entries (id, workspace_id, agent_id, mission_id, ts, entry_type, severity,
		    actor_type, summary, payload, refs, trace_id)
		VALUES (?, ?, ?, ?, ?, 'run.started', 'info', 'orchestrator', 'run started (assignment)',
		    ?, ?, ?)`,
		"je-"+runID, wsID, agentID, missionID, ts,
		`{"trigger_type":"ASSIGNMENT","assignment_id":"`+assignID+`"}`,
		`{"assignment_id":"`+assignID+`"}`, runID); err != nil {
		t.Fatalf("seed run.started: %v", err)
	}
}

// TestIssueRuns_CarriesRunAndAgentLinks — every row names the journal run it
// produced (run_id == trace_id) and the agent by id and slug, so a client can
// open the run, its journal trace and the agent instead of dead-ending on a
// name. An assignment that never reached a run carries no run id.
func TestIssueRuns_CarriesRunAndAgentLinks(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	m1 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	m2 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "IN_PROGRESS")

	seedMissionAssignment(t, h, wsID, m1, workerID, "asg_a", "COMPLETED", "wrote report",
		"2026-06-01T10:00:00Z", "2026-06-01T10:00:30Z")
	seedMissionAssignment(t, h, wsID, m1, leadID, "asg_b", "PENDING", "", "", "")
	seedMissionAssignment(t, h, wsID, m2, workerID, "asg_c", "COMPLETED", "other issue",
		"2026-06-01T12:00:00Z", "2026-06-01T12:00:10Z")
	seedRunStarted(t, h, wsID, m1, workerID, "asg_a", "run_aaaa", "2026-06-01T10:00:00.100Z")
	// The other issue's run must not be borrowed by ENG-1's rows.
	seedRunStarted(t, h, wsID, m2, workerID, "asg_c", "run_cccc", "2026-06-01T12:00:00.100Z")

	rr := httptest.NewRecorder()
	h.ListRuns(rr, issueRunsRequest(t, userID, wsID, crewID, "ENG-1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var got []issueRunDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	byID := map[string]issueRunDTO{}
	for _, run := range got {
		byID[run.ID] = run
	}
	a, ok := byID["asg_a"]
	if !ok {
		t.Fatalf("asg_a missing; body=%s", rr.Body.String())
	}
	if a.RunID != "run_aaaa" || a.TraceID != "run_aaaa" {
		t.Fatalf("asg_a run_id/trace_id = %q/%q, want run_aaaa for both", a.RunID, a.TraceID)
	}
	if a.AgentID != workerID || a.AgentSlug != "worker" {
		t.Fatalf("asg_a agent_id/agent_slug = %q/%q, want %q/worker", a.AgentID, a.AgentSlug, workerID)
	}
	b, ok := byID["asg_b"]
	if !ok {
		t.Fatalf("asg_b missing; body=%s", rr.Body.String())
	}
	if b.RunID != "" || b.TraceID != "" {
		t.Fatalf("asg_b never ran, yet run_id/trace_id = %q/%q", b.RunID, b.TraceID)
	}
	if got := headerInt(t, rr, "X-Total-Count"); got != 2 {
		t.Fatalf("X-Total-Count = %d, want 2", got)
	}
}

// TestIssueRuns_Pages — ?limit/?offset page the list and X-Total-Count still
// names every run, so a client can fetch the rest instead of stopping at a
// silent cap.
func TestIssueRuns_Pages(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	m1 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	for i, id := range []string{"asg_1", "asg_2", "asg_3"} {
		started := "2026-06-01T1" + string(rune('0'+i)) + ":00:00Z"
		seedMissionAssignment(t, h, wsID, m1, workerID, id, "COMPLETED", "ok", started, started)
	}

	req := issueRunsRequest(t, userID, wsID, crewID, "ENG-1")
	q := req.URL.Query()
	q.Set("limit", "2")
	q.Set("offset", "1")
	req.URL.RawQuery = q.Encode()
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var got []issueRunDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("page len = %d, want 2", len(got))
	}
	// Newest first, offset 1 skips asg_3 (started 12:00).
	if got[0].ID != "asg_2" || got[1].ID != "asg_1" {
		t.Fatalf("page = %s,%s; want asg_2,asg_1", got[0].ID, got[1].ID)
	}
	if got := headerInt(t, rr, "X-Total-Count"); got != 3 {
		t.Fatalf("X-Total-Count = %d, want 3", got)
	}
	if got := headerInt(t, rr, "X-Offset"); got != 1 {
		t.Fatalf("X-Offset = %d, want 1", got)
	}
}
