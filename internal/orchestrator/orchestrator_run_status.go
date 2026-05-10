package orchestrator

// Status update + output-handler wrappers extracted from
// orchestrator_run.go. Pure file move; signatures and behavior unchanged.

import (
	"context"
	"encoding/json"
	"time"
)

func (o *Orchestrator) wrapScrubHandler(handler EventHandler) EventHandler {
	if handler == nil || o.scrubber == nil {
		return handler
	}
	var scrubNotified bool
	return func(event AgentEvent) {
		original := event.Content
		event.Content = o.scrubber.Scrub(event.Content)
		if !scrubNotified && event.Content != original {
			scrubNotified = true
			handler(AgentEvent{
				Type:      "system",
				Content:   "[security] Credential pattern detected in agent output -- redacted by stdout scrubber",
				Timestamp: time.Now(),
			})
			o.logger.Warn("scrubber redacted credential in agent output")
		}
		handler(event)
	}
}

func (o *Orchestrator) updateRunStatus(ctx context.Context, runID, status string) {
	data, err := o.state.Get(ctx, "agent_runs", runID)
	if err != nil {
		o.logger.Error("updateRunStatus: get failed", "run_id", runID, "error", err)
		return
	}
	if data == nil {
		o.logger.Warn("updateRunStatus: run not found", "run_id", runID)
		return
	}
	var run RunState
	if err := json.Unmarshal(data, &run); err != nil {
		o.logger.Error("updateRunStatus: unmarshal failed", "run_id", runID, "error", err)
		return
	}
	run.Status = status
	run.LastActivity = time.Now()
	updated, err := json.Marshal(run)
	if err != nil {
		o.logger.Error("updateRunStatus: marshal failed", "run_id", runID, "error", err)
		return
	}
	if err := o.state.Set(ctx, "agent_runs", runID, updated); err != nil {
		o.logger.Error("updateRunStatus: set failed", "run_id", runID, "error", err)
	}
}
