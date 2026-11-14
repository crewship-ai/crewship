package pipeline

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/artifactrun"
)

// personStartedRoutine reports whether a person started this run directly
// (UI, CLI or API, admitted as routine.run or routine.batch) with no agent in
// the invocation. A call_pipeline expansion (an agent starting a saved
// routine, or a nested call), a schedule (an agent can create one through
// /internal/routines/schedules and schedules carry no creator), a webhook, an
// automation, a wake check or a routine an agent authored never qualifies: an agent must not borrow
// another agent's standing access, or a fresh read cap, through a routine
// (§7.1c item 8).
func personStartedRoutine(req AgentStepRequest) bool {
	if req.InvokingUserID == "" || req.InvokingAgentID != "" || req.AuthorAgentID != "" {
		return false
	}
	if req.InvocationAuthority != RoutineRunAuthority && req.InvocationAuthority != RoutineBatchAuthority {
		return false
	}
	return req.TriggeredVia == "" || req.TriggeredVia == TriggeredViaManual
}

// recordRoutineArtifactRun records a routine step's run as a run without a
// human (#5339) before its agent starts and returns its artifact capability
// plus a function that ends it. "" means the step reaches no artifact.
func recordRoutineArtifactRun(ctx context.Context, db *sql.DB, logger *slog.Logger, req AgentStepRequest, crewID, agentID, runID string, timeout time.Duration) (string, func()) {
	noop := func() {}
	if db == nil || crewID == "" || runID == "" || !personStartedRoutine(req) {
		return "", noop
	}
	source := "routine:" + req.PipelineID + "/" + req.StepID
	if len(source) > 500 {
		source = source[:500]
	}
	capability, err := artifactrun.Record(ctx, db, artifactrun.Run{WorkspaceID: req.WorkspaceID, CrewID: crewID, AgentID: agentID, RunID: runID, Trigger: artifactrun.TriggerRoutine, Source: source}, artifactrun.TTLFor(timeout))
	if err != nil {
		if logger != nil {
			logger.Warn("unattended artifact run not recorded; the step reaches no artifact", "run_id", runID, "error", err)
		}
		return "", noop
	}
	if capability == "" {
		return "", noop
	}
	return capability, func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := artifactrun.End(endCtx, db, runID); err != nil && logger != nil {
			logger.Warn("unattended artifact run end not recorded", "run_id", runID, "error", err)
		}
	}
}
