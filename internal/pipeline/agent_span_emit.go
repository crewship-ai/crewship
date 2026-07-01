package pipeline

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// emitRunAgentSpan persists one captured agent sub-span (a single tool call
// inside an agent_run step) as a journal entry of type run.agent_span. It is
// the storage half of the drillable run-trace tree — the runs API reads these
// back keyed by step_id (see internal/api/pipeline_runs.go loadRunAgentSpans).
//
// The entry's trace_id AND actor_id both anchor to the run id so the runs API
// can pull every sub-span of a run through the existing (workspace_id,
// trace_id) journal index without a payload scan. run_id is ALSO duplicated
// into the payload so the v120 generated run_id column resolves identically.
//
// Best-effort: a nil emitter (journal not wired, dry-run, unit test) is a
// silent no-op, and an Emit error is swallowed — losing a sub-span must never
// fail the routine step that produced it.
func emitRunAgentSpan(ctx context.Context, emitter Emitter, workspaceID, crewID string, span orchestrator.RunAgentSpan) {
	if emitter == nil {
		return
	}
	payload := map[string]any{
		"run_id":      span.RunID,
		"step_id":     span.StepID,
		"seq":         span.Seq,
		"kind":        span.Kind,
		"name":        span.Name,
		"started_at":  span.StartedAt.UTC().Format(time.RFC3339Nano),
		"duration_ms": span.DurationMs,
		"status":      span.Status,
	}
	if span.Detail != "" {
		payload["detail"] = span.Detail
	}
	if len(span.Attributes) > 0 {
		payload["attributes"] = span.Attributes
	}
	sev := journal.SeverityInfo
	if span.Status == "error" {
		sev = journal.SeverityWarn
	}
	_, _ = emitter.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryRunAgentSpan,
		Severity:    sev,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     span.RunID,
		TraceID:     span.RunID,
		Summary:     "agent sub-span " + span.Kind + " " + span.Name,
		Payload:     payload,
	})
}
