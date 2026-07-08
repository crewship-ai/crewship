package pipeline

import (
	"context"
	"log/slog"
)

// CrewPrewarmer is the optional capability an AgentRunner exposes to warm a
// crew's container ahead of a run's first step (#836). The production
// OrchestratorRunner implements it; bare test runners don't, so the executor
// treats it as best-effort and simply skips prewarm when the runner can't.
type CrewPrewarmer interface {
	// PrewarmCrew ensures the crew's container is running. It MUST be
	// idempotent under concurrent calls — the provider's per-crew lock
	// collapses N callers for one crew to a single container start — and MUST
	// NOT produce any run/LLM/cost/billing side-effect. It only provisions the
	// runtime.
	PrewarmCrew(ctx context.Context, crewID, workspaceID string) error
}

// PrewarmForRun warms the author crew's container for a pending/scheduled run
// off the critical path, so the run's first agent step doesn't pay cold
// container provisioning inline (#836). It is BEST-EFFORT: any failure is
// logged at debug and swallowed — the run's own EnsureCrewRuntime on the first
// step is the backstop, and a prewarm miss only forfeits the latency it was
// trying to save. It emits no run row, LLM call, or cost event.
//
// Skipped when: the runner can't prewarm (bare/test runner), the routine isn't
// runnable (a proposed/disabled routine won't run, so don't spin a container),
// it has no author crew, or no step uses the crew container (a purely agentless
// http/transform routine needs none).
func (e *Executor) PrewarmForRun(ctx context.Context, pipelineID, workspaceID string) {
	pw, ok := e.runner.(CrewPrewarmer)
	if !ok {
		return
	}
	p, err := e.store.GetByID(ctx, pipelineID)
	if err != nil {
		prewarmDebug("load pipeline", pipelineID, err)
		return
	}
	if !routineStatusRunnable(p.Status) || p.AuthorCrewID == "" {
		return
	}
	dsl, err := Parse([]byte(p.DefinitionJSON))
	if err != nil {
		prewarmDebug("parse dsl", pipelineID, err)
		return
	}
	if !dslUsesCrewContainer(dsl) {
		return
	}
	if err := pw.PrewarmCrew(ctx, p.AuthorCrewID, workspaceID); err != nil {
		prewarmDebug("prewarm crew", pipelineID, err)
	}
}

// dslUsesCrewContainer reports whether any step execs inside the crew's own
// container — agent_run and script do. Pure http/code/transform/wait routines
// never touch it, so prewarming would waste a container start. call_pipeline is
// excluded: it dispatches a nested run in the TARGET routine's author context,
// not this crew's container.
func dslUsesCrewContainer(dsl *DSL) bool {
	for _, s := range dsl.Steps {
		switch s.Type {
		case StepAgentRun, StepScript:
			return true
		}
	}
	return false
}

func prewarmDebug(stage, pipelineID string, err error) {
	slog.Default().Debug("pipeline prewarm skipped",
		"stage", stage, "pipeline_id", pipelineID, "error", err)
}
