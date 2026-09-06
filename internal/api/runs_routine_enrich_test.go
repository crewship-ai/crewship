package api

// GET /api/v1/runs must be able to NAME a routine run.
//
// A routine run has no agent by design: a schedule- or webhook-triggered run
// has no invoking agent, so runs.agent_id is empty, the agent enrichment
// skips the row, and agent_slug comes back null. Every client then renders it
// as "?" — which reads as missing data rather than as "a routine did this".
// The journal's pipeline.run.started payload carries neither the routine slug
// nor how it was triggered, so trigger_type came back "" as well.
//
// Both facts live in the pipeline_runs row keyed by the same id. These tests
// pin that the enrichment reads them, that it does NOT overwrite a
// trigger_type the journal did record, and that an agent run is untouched.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// insertPipelineRunRow writes the pipeline_runs row that emitPipelineRunRow's
// journal entries describe. The journal and the table are written by
// different producers in production, so the fixture keeps them separate too —
// a test that wrote both from one helper could not express the case where
// the table row is missing.
func insertPipelineRunRow(t *testing.T, f *runsTestFixture, runID, slug, triggeredVia string, when time.Time) {
	t.Helper()
	// pipeline_runs.pipeline_id is a real FK, so the routine has to exist.
	if _, err := f.h.db.Exec(`
		INSERT OR IGNORE INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('pl_test', ?, 'test-routine', 'Test routine', '{}', 'h')`, f.wsID); err != nil {
		t.Fatalf("insert pipelines: %v", err)
	}
	_, err := f.h.db.Exec(`
		INSERT INTO pipeline_runs
			(id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, triggered_via)
		VALUES (?, ?, 'pl_test', ?, 'completed', 'run', ?, ?)`,
		runID, f.wsID, slug, when.UTC().Format(time.RFC3339), triggeredVia)
	if err != nil {
		t.Fatalf("insert pipeline_runs %s: %v", runID, err)
	}
}

func listRuns(t *testing.T, f *runsTestFixture) map[string]runResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp runListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]runResponse{}
	for _, r := range resp.Data {
		byID[r.ID] = r
	}
	return byID
}

func TestRunHandler_List_RoutineRunCarriesSlugAndTrigger(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitPipelineRunRow(t, "routine_x", "COMPLETED", now.Add(-2*time.Minute))
	insertPipelineRunRow(t, f, "routine_x", "nightly-digest", "schedule", now.Add(-2*time.Minute))

	got := listRuns(t, f)["routine_x"]
	if got.PipelineSlug == nil || *got.PipelineSlug != "nightly-digest" {
		t.Errorf("pipeline_slug = %v, want nightly-digest — without it the row has no name to show", got.PipelineSlug)
	}
	// triggered_via "schedule" is presented in the documented trigger_type
	// vocabulary (USER / CRON / WEBHOOK), the one the run.started payload
	// and the ?trigger= filter use.
	if got.TriggerType != "CRON" {
		t.Errorf("trigger_type = %q, want CRON (from pipeline_runs.triggered_via = schedule)", got.TriggerType)
	}
	// Not asserted here: that agent_slug is null. The shared fixture stamps
	// an agent_id on every journal row it writes, so this test cannot
	// reproduce the agent-less run that motivated the change — and asserting
	// against the fixture's own choice would pin the fixture, not the
	// handler. What matters for the defect is that the routine's own name and
	// trigger are present, which is what is checked above; the client-side
	// half (prefer the agent, fall back to the routine) is pinned in
	// cmd/crewship's history test.
}

func TestRunHandler_List_RoutineRunWithoutTableRowStaysEmpty(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	// Journal entries only — no pipeline_runs row (a swept or pre-migration
	// run). The enrichment must degrade, not fail the whole list.
	f.emitPipelineRunRow(t, "routine_y", "COMPLETED", now.Add(-2*time.Minute))

	got := listRuns(t, f)["routine_y"]
	if got.Kind != "pipeline" {
		t.Fatalf("kind = %q, want pipeline", got.Kind)
	}
	if got.PipelineSlug != nil {
		t.Errorf("pipeline_slug = %v, want nil when there is no row to read it from", *got.PipelineSlug)
	}
}

func TestRunHandler_List_AgentRunUnaffectedByRoutineEnrichment(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitRunRow(t, "run_plain", "COMPLETED", "USER", now.Add(-time.Minute))

	got := listRuns(t, f)["run_plain"]
	if got.Kind != "agent" {
		t.Fatalf("kind = %q, want agent", got.Kind)
	}
	if got.PipelineSlug != nil {
		t.Errorf("pipeline_slug = %v, want nil on an agent run", *got.PipelineSlug)
	}
	// The journal's own trigger_type must survive: the pipeline lookup is a
	// fallback for a missing value, never an override.
	if got.TriggerType != "USER" {
		t.Errorf("trigger_type = %q, want the journal's USER", got.TriggerType)
	}
}
