package api

// Version-pinned replay: the read-only primitive `crewship routine
// backtest` composes on (issue #1421). ReplayRun already re-invokes a
// prior run's captured inputs; this adds an optional pinned_version so
// the replay executes a specific immutable version's definition instead
// of head — without ever touching which version is head/live.
//
// Mirrors the pinning contract already proven for webhook fire +
// schedule force-fire in pipeline_trigger_pinning_test.go: a pin
// executes the pinned definition (never a silent head fallback), and a
// pin at a missing version fails legibly (409) with nothing executed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// TestReplayRun_PinnedVersion_ExecutesPinned_NotHead is the core
// backtest primitive: replaying an existing run with pinned_version set
// executes THAT version's definition, and never mutates the pipeline's
// head_version or head definition — a backtest against a candidate
// version must stay a read-only evaluation.
func TestReplayRun_PinnedVersion_ExecutesPinned_NotHead(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)

	seedVersionedPipeline(t, db, wsID, "pln_replay_pin", "replay-pin")
	origRunID := "run_orig_1"
	seedRunRow(t, db, wsID, "pln_replay_pin", "replay-pin", origRunID, "completed")

	one := 1
	body, err := json.Marshal(map[string]any{"pinned_version": one})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/"+origRunID+"/replay", strings.NewReader(string(body))),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", origRunID)
	rr := httptest.NewRecorder()
	h.ReplayRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var res struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || res.RunID == "" {
		t.Fatalf("decode replay response (err=%v): %s", err, rr.Body.String())
	}

	rec, err := runStore.Get(t.Context(), res.RunID)
	if err != nil {
		t.Fatalf("load replayed run row: %v", err)
	}
	if rec.PipelineVersion == nil || *rec.PipelineVersion != 1 {
		t.Errorf("replayed run pipeline_version: got %v, want 1", rec.PipelineVersion)
	}
	// Step outputs live in the normalized pipeline_run_step_outputs table
	// since #1411 — RunRecord.StepOutputsJSON is no longer written on the
	// hot path, so read via GetStepOutputs (same as the sibling
	// pipeline_trigger_pinning_test.go).
	outputs, err := runStore.GetStepOutputs(t.Context(), rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if _, ok := outputs["v1step"]; !ok {
		t.Errorf("step outputs %#v missing v1step — pinned version did not execute", outputs)
	}
	if _, ok := outputs["v2step"]; ok {
		t.Errorf("step outputs %#v contain v2step — HEAD executed despite the pin", outputs)
	}

	// The read-only guarantee: head must be untouched by the backtest
	// replay — still v2, still the v2 definition.
	p, err := h.store.GetBySlug(t.Context(), wsID, "replay-pin")
	if err != nil {
		t.Fatalf("reload pipeline: %v", err)
	}
	if p.DefinitionJSON != pinnedV2DSL {
		t.Errorf("pipeline head definition mutated by backtest replay: got %q", p.DefinitionJSON)
	}
	var headVersion int
	if err := db.QueryRow(`SELECT head_version FROM pipelines WHERE id = ?`, "pln_replay_pin").Scan(&headVersion); err != nil {
		t.Fatalf("read head_version: %v", err)
	}
	if headVersion != 2 {
		t.Errorf("head_version mutated by backtest replay: got %d, want 2", headVersion)
	}
}

// TestReplayRun_PinnedVersionMissing_FailsLegibly_NoRunCreated: a
// backtest against a candidate version that was deleted (or never
// existed) must fail with a legible 409 and must not silently fall back
// to executing head — that would produce a "backtest" result that
// secretly compared head against itself.
func TestReplayRun_PinnedVersionMissing_FailsLegibly_NoRunCreated(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)

	seedVersionedPipeline(t, db, wsID, "pln_replay_pin_ghost", "replay-pin-ghost")
	origRunID := "run_orig_ghost"
	seedRunRow(t, db, wsID, "pln_replay_pin_ghost", "replay-pin-ghost", origRunID, "completed")

	ninetyNine := 99
	body, err := json.Marshal(map[string]any{"pinned_version": ninetyNine})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/"+origRunID+"/replay", strings.NewReader(string(body))),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", origRunID)
	rr := httptest.NewRecorder()
	h.ReplayRun(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "version") {
		t.Errorf("error body %q should name the missing version", rr.Body.String())
	}

	var runCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE id != ?`, origRunID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Errorf("pipeline_runs rows beyond the original: got %d, want 0 — missing pinned version must not execute anything", runCount)
	}
}

// TestReplayRun_NoPinnedVersion_ExecutesHead_Unchanged: omitting
// pinned_version (or an empty body) must keep today's replay behaviour
// — replay against HEAD — so this is a strictly additive change.
func TestReplayRun_NoPinnedVersion_ExecutesHead_Unchanged(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)

	seedVersionedPipeline(t, db, wsID, "pln_replay_head", "replay-head")
	origRunID := "run_orig_head"
	seedRunRow(t, db, wsID, "pln_replay_head", "replay-head", origRunID, "completed")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/"+origRunID+"/replay", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", origRunID)
	rr := httptest.NewRecorder()
	h.ReplayRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var res struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || res.RunID == "" {
		t.Fatalf("decode replay response (err=%v): %s", err, rr.Body.String())
	}
	rec, err := runStore.Get(t.Context(), res.RunID)
	if err != nil {
		t.Fatalf("load replayed run row: %v", err)
	}
	// Step outputs read from the normalized table (#1411), not the
	// no-longer-written StepOutputsJSON column.
	outputs, err := runStore.GetStepOutputs(t.Context(), rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if _, ok := outputs["v2step"]; !ok {
		t.Errorf("step outputs %#v missing v2step — expected HEAD (v2) to execute when unpinned", outputs)
	}
}
