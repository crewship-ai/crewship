package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// hasNeeds reports whether any step in the DSL declares an explicit
// `needs:` array. The runDSL dispatcher uses this to decide between
// the linear loop (existing behaviour, all pipelines built before
// `needs:` shipped) and the DAG scheduler.
//
// We don't switch on a separate top-level flag because we want the
// upgrade to be transparent: the moment an author adds `needs:` to
// any step, parallel execution of independent branches kicks in
// without further configuration.
func hasNeeds(dsl *DSL) bool {
	for i := range dsl.Steps {
		if len(dsl.Steps[i].Needs) > 0 {
			return true
		}
	}
	return false
}

// validateDAG checks every Needs entry references a real step and
// that there are no cycles. Cycle detection rides on the same Kahn-
// style topological pass the scheduler uses, so a clean validate
// pass guarantees scheduler progress.
//
// Errors here surface at runtime — Save-time validation in dsl.go
// already catches obvious mistakes, but a step rename can leave
// stale Needs entries that only break at execution.
func validateDAG(dsl *DSL) error {
	known := make(map[string]bool, len(dsl.Steps))
	for i := range dsl.Steps {
		known[dsl.Steps[i].ID] = true
	}
	for i := range dsl.Steps {
		s := &dsl.Steps[i]
		for _, dep := range s.Needs {
			if dep == s.ID {
				return fmt.Errorf("step %q depends on itself", s.ID)
			}
			if !known[dep] {
				return fmt.Errorf("step %q needs unknown step %q", s.ID, dep)
			}
		}
	}
	// Topological sort (Kahn) for cycle detection
	indegree := make(map[string]int, len(dsl.Steps))
	for i := range dsl.Steps {
		indegree[dsl.Steps[i].ID] = len(dsl.Steps[i].Needs)
	}
	// children[parent] = []children
	children := make(map[string][]string, len(dsl.Steps))
	for i := range dsl.Steps {
		s := &dsl.Steps[i]
		for _, dep := range s.Needs {
			children[dep] = append(children[dep], s.ID)
		}
	}
	queue := make([]string, 0, len(dsl.Steps))
	for id, deg := range indegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	processed := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		processed++
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if processed != len(dsl.Steps) {
		return errors.New("pipeline DAG has a cycle")
	}
	return nil
}

// dagStepFailure is the atomic-Value payload that the DAG goroutines
// write into firstErr. Lifted to package scope so the type is the
// same identity inside the runDAG loop and inside executeOneStep —
// otherwise the type assertion `f.(*dagStepFailure)` would mismatch.
type dagStepFailure struct {
	stepID    string
	message   string
	isCostCap bool
}

