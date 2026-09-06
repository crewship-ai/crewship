package api

// `routine active` omitted runs parked on an approval waitpoint (#2413 item 6).
//
// ListActiveRuns read the in-process RunRegistry and nothing else. A run that
// parks on a wait step RELEASES its registry entry and its concurrency slot by
// design — executor.go returns WAITING promptly so the slot is freed — so the
// endpoint reported nothing while a run sat waiting for a human, and printed
// "No active runs." for a run that resumed and completed the moment somebody
// approved it. Its own help said "in-flight routine runs".
//
// The store's ListActive and the workspace feed's ?status=active already agree
// that in-flight means queued/running/waiting. These tests pin that this
// endpoint joined them, that the registry half still answers when the store is
// absent, and that a run present in both is not listed twice.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// acquireRegistryRun puts one run in the in-process registry, the way a real
// execution does, and returns its release func.
func acquireRegistryRun(t *testing.T, reg *pipeline.RunRegistry, wsID, pipelineID, slug, runID string) func() {
	t.Helper()
	_, release, err := reg.Acquire(context.Background(), pipeline.AcquireOpts{
		RunID:        runID,
		WorkspaceID:  wsID,
		PipelineID:   pipelineID,
		PipelineSlug: slug,
	})
	if err != nil {
		t.Fatalf("acquire %s: %v", runID, err)
	}
	return release
}

func activeRunsBody(t *testing.T, h *PipelineHandler, userID, wsID string) []map[string]any {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipelines/runs/active", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListActiveRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	return out
}

func TestPipelineRuns_ListActiveRuns_IncludesWaitingRuns(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetRunRegistry(pipeline.NewRunRegistry())
	h.SetRunStore(pipeline.NewRunStore(db))
	seedRunsPipeline(t, db, wsID, "pl_1", "nightly")
	// Parked on an approval: the row exists, the registry entry does not.
	seedRunRow(t, db, wsID, "pl_1", "nightly", "run_waiting", "waiting")

	rows := activeRunsBody(t, h, userID, wsID)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the waiting run: %+v", len(rows), rows)
	}
	if rows[0]["run_id"] != "run_waiting" {
		t.Errorf("run_id = %v, want run_waiting", rows[0]["run_id"])
	}
	if rows[0]["status"] != "waiting" {
		t.Errorf("status = %v, want waiting — the state has to be visible, "+
			"because a parked run needs an approval and a running one needs nothing",
			rows[0]["status"])
	}
	if rows[0]["pipeline_slug"] != "nightly" {
		t.Errorf("pipeline_slug = %v, want nightly", rows[0]["pipeline_slug"])
	}
}

func TestPipelineRuns_ListActiveRuns_IncludesQueuedAndRunningRows(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetRunStore(pipeline.NewRunStore(db))
	seedRunsPipeline(t, db, wsID, "pl_1", "nightly")
	seedRunRow(t, db, wsID, "pl_1", "nightly", "run_q", "queued")
	seedRunRow(t, db, wsID, "pl_1", "nightly", "run_r", "running")
	// Terminal rows must stay out — "active" is not "recent".
	seedRunRow(t, db, wsID, "pl_1", "nightly", "run_done", "completed")

	rows := activeRunsBody(t, h, userID, wsID)
	got := map[string]string{}
	for _, r := range rows {
		id, _ := r["run_id"].(string)
		st, _ := r["status"].(string)
		got[id] = st
	}
	if len(got) != 2 || got["run_q"] != "queued" || got["run_r"] != "running" {
		t.Errorf("got %v, want exactly run_q=queued and run_r=running", got)
	}
}

func TestPipelineRuns_ListActiveRuns_NoStoreStillAnswersFromTheRegistry(t *testing.T) {
	// The registry half is what the cancel buttons need, and it must survive
	// a handler built without a store (several construction paths, including
	// older tests, leave it nil).
	h, db, userID, wsID := runsHandlerRig(t)
	reg := pipeline.NewRunRegistry()
	h.SetRunRegistry(reg)
	seedRunsPipeline(t, db, wsID, "pl_1", "nightly")
	release := acquireRegistryRun(t, reg, wsID, "pl_1", "nightly", "run_live")
	defer release()

	rows := activeRunsBody(t, h, userID, wsID)
	if len(rows) != 1 || rows[0]["run_id"] != "run_live" {
		t.Fatalf("registry run missing with no store wired: %+v", rows)
	}
	if rows[0]["status"] != "running" {
		t.Errorf("status = %v, want running", rows[0]["status"])
	}
}

func TestPipelineRuns_ListActiveRuns_DoesNotDoubleListARunInBothSources(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	reg := pipeline.NewRunRegistry()
	h.SetRunRegistry(reg)
	h.SetRunStore(pipeline.NewRunStore(db))
	seedRunsPipeline(t, db, wsID, "pl_1", "nightly")
	seedRunRow(t, db, wsID, "pl_1", "nightly", "run_both", "running")
	release := acquireRegistryRun(t, reg, wsID, "pl_1", "nightly", "run_both")
	defer release()

	rows := activeRunsBody(t, h, userID, wsID)
	if len(rows) != 1 {
		t.Fatalf("a run in the registry AND the store must appear once, got %d: %+v", len(rows), rows)
	}
}
