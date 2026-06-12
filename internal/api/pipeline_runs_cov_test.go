package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// pipeline_runs_cov_test.go covers the remaining branches of
// pipeline_runs.go: the manage-role gate on CancelRun, the happy
// cancel path against a live registry entry, ListActiveRuns with a
// populated registry, the limit/since query filters, and the
// closed-DB 500s. Helpers are prefixed covPRun.

// covPRunAcquire registers a live run in the registry and returns the
// release func (deferred by callers to avoid leaking the entry).
func covPRunAcquire(t *testing.T, reg *pipeline.RunRegistry, runID, wsID string) func() {
	t.Helper()
	_, release, err := reg.Acquire(context.Background(), pipeline.AcquireOpts{
		RunID:        runID,
		WorkspaceID:  wsID,
		PipelineID:   "covprun-pipe",
		PipelineSlug: "covprun-slug",
	})
	if err != nil {
		t.Fatalf("registry acquire: %v", err)
	}
	return release
}

func TestCovPRun_CancelRun_MemberRole_403(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	h.SetRunRegistry(pipeline.NewRunRegistry())

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/r1/cancel", nil),
		userID, wsID, "MEMBER")
	req.SetPathValue("runId", "r1")
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPRun_CancelRun_LiveRun_RequestsCancel(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	reg := pipeline.NewRunRegistry()
	h.SetRunRegistry(reg)
	// One run in another workspace (exercises the scan-skip arm of the
	// found loop) plus the target run in ours.
	releaseOther := covPRunAcquire(t, reg, "covprun-other", "other-ws")
	defer releaseOther()
	release := covPRunAcquire(t, reg, "covprun-live", wsID)
	defer release()

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/covprun-live/cancel", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("runId", "covprun-live")
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["run_id"] != "covprun-live" || resp["cancel_requested"] != true {
		t.Errorf("resp = %v, want run_id=covprun-live cancel_requested=true", resp)
	}
	if !reg.IsCancelRequested("covprun-live") {
		t.Errorf("registry did not record cancel request")
	}
}

func TestCovPRun_GetRun_DBError_500(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	db.Close()

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/r1", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("runId", "r1")
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPRun_ListWorkspaceRuns_LimitAndSinceFilters(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "covprun-p1", "covprun-p1")
	seedRunRow(t, db, wsID, "covprun-p1", "covprun-p1", "covprun-r1", "completed")
	seedRunRow(t, db, wsID, "covprun-p1", "covprun-p1", "covprun-r2", "completed")

	// limit=1 caps the rows even though two match; since= far in the
	// past keeps both eligible so the cap is what bites.
	since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	req := withWorkspaceUser(
		httptest.NewRequest("GET",
			"/api/v1/workspaces/"+wsID+"/pipeline-runs?limit=1&since="+since+"&status=completed", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows  []map[string]any `json:"rows"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || len(resp.Rows) != 1 {
		t.Fatalf("count = %d rows=%d, want exactly 1 (limit applied)", resp.Count, len(resp.Rows))
	}

	// since= in the future excludes everything.
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	req = withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs?since="+future, nil),
		userID, wsID, "OWNER")
	rr = httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 0 {
		t.Fatalf("future since returned %d rows, want 0", resp.Count)
	}
}

func TestCovPRun_ListWorkspaceRuns_DBError_500(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	db.Close()

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPRun_ListActiveRuns_PopulatedRegistry(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	reg := pipeline.NewRunRegistry()
	h.SetRunRegistry(reg)
	release := covPRunAcquire(t, reg, "covprun-active", wsID)
	defer release()

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipelines/runs/active", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListActiveRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["run_id"] != "covprun-active" || rows[0]["pipeline_slug"] != "covprun-slug" {
		t.Errorf("row = %v, want run_id=covprun-active pipeline_slug=covprun-slug", rows[0])
	}
	if rows[0]["cancel_requested"] != false {
		t.Errorf("cancel_requested = %v, want false", rows[0]["cancel_requested"])
	}
}