// runDAG schedules the steps as a DAG: ready steps (all Needs
// satisfied) run in parallel; the loop advances when any wave
// completes. First step error cancels the run-scoped context so
// in-flight peers exit early; the final result records the first
// failure as FailedAtStep.
//
// We do NOT support DAG dry-run or DAG call_pipeline in this MVP —
// dry-run reports a linear preview (graph rendering is the UI's
// concern), and call_pipeline forces sequential mode for the
// nested call. That's enforced by the validator at hasNeeds() +
// step type check below.
func (e *Executor) runDAG(
	ctx context.Context,
	in RunInput,
	depth int,
	dsl *DSL,
	result *RunResult,
	pipelineID, pipelineSlug, runID string,
	emit *pipelineEmitContext,
	inputsForCtx map[string]any,
	renderEnv map[string]string,
	startedAt time.Time,
) (*RunResult, error) {
	if err := validateDAG(dsl); err != nil {
		result.Status = "FAILED"
		result.ErrorMessage = err.Error()
		if len(dsl.Steps) > 0 {
			result.FailedAtStep = dsl.Steps[0].ID
		}
		emit.emitRunFailed(ctx, result.FailedAtStep, err.Error())
		result.DurationMs = time.Since(startedAt).Milliseconds()
		return result, nil
	}

	// DAG mode forbids call_pipeline (would need its own nested DAG
	// scheduler — out of scope for this PR). Surface a clear error
	// so the author can refactor rather than seeing weird behaviour.
	for i := range dsl.Steps {
		if dsl.Steps[i].Type == StepCallPipeline {
			err := fmt.Errorf("step %q: call_pipeline cannot be used inside a DAG (steps with `needs:`); split into a separate sequential pipeline", dsl.Steps[i].ID)
			result.Status = "FAILED"
			result.FailedAtStep = dsl.Steps[i].ID
			result.ErrorMessage = err.Error()
			emit.emitRunFailed(ctx, result.FailedAtStep, err.Error())
			result.DurationMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}
	}

	// Per-step state. completed: step has either finished
	// successfully OR was deliberately skipped (output recorded as
	// "<skipped>"). The DAG advances on completion regardless of
	// skip state — downstream steps see the skip output and can
	// branch on it via their own `if:`.
	completed := make(map[string]bool, len(dsl.Steps))
	stepByID := make(map[string]*Step, len(dsl.Steps))
	stepIndex := make(map[string]int, len(dsl.Steps))
	for i := range dsl.Steps {
		stepByID[dsl.Steps[i].ID] = &dsl.Steps[i]
		stepIndex[dsl.Steps[i].ID] = i
	}

	// Run-scoped cancel for fail-fast across the wave.
	dagCtx, dagCancel := context.WithCancel(ctx)
	defer dagCancel()

	// Mutex protects result.StepOutputs + result.CostUSD because
	// multiple goroutines write into them.
	var resMu sync.Mutex
	var firstErr atomic.Value // holds *dagStepFailure

	for {
		// Compute the ready set: completed[needs[*]] && !completed[id]
		ready := make([]*Step, 0)
		resMu.Lock()
		for i := range dsl.Steps {
			s := &dsl.Steps[i]
			if completed[s.ID] {
				continue
			}
			ok := true
			for _, dep := range s.Needs {
				if !completed[dep] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, s)
			}
		}
		resMu.Unlock()
		if len(ready) == 0 {
			break
		}

		// Bail before spawning a wave if a prior wave already
		// failed. We finish the in-flight wave but don't start the
		// next one.
		if firstErr.Load() != nil {
			break
		}

		var wg sync.WaitGroup
		for _, sp := range ready {
			step := sp
			wg.Add(1)
			go func() {
				defer wg.Done()
				if dagCtx.Err() != nil {
					return
				}
				e.executeOneStep(dagCtx, step, stepIndex[step.ID], in, runID, pipelineID, emit, inputsForCtx, renderEnv, depth, &resMu, result, dsl, &firstErr, dagCancel)
			}()
		}
		wg.Wait()

		// Only mark a step completed if it actually produced an
		// output. Steps that returned early due to dagCtx cancel
		// (a peer in the wave failed) left no entry in StepOutputs,
		// and treating them as completed would let downstream
		// branches advance with empty inputs on a future wave. The
		// bail-out at firstErr below already handles fail-fast, but
		// defense-in-depth keeps the invariant clean if someone later
		// removes the bail.
		resMu.Lock()
		for _, sp := range ready {
			if _, ok := result.StepOutputs[sp.ID]; ok {
				completed[sp.ID] = true
			}
		}
		resMu.Unlock()

		if f := firstErr.Load(); f != nil {
			fail := f.(*dagStepFailure)
			result.Status = "FAILED"
			result.FailedAtStep = fail.stepID
			result.ErrorMessage = fail.message
			emit.emitRunFailed(ctx, fail.stepID, fail.message)
			if in.Mode == ModeRun && in.pipeline != nil {
				_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "FAILED")
			}
			result.DurationMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}
	}

	// Final output: prefer leaf nodes (steps that no other step
	// references via `needs`) — they're the actual terminals of the
	// DAG. With multiple leaves we take the first leaf in source
	// order so authors can deterministically position the "primary"
	// terminal first. Fall back to last-non-skipped in source order
	// for linear pipelines (zero `needs` everywhere) so behaviour
	// stays unchanged for them.
	resMu.Lock()
	referenced := make(map[string]bool, len(dsl.Steps))
	hasNeeds := false
	for _, s := range dsl.Steps {
		if len(s.Needs) > 0 {
			hasNeeds = true
			for _, dep := range s.Needs {
				referenced[dep] = true
			}
		}
	}
	if hasNeeds {
		// Walk leaves in source order so the first non-empty leaf
		// wins — predictable for authors.
		for _, s := range dsl.Steps {
			if referenced[s.ID] {
				continue
			}
			out := result.StepOutputs[s.ID]
			if out != "" && out != "<skipped>" {
				result.Output = out
				break
			}
		}
	}
	if result.Output == "" {
		// Linear-pipeline fallback (or no leaf had output).
		for i := len(dsl.Steps) - 1; i >= 0; i-- {
			out := result.StepOutputs[dsl.Steps[i].ID]
			if out != "" && out != "<skipped>" {
				result.Output = out
				break
			}
		}
	}
	resMu.Unlock()

	result.DurationMs = time.Since(startedAt).Milliseconds()
	result.Status = "COMPLETED"
	emit.emitRunCompleted(ctx, result.DurationMs, result.CostUSD)
	if in.Mode == ModeRun && in.pipeline != nil {
		_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "COMPLETED")
	}
	return result, nil
}

