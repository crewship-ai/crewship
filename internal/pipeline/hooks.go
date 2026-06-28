package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
)

// parseRunMetadata decodes a run's metadata_json into a map for
// {{ run.metadata.x }} templating. Empty/invalid → nil (lookups miss
// cleanly).
func parseRunMetadata(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// runRoutineHook executes a single lifecycle hook step (Wave 4.1). Hooks
// are deterministic side-channels: only code | http | transform are
// allowed (no agent_run — hooks must not recurse or spend tokens). The
// hook renders against a minimal context built from the run's inputs +
// safe env, NOT the main run's step outputs (a hook is a wrapper, not a
// pipeline step). Returns the hook's output + error.
func (e *Executor) runRoutineHook(ctx context.Context, hook *Step, in RunInput, runID, pipelineSlug string) (string, error) {
	if hook == nil {
		return "", nil
	}
	// Hooks run at the Run() boundary — BEFORE runDSL merges input
	// defaults — so merge here too, else a hook referencing a defaulted
	// input (inputs.x where x has a default) sees it as undefined.
	inputs := in.Inputs
	if in.dsl != nil {
		inputs = mergeInputs(in.Inputs, in.dsl)
	}
	render := RenderContext{
		Inputs:      inputs,
		StepOutputs: map[string]string{},
		Env: map[string]string{
			"run_id":           runID,
			"pipeline_slug":    pipelineSlug,
			"invoking_crew_id": in.InvokingCrewID,
			"is_replay":        boolToEnvStr(in.IsReplay),
		},
	}
	switch hook.Type {
	case StepHTTP:
		out, _, _, err := e.runHTTPStep(ctx, *hook, render)
		return out, err
	case StepCode:
		out, _, _, err := e.runCodeStep(ctx, *hook, render, in)
		return out, err
	case StepTransform:
		out, _, _, err := e.runTransformStep(*hook, render)
		return out, err
	default:
		// Validation rejects these at save time; this is the runtime
		// belt-and-braces for a definition that smuggled one past.
		return "", fmt.Errorf("hook step %q type %q not allowed (use code, http, or transform)", hook.ID, hook.Type)
	}
}

// runHooksAround wraps a main-execution function with the routine's
// lifecycle hooks. before_all runs first; its failure aborts the run
// (returns a FAILED result without executing the body). after_all runs
// on COMPLETED, on_failure on FAILED — both best-effort (logged, never
// override the outcome). Hooks are skipped on resume re-entry and in
// dry-run. nil DSL/Hooks → body runs unchanged.
func (e *Executor) runHooksAround(ctx context.Context, in RunInput, runID, pipelineSlug string, body func() (*RunResult, error)) (*RunResult, error) {
	hooks := (*RoutineHooks)(nil)
	if in.dsl != nil {
		hooks = in.dsl.Hooks
	}
	// No hooks, or a context where hooks shouldn't fire (resume re-entry,
	// dry-run): run the body untouched.
	if hooks == nil || in.resume || in.Mode == ModeDryRun {
		return body()
	}

	if hooks.BeforeAll != nil {
		if _, err := e.runRoutineHook(ctx, hooks.BeforeAll, in, runID, pipelineSlug); err != nil {
			e.persistWarn("hook before_all", runID, err)
			return &RunResult{
				RunID:        runID,
				PipelineID:   in.PipelineID,
				PipelineSlug: pipelineSlug,
				Status:       "FAILED",
				FailedAtStep: hooks.BeforeAll.ID,
				ErrorMessage: fmt.Sprintf("before_all hook failed: %v", err),
			}, nil
		}
	}

	res, err := body()

	// after_all / on_failure are best-effort: a failing teardown hook is
	// logged but never flips the run's outcome.
	if err == nil && res != nil {
		switch res.Status {
		case "COMPLETED":
			if hooks.AfterAll != nil {
				if _, herr := e.runRoutineHook(ctx, hooks.AfterAll, in, runID, pipelineSlug); herr != nil {
					e.persistWarn("hook after_all", runID, herr)
				}
			}
		case "FAILED":
			if hooks.OnFailure != nil {
				if _, herr := e.runRoutineHook(ctx, hooks.OnFailure, in, runID, pipelineSlug); herr != nil {
					e.persistWarn("hook on_failure", runID, herr)
				}
			}
		}
	}
	return res, err
}
