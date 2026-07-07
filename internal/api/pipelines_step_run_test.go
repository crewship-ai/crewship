package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"log/slog"
)

// recordingRunner captures the AgentStepRequest the handler forwards so a
// step-run test can assert the prompt was rendered against the fixture and
// the tier resolved into an adapter/model — things the shared stubRunner
// (output-only) can't observe.
type recordingRunner struct {
	last   pipeline.AgentStepRequest
	output string
	calls  int
}

func (r *recordingRunner) RunStep(_ context.Context, req pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	r.calls++
	r.last = req
	return pipeline.AgentStepResult{Output: r.output, CostUSD: 0.002, TokensIn: 120, TokensOut: 40, DurationMs: 7}, nil
}

// insertRawPipeline seeds a pipeline with a caller-supplied definition JSON
// (seedSmokePipeline hardcodes a single agent_run step; step-run tests need
// custom step shapes — a non-agent step, an interpolated prompt).
func insertRawPipeline(t *testing.T, db *sql.DB, slug, def string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES (?, 'ws_smoke', ?, ?, ?, ?, 0, 1, 'crew_a', 'agent_lead', 'agent_tool_call', ?, 1, ?, ?)`,
		"pln_test_"+slug, slug, slug, def, "hash_"+slug, now, now, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestPipelinesAPI_StepRun_ExecutesSingleStep(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	// agent_run step "extract" with a fixture-interpolated prompt.
	def := `{"name":"parse-invoice","steps":[{"id":"extract","type":"agent_run","agent_slug":"agent_lead","prompt":"Extract from {{ inputs.name }}"}]}`
	insertRawPipeline(t, db, "parse-invoice", def)

	runner := &recordingRunner{output: "{\"total\": 42}"}
	h := NewPipelineHandler(db, slog.Default(), runner, nil)

	body := strings.NewReader(`{"step_id":"extract","inputs":{"name":"sample.pdf"},"tier_override":"fast"}`)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/api/v1/workspaces/ws_smoke/pipelines/parse-invoice/step_run", body), "ws_smoke")
	req.SetPathValue("slug", "parse-invoice")
	w := httptest.NewRecorder()
	h.StepRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["step_id"] != "extract" || out["output"] != "{\"total\": 42}" {
		t.Errorf("unexpected response: %+v", out)
	}
	if out["valid"] != true || out["simulated"] != true {
		t.Errorf("valid/simulated flags wrong: %+v", out)
	}
	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls)
	}
	// Prompt rendered against the fixture (no upstream steps).
	if runner.last.Prompt != "Extract from sample.pdf" {
		t.Errorf("rendered prompt = %q, want fixture-interpolated", runner.last.Prompt)
	}
	// Isolation: no run/step id → no persistence / sub-span capture.
	if runner.last.PipelineRunID != "" || runner.last.StepID != "" {
		t.Errorf("expected empty run/step ids (non-persisted sim), got %q/%q", runner.last.PipelineRunID, runner.last.StepID)
	}
	// Tier resolved into a concrete adapter/model.
	if runner.last.Adapter == "" || runner.last.Model == "" {
		t.Errorf("tier not resolved into adapter/model: %+v", runner.last)
	}
}

func TestPipelinesAPI_StepRun_503WithoutRunner(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	insertRawPipeline(t, db, "demo", `{"name":"demo","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`)
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(`{"step_id":"a"}`)), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.StepRun(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestPipelinesAPI_StepRun_UnknownStep404(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	insertRawPipeline(t, db, "demo", `{"name":"demo","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`)
	h := NewPipelineHandler(db, slog.Default(), &recordingRunner{}, nil)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(`{"step_id":"ghost"}`)), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.StepRun(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown step, got %d", w.Code)
	}
}

func TestPipelinesAPI_StepRun_NonAgentStep400(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	// A transform step — step-run only supports agent_run.
	insertRawPipeline(t, db, "demo", `{"name":"demo","steps":[{"id":"t","type":"transform","transform":{"input":"true","expression":"."}}]}`)
	h := NewPipelineHandler(db, slog.Default(), &recordingRunner{}, nil)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(`{"step_id":"t"}`)), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.StepRun(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-agent_run step, got %d", w.Code)
	}
}

func TestPipelinesAPI_StepRun_MissingStepID400(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	insertRawPipeline(t, db, "demo", `{"name":"demo","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`)
	h := NewPipelineHandler(db, slog.Default(), &recordingRunner{}, nil)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(`{"inputs":{}}`)), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.StepRun(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing step_id, got %d", w.Code)
	}
}
