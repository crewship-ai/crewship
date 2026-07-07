package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// stepRunRequestBody is the /step_run body — one step, one fixture.
type stepRunRequestBody struct {
	StepID string         `json:"step_id"`
	Inputs map[string]any `json:"inputs"`
	// StepOutputs seeds upstream `{{ steps.<id>.output }}` refs so a step that
	// consumes another step's output can be debugged in isolation with a
	// fixture standing in for the DAG (most non-first steps do). step_id →
	// raw output string; no upstream execution happens. Absent → those refs
	// render empty and the response WARNs.
	StepOutputs map[string]string `json:"step_outputs,omitempty"`
	// TierOverride replaces the step's complexity for this one execution
	// (trivial | fast | moderate | smart); any other value is ignored. A
	// step-level model_override always wins over this.
	TierOverride string `json:"tier_override,omitempty"`
}

// stepRunResponse is the debug verdict for one simulated step: what was
// actually sent (rendered prompt + resolved model), what came back, whether
// it validates, and what it cost.
type stepRunResponse struct {
	StepID           string  `json:"step_id"`
	StepType         string  `json:"step_type"`
	Adapter          string  `json:"adapter"`
	Model            string  `json:"model"`
	RenderedPrompt   string  `json:"rendered_prompt"`
	Output           string  `json:"output"`
	Valid            bool    `json:"valid"`
	ValidationReason string  `json:"validation_reason,omitempty"`
	CostUSD          float64 `json:"cost_usd"`
	TokensIn         int     `json:"tokens_in"`
	TokensOut        int     `json:"tokens_out"`
	DurationMs       int64   `json:"duration_ms"`
	// Simulated is always true — a signal to any consumer that this did NOT
	// produce a real run record (no run id, not in metrics/records).
	Simulated bool `json:"simulated"`
	// Warnings surfaces debug-loop hazards — chiefly a prompt that references
	// an upstream `{{ steps.X.output }}` the caller didn't seed via
	// step_outputs, which rendered empty (so you'd be iterating on a prompt
	// that never sees production's real upstream data).
	Warnings []string `json:"warnings,omitempty"`
}

// StepRun POST /workspaces/{wsId}/pipelines/{slug}/step_run
//
// Executes ONE agent_run step against a supplied input fixture — no DAG
// traversal, no upstream steps, no persisted run record. It's the "unit
// test for a step": iterate on one parse/extract prompt in seconds instead
// of running the whole ~8-minute pipeline (dry-run doesn't execute; a full
// run is too slow). Returns the rendered prompt, the step output, the
// validation verdict, and the cost.
//
// Isolation: leaving PipelineRunID/StepID empty on the AgentStepRequest is
// the switch that makes RunStep skip sub-span capture + run.agent_span
// journaling — so a step-run never pollutes run records or metrics. (The
// only persisted side-effect is the synthetic chat row RunStep always
// writes for the conversation store.)
func (h *PipelineHandler) StepRun(w http.ResponseWriter, r *http.Request) {
	// Executing a step spawns an agent call (control-plane) — MANAGER+, same
	// gate as /run.
	if !requireRole(w, r, "create") {
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	if h.runner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "pipeline runner not wired",
			"hint":  "the orchestrator hasn't booted yet, or this build was assembled without the runner adapter",
		})
		return
	}

	var body stepRunRequestBody
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if strings.TrimSpace(body.StepID) == "" {
		replyError(w, http.StatusBadRequest, "step_id is required")
		return
	}

	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("step_run: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}

	dsl, err := pipeline.Parse([]byte(p.DefinitionJSON))
	if err != nil {
		replyError(w, http.StatusInternalServerError, "parse pipeline definition")
		return
	}

	// Linear scan — step-run is intentionally DAG-free (no needs[] traversal).
	var step *pipeline.Step
	for i := range dsl.Steps {
		if dsl.Steps[i].ID == body.StepID {
			step = &dsl.Steps[i]
			break
		}
	}
	if step == nil {
		replyError(w, http.StatusNotFound, "step not found: "+body.StepID)
		return
	}
	if step.Type != pipeline.StepAgentRun {
		replyError(w, http.StatusBadRequest,
			"step_run supports only agent_run steps; "+body.StepID+" is "+string(step.Type))
		return
	}

	// Render the step prompt against the fixture + any seeded upstream outputs.
	// Most non-first steps consume `{{ steps.X.output }}`; step_outputs stands
	// in for the DAG so those steps can be debugged in isolation. WARN loudly
	// when a referenced upstream output wasn't seeded — it rendered empty, so
	// you'd be iterating on a prompt that never sees production's real data.
	var warnings []string
	for _, ref := range pipeline.ReferencedStepOutputs(step.Prompt) {
		if _, ok := body.StepOutputs[ref]; !ok {
			warnings = append(warnings, fmt.Sprintf(
				"prompt references {{ steps.%s.output }} but no --outputs fixture was provided for %q — it rendered empty; the step is seeing different input than a real run",
				ref, ref))
		}
	}
	if len(warnings) > 0 {
		h.logger.Warn("step_run: unseeded upstream output refs", "slug", slug, "step", body.StepID, "count", len(warnings))
	}
	rendered := pipeline.Render(step.Prompt, pipeline.RenderContext{
		Inputs:      body.Inputs,
		StepOutputs: body.StepOutputs,
	})

	// Resolve the tier, mirroring the executor: an explicit step ModelOverride
	// always wins; otherwise an accepted --tier-override replaces complexity.
	stepForResolve := *step
	switch pipeline.Complexity(body.TierOverride) {
	case pipeline.ComplexityTrivial, pipeline.ComplexityFast, pipeline.ComplexityModerate, pipeline.ComplexitySmart:
		if step.ModelOverride == "" {
			stepForResolve.Complexity = pipeline.Complexity(body.TierOverride)
		}
	default:
		// empty or unknown → no override (forgive-and-carry-on, like /run)
	}
	am, _, err := h.resolver.Resolve(r.Context(), workspaceID, stepForResolve)
	if err != nil {
		h.logger.Error("step_run: resolve tier", "error", err, "slug", slug, "step", body.StepID)
		replyError(w, http.StatusInternalServerError, "resolve step model")
		return
	}

	res, err := h.runner.RunStep(r.Context(), pipeline.AgentStepRequest{
		WorkspaceID:  workspaceID,
		AuthorCrewID: p.AuthorCrewID,
		AgentSlug:    step.AgentSlug,
		Adapter:      am.Adapter,
		Model:        am.Model,
		Prompt:       rendered,
		TimeoutSec:   step.TimeoutSec,
		PipelineID:   p.ID,
		// PipelineRunID + StepID deliberately empty → non-persisted simulation.
	})
	if err != nil {
		h.logger.Warn("step_run: execution failed", "error", err, "slug", slug, "step", body.StepID)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "step execution failed",
			"detail": err.Error(),
		})
		return
	}

	valid, reason := pipeline.ValidateStepOutput(res.Output, step.Validation)

	writeJSON(w, http.StatusOK, stepRunResponse{
		StepID:           step.ID,
		StepType:         string(step.Type),
		Adapter:          am.Adapter,
		Model:            am.Model,
		RenderedPrompt:   rendered,
		Output:           res.Output,
		Valid:            valid,
		ValidationReason: reason,
		CostUSD:          res.CostUSD,
		TokensIn:         res.TokensIn,
		TokensOut:        res.TokensOut,
		DurationMs:       res.DurationMs,
		Simulated:        true,
		Warnings:         warnings,
	})
}
