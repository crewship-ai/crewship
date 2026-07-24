package pipeline

import (
	"context"
	"fmt"
)

// DeterministicStepTypes are the step types RunDeterministicStep accepts —
// exactly the ones whose runners never touch an LLM (#1423 item 3): http,
// script, transform. code is deliberately NOT included yet — it needs a
// sandboxed container runner wired the same way script does, and item 3
// scoped step-run's extension to http/script/transform specifically.
var DeterministicStepTypes = map[StepType]bool{
	StepHTTP:      true,
	StepScript:    true,
	StepTransform: true,
}

// RunDeterministicStep executes ONE http, script, or transform step in
// isolation, reusing the exact same runner methods (runHTTPStep /
// runScriptStep / runTransformStep) the real DAG dispatch loop
// (dispatchStep) uses for these step types — not a second, reimplemented
// execution path. This is the step-run counterpart to runAgentStep for
// step types that don't invoke an agent: `routine step-run` (#1423 item 3)
// previously covered only agent_run, leaving the cheapest, most
// deterministic step types — exactly the ones a routine author most wants
// to unit-test in isolation — impossible to debug without running the
// whole pipeline.
//
// No DAG traversal, no persisted run record. renderCtx and in carry the
// same fields the real dispatch loop threads through (Inputs, StepOutputs,
// EgressTargets, WorkspaceID, AuthorCrewID, ...) — the caller is
// responsible for populating them so a step-run behaves identically to how
// the step would inside a real run (in particular: renderCtx.EgressTargets
// should be the routine's dsl.EgressTargets, so the http step's egress
// policy check sees the same "does this routine declare its own allowlist"
// signal a real run would).
//
// runID is threaded only to runScriptStep, which uses it solely to tag its
// exec.command audit journal entry (emitScriptAudit) — pass "" for a
// step-run, matching how the agent_run step-run path leaves
// PipelineRunID/StepID empty on AgentStepRequest to skip run-record
// journaling. The script itself still executes for real, in the crew's
// container, with real side effects; only the "this belongs to a run"
// bookkeeping is absent.
func (e *Executor) RunDeterministicStep(ctx context.Context, step Step, renderCtx RenderContext, in RunInput, runID string) (output string, costUSD float64, durationMs int64, err error) {
	switch step.Type {
	case StepHTTP:
		return e.runHTTPStep(ctx, step, renderCtx, in)
	case StepTransform:
		return e.runTransformStep(step, renderCtx)
	case StepScript:
		return e.runScriptStep(ctx, step, renderCtx, in, runID)
	default:
		return "", 0, 0, fmt.Errorf(
			"RunDeterministicStep: step type %q is not deterministic (supports http, script, transform — agent_run has its own step-run path, everything else needs a real run)",
			step.Type)
	}
}