// executeOneStep is the per-step execution body shared by the DAG
// scheduler. Mirrors the body of the linear loop in runDSL, with
// two concessions to parallelism:
//
//  1. result.StepOutputs / result.CostUSD writes go through resMu.
//  2. On error (or cost cap trip) the helper records into firstErr
//     atomically and calls dagCancel to fail-fast peers. Per-step
//     return values would race against each other; the atomic-Value
//     pattern is the cleanest safe path.
//
// We re-render the step's prompt + If condition INSIDE the goroutine
// against a fresh snapshot of result.StepOutputs so a step that
// became ready after a peer wrote its output sees that output
// (otherwise a render done before the wave would miss the prior
// wave's outputs entirely).
func (e *Executor) executeOneStep(
	ctx context.Context,
	step *Step,
	stepIdx int,
	in RunInput,
	runID, pipelineID string,
	emit *pipelineEmitContext,
	inputsForCtx map[string]any,
	renderEnv map[string]string,
	depth int,
	resMu *sync.Mutex,
	result *RunResult,
	dsl *DSL,
	firstErr *atomic.Value,
	dagCancel context.CancelFunc,
) {
	// Render against a fresh snapshot of step outputs so peer-wave
	// outputs are visible to this step's templates.
	resMu.Lock()
	outputsSnap := make(map[string]string, len(result.StepOutputs))
	for k, v := range result.StepOutputs {
		outputsSnap[k] = v
	}
	resMu.Unlock()
	ctxRender := RenderContext{
		Inputs:      inputsForCtx,
		StepOutputs: outputsSnap,
		Env:         renderEnv,
	}
	renderedPrompt := Render(step.Prompt, ctxRender)

	// Conditional skip — same semantics as the linear path.
	if step.If != "" {
		if !evalIfCondition(Render(step.If, ctxRender)) {
			emit.emitStepSkipped(ctx, *step, step.If)
			resMu.Lock()
			result.StepOutputs[step.ID] = "<skipped>"
			resMu.Unlock()
			return
		}
	}

	tier, fallback, err := e.resolver.Resolve(ctx, in.WorkspaceID, *step)
	if err != nil {
		f := &dagStepFailure{stepID: step.ID, message: "tier resolver: " + err.Error()}
		emit.emitStepFailed(ctx, *step, "tier_resolution", err.Error())
		firstErr.CompareAndSwap(nil, f)
		dagCancel()
		return
	}

	emit.emitStepStarted(ctx, *step, stepIdx, tier)

	output, stepCost, stepDur, stepErr := e.runStepWithRetry(ctx, *step, renderedPrompt, tier, fallback, in, runID, pipelineID, emit, ctxRender, depth)
	if stepErr != nil {
		f := &dagStepFailure{stepID: step.ID, message: stepErr.Error()}
		firstErr.CompareAndSwap(nil, f)
		dagCancel()
		return
	}
	resMu.Lock()
	result.StepOutputs[step.ID] = output
	result.CostUSD += stepCost
	costNow := result.CostUSD
	resMu.Unlock()
	emit.emitStepCompleted(ctx, *step, output, stepDur, stepCost)

	// Cost-cap gate (post-step). The check reads from the locked
	// snapshot above so two parallel completions can't both miss
	// the cap by tripping the gate against a stale total.
	if dsl.MaxCostUSD > 0 && costNow > dsl.MaxCostUSD {
		f := &dagStepFailure{
			stepID:    step.ID,
			message:   fmt.Sprintf("cost cap exceeded: $%.4f > $%.4f after step %q", costNow, dsl.MaxCostUSD, step.ID),
			isCostCap: true,
		}
		firstErr.CompareAndSwap(nil, f)
		dagCancel()
	}
}
