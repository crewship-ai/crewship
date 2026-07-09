package pipeline

import (
	"context"
	"strconv"

	"github.com/crewship-ai/crewship/internal/journal"
)

// containerReady carries the per-step container-acquire timing the runner
// measures around EnsureCrewRuntime.
type containerReady struct {
	RunID       string
	PipelineID  string
	StepID      string
	ContainerID string
	DurationMs  int64
}

// emitStepContainerReady records how long a routine step spent acquiring its
// crew container (the EnsureCrewRuntime call), as a journal entry keyed to the
// run. This isolates the container-provision cost from the LLM/tool time buried
// in the step's total duration — the exact quantity the #902 prewarm shortens
// (a warm hit is near-zero; a cold provision is seconds). `routine logs`
// surfaces it via the shared duration column, so claim→first-step is a direct
// read rather than a guess (#911).
//
// Best-effort and gated: a nil emitter is a silent no-op, and an entry is only
// emitted for a real routine step (run_id + step_id present) so ad-hoc / chat
// container acquisitions don't pollute the run timeline. An Emit error is
// swallowed — losing the metric must never fail the step that produced it.
func emitStepContainerReady(ctx context.Context, emitter Emitter, workspaceID, crewID string, cr containerReady) {
	if emitter == nil || cr.RunID == "" || cr.StepID == "" {
		return
	}
	payload := map[string]any{
		"run_id":      cr.RunID,
		"pipeline_id": cr.PipelineID,
		"step_id":     cr.StepID,
		"duration_ms": cr.DurationMs,
	}
	if cr.ContainerID != "" {
		payload["container_id"] = cr.ContainerID
	}
	_, _ = emitter.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryPipelineStepContainerReady,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     cr.RunID,
		TraceID:     cr.RunID,
		// Duration in the summary too, so the summary-only run-logs endpoint
		// (and any timeline that doesn't project the payload) still shows the
		// number.
		Summary: "step " + cr.StepID + " container ready (" + strconv.FormatInt(cr.DurationMs, 10) + "ms)",
		Payload: payload,
	})
}
