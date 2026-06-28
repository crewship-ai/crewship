package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// seedIssueRun inserts a pipeline_run linked to an issue via the
// triggered_via='issue' + triggered_by_id=<identifier> convention that
// GetRun's JOIN and ListRuns both rely on.
func seedIssueRun(t *testing.T, h *IssueHandler, wsID, pipelineID, slug, runID, identifier, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`
		INSERT INTO pipeline_runs (
		    id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at,
		    step_outputs_json, cost_usd, duration_ms, triggered_via, triggered_by_id,
		    inputs_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'run', ?, '{}', 0, 0, 'issue', ?, '{}', ?, ?)`,
		runID, wsID, pipelineID, slug, status, now, identifier, now, now); err != nil {
		t.Fatalf("seed issue run: %v", err)
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

// TestIssueRuns_UnknownIssue_Returns404 — an identifier with no mission
// row 404s rather than returning an empty list, matching the other issue
// sub-resource handlers.
func TestIssueRuns_UnknownIssue_Returns404(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)
	rr := httptest.NewRecorder()
	h.ListRuns(rr, issueRunsRequest(t, userID, wsID, crewID, "ENG-999"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestIssueRuns_ReturnsLinkedRuns — only runs triggered by this exact
// issue come back, newest-first, with the run-record-compatible shape.
func TestIssueRuns_ReturnsLinkedRuns(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "IN_PROGRESS")
	seedRunsPipeline(t, h.db, wsID, "pl-iss", "iss-pipe")

	// Two runs for ENG-1, one for ENG-2 — the latter must NOT leak in.
	seedIssueRun(t, h, wsID, "pl-iss", "iss-pipe", "prn_a", "ENG-1", "completed")
	seedIssueRun(t, h, wsID, "pl-iss", "iss-pipe", "prn_b", "ENG-1", "failed")
	seedIssueRun(t, h, wsID, "pl-iss", "iss-pipe", "prn_c", "ENG-2", "completed")

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
		if run.ID == "prn_c" {
			t.Fatalf("ENG-2's run leaked into ENG-1 list")
		}
		if run.TriggeredVia != "issue" {
			t.Fatalf("triggered_via = %q, want issue", run.TriggeredVia)
		}
	}
}
