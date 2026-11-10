package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func (r *OrchestratorRunner) emitStepAgentWait(ctx context.Context, req AgentStepRequest, waitID, phase string, at time.Time, elapsed time.Duration) {
	if r.journalE == nil || req.PipelineRunID == "" || req.StepID == "" {
		return
	}

	payload := map[string]any{
		"run_id":      req.PipelineRunID,
		"pipeline_id": req.PipelineID,
		"step_id":     req.StepID,
		"agent_slug":  req.AgentSlug,
		"wait_id":     waitID,
		"phase":       phase,
	}
	if req.Attempt > 0 {
		payload["attempt"] = req.Attempt
	}
	summary := fmt.Sprintf("Step %s is waiting for agent %s", req.StepID, req.AgentSlug)
	if phase != "waiting" {
		ms := elapsed.Milliseconds()
		payload["duration_ms"] = ms
		switch phase {
		case "acquired":
			summary = fmt.Sprintf("Agent %s acquired for step %s after %dms", req.AgentSlug, req.StepID, ms)
		case "timed_out":
			summary = fmt.Sprintf("Step %s timed out waiting for agent %s after %dms", req.StepID, req.AgentSlug, ms)
		case "cancelled":
			summary = fmt.Sprintf("Step %s stopped waiting for agent %s after %dms", req.StepID, req.AgentSlug, ms)
		}
	}

	// A slow journal must not hold a routine step indefinitely.
	emitCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = r.journalE.Emit(emitCtx, journal.Entry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.AuthorCrewID,
		TS:          at,
		Type:        journal.EntryPipelineStepAgentWait,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     req.PipelineRunID,
		TraceID:     req.PipelineRunID,
		Summary:     summary,
		Payload:     payload,
	})
}
