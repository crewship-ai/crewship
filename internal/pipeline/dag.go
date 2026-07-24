package pipeline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// dagWaveConcurrency caps how many steps in a DAG wave execute at once.
// Each step may provision a crew runtime and hit the LLM provider, so an
// unbounded wide wave stampedes into rate limits. Sized in the 8–16 band:
// high enough that typical fan-outs run fully parallel, low enough not to
// trip provider 429s.
const dagWaveConcurrency = 12

// Parallelism modes (DSL.Parallelism). Empty resolves to explicit.
const (
	ParallelismExplicit = "explicit"
	ParallelismAuto     = "auto"
	ParallelismOff      = "off"
)

// parallelismMode resolves DSL.Parallelism to a canonical mode; empty (or
// unknown, which the validator rejects at save time) ⇒ explicit — today's
// behaviour, so existing routines are unaffected.
func parallelismMode(dsl *DSL) string {
	switch dsl.Parallelism {
	case ParallelismAuto:
		return ParallelismAuto
	case ParallelismOff:
		return ParallelismOff
	default:
		return ParallelismExplicit
	}
}

// stepRefRE matches a template reference to another step's output, e.g.
// {{ steps.parse.output }} captures "parse".
var stepRefRE = regexp.MustCompile(`steps\.([a-zA-Z0-9_-]+)`)

// hasCallPipeline reports whether any step is a call_pipeline — those
// can't run inside a DAG (they need their own nested scheduler), so
// parallelism:auto falls back to linear when one is present.
func hasCallPipeline(dsl *DSL) bool {
	for i := range dsl.Steps {
		if dsl.Steps[i].Type == StepCallPipeline {
			return true
		}
	}
	return false
}

// deriveAutoNeeds returns a copy of dsl whose steps have Needs populated
// from data-flow references ({{ steps.X }}), for parallelism:auto. A step
// that already declares Needs keeps them (author intent wins); self- and
// unknown references are ignored. The original DSL is not mutated — the
// derived copy is handed to the DAG scheduler, which re-renders each step
// against a fresh snapshot, so a step with no data reference simply sees
// empty upstream (exactly as today) if it was mis-grouped.
func deriveAutoNeeds(dsl *DSL) *DSL {
	ids := make(map[string]bool, len(dsl.Steps))
	for i := range dsl.Steps {
		ids[dsl.Steps[i].ID] = true
	}
	out := *dsl
	out.Steps = make([]Step, len(dsl.Steps))
	copy(out.Steps, dsl.Steps)
	for i := range out.Steps {
		st := &out.Steps[i]
		if len(st.Needs) > 0 {
			continue
		}
		seen := map[string]bool{}
		var needs []string
		for _, m := range stepRefRE.FindAllSubmatch(st.Raw, -1) {
			ref := string(m[1])
			if ref == st.ID || !ids[ref] || seen[ref] {
				continue
			}
			seen[ref] = true
			needs = append(needs, ref)
		}
		sort.Strings(needs)
		st.Needs = needs
	}
	return &out
}

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
	runMeta map[string]any,
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

	// Boot-time resume: steps restored from the previous lifetime
	// already completed — seed them into the completed set so the
	// ready-set computation schedules only unfinished steps. Their
	// outputs were seeded into result.StepOutputs by runDSL, so
	// downstream templates render against the completed work.
	if in.resume {
		for id := range in.restoredOutputs {
			if _, ok := stepByID[id]; ok {
				completed[id] = true
			}
		}
	}

	// Run-scoped cancel for fail-fast across the wave.
	dagCtx, dagCancel := context.WithCancel(ctx)
	defer dagCancel()

	// Mutex protects result.StepOutputs + result.CostUSD because
	// multiple goroutines write into them.
	var resMu sync.Mutex
	var firstErr atomic.Value  // holds *dagStepFailure
	var suspended atomic.Value // holds *suspendError (wait-step park; not a failure)

	// Bound wave fan-out: a wide wave (e.g. 50 independent steps) would
	// otherwise spawn one goroutine per step, each calling EnsureCrewRuntime
	// + RunAgent — stampeding the provider into 429s (backoff → slower than
	// serial). A buffered-channel semaphore caps how many steps execute at
	// once; the slot is released as each step finishes. Shared across waves
	// (waves are sequential — wg.Wait between them — so reuse is safe).
	sem := make(chan struct{}, dagWaveConcurrency)

	for {
		// Between-wave cancel pre-emption (#1424). Without this, a run
		// cancelled AFTER a wave completes but BEFORE the next is scheduled
		// recomputes the ready set and spawns the next wave's goroutines,
		// which all short-circuit on dagCtx.Err() WITHOUT recording an
		// output or setting firstErr — so `completed`/`ready` never change
		// and the loop respawns forever at full CPU. Run() then never
		// returns and the deferred release() never frees the concurrency
		// slot. Mirror the linear loop's between-step cancel exit: stamp a
		// terminal FAILED result (Run() re-labels to CANCELLED when the
		// registry confirms a user cancel) and return.
		if cerr := ctx.Err(); cerr != nil {
			return e.failRun(ctx, in, emit, result, "", cerr.Error(), false, startedAt), nil
		}

		// Compute the ready set: completed[needs[*]] && !completed[id]
		ready := make([]*Step, 0, len(dsl.Steps))
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
			// Acquire a bounded slot before spawning. A cancelled DAG
			// (peer failed) still drains: every spawned goroutine releases,
			// so the acquire always makes progress; remaining steps spawn,
			// see dagCtx.Err(), and return fast.
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if dagCtx.Err() != nil {
					return
				}
				e.executeOneStep(dagCtx, step, stepIndex[step.ID], in, runID, pipelineID, emit, inputsForCtx, renderEnv, runMeta, depth, &resMu, result, dsl, &firstErr, &suspended, dagCancel, startedAt)
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
		// Snapshot under the lock for the wave-boundary flush below —
		// holding resMu across a DB write would stall peers.
		outputsSnap := make(map[string]string, len(result.StepOutputs))
		for k, v := range result.StepOutputs {
			outputsSnap[k] = v
		}
		costSnap := result.CostUSD
		resMu.Unlock()

		// Flush persisted step state at the wave boundary so a hard
		// kill mid-DAG loses at most the in-flight wave. DAG resume
		// granularity is therefore per-wave, not per-step — the
		// parallel scheduler has no single current_step_id to stamp.
		e.persistWaveOutputs(ctx, in, depth, runID, outputsSnap, costSnap, startedAt)

		// Suspend takes precedence over a failure: a wait step parked on an
		// approval (MarkWaiting already stamped status=waiting + current_step
		// inside runWaitStep). Return WAITING promptly and release the slot;
		// the approve handler (or boot scan) resumes from the restored
		// outputs. Checked before firstErr so a sibling cancelled by the
		// suspend's dagCancel doesn't mask the park as a failure.
		if s := suspended.Load(); s != nil {
			susp := s.(*suspendError)
			result.Status = "WAITING"
			result.CurrentStep = susp.stepID
			result.WaitpointToken = susp.token
			result.DurationMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}

		if f := firstErr.Load(); f != nil {
			fail := f.(*dagStepFailure)
			return e.failRun(ctx, in, emit, result, fail.stepID, fail.message, true, startedAt), nil
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
	e.completeRun(ctx, in, emit, result)
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
	runMeta map[string]any,
	depth int,
	resMu *sync.Mutex,
	result *RunResult,
	dsl *DSL,
	firstErr *atomic.Value,
	suspended *atomic.Value,
	dagCancel context.CancelFunc,
	startedAt time.Time,
) {
	// Render against a fresh snapshot of step outputs so peer-wave
	// outputs are visible to this step's templates.
	resMu.Lock()
	outputsSnap := make(map[string]string, len(result.StepOutputs))
	for k, v := range result.StepOutputs {
		outputsSnap[k] = v
	}
	resMu.Unlock()
	ctxRender := buildStepRenderContext(inputsForCtx, outputsSnap, renderEnv, runMeta, dsl.EgressTargets, in.stateSnapshot)
	renderedPrompt := renderAgentPrompt(*step, ctxRender, in.TriggeredVia)

	// Conditional skip — same semantics as the linear path.
	if step.If != "" {
		if !evalStepCondition(step.If, ctxRender) {
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

	// Snapshot the run cost so the retry loop's cost-cap check reads a
	// stable prior total (concurrent siblings mutate result.CostUSD).
	resMu.Lock()
	priorCost := result.CostUSD
	resMu.Unlock()
	output, stepCost, stepDur, stepErr := e.runStepWithRetry(ctx, *step, renderedPrompt, tier, fallback, in, runID, pipelineID, emit, ctxRender, depth, priorCost)
	if stepErr != nil {
		// Suspend (wait-step park) is not a failure: record it separately and
		// stop scheduling. The runDAG loop checks `suspended` before firstErr.
		var susp *suspendError
		if errors.As(stepErr, &susp) {
			suspended.CompareAndSwap(nil, susp)
			dagCancel()
			return
		}
		// Keep the failed attempt's spend — runStepWithRetry reports the
		// cost of every tier it burned alongside the error. Mirrors the
		// sequential runDSL failure branch; without it failed DAG runs
		// persisted cost_usd=0.
		resMu.Lock()
		result.CostUSD += stepCost
		resMu.Unlock()
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
	// Per-step incremental durability flush (#1428, 2.8). DAG persistence was
	// per-WAVE only, so a hard kill mid-wave lost every already-completed step
	// in that wave and replayed them all on resume — up to a dozen
	// non-idempotent POSTs / notifies fired twice. Flushing after each step
	// lands it durably so resume skips it. #1411 normalized step outputs into
	// their own table, so this is a single-row upsert of THIS step (its own
	// goroutine owns `output`/`step.ID`; `costNow` was snapshotted under the
	// lock) rather than the old whole-map rewrite — per-step granularity
	// without the O(N²) blob churn. The wave-boundary flush
	// (persistWaveOutputs) still re-upserts the full set. persistStepOutput
	// scrubs the persisted copy (#1416 item 5).
	e.persistStepOutput(ctx, in, depth, runID, step.ID, output, costNow, startedAt)
	// State_write bindings persist for the NEXT run (#1420). Add this step's
	// own output to the goroutine-local snapshot so a value template can
	// reference it, then render+upsert off the shared lock.
	if len(step.StateWrite) > 0 {
		outputsSnap[step.ID] = output
		e.persistStateWrites(ctx, *step, in, buildStepRenderContext(inputsForCtx, outputsSnap, renderEnv, runMeta, dsl.EgressTargets, in.stateSnapshot))
	}
	// #1416 item 5: the journal/broadcast copy is scrubbed; the in-memory
	// result.StepOutputs entry above stays raw for downstream template chaining.
	emit.emitStepCompleted(ctx, *step, scrubStepOutput(output), stepDur, stepCost)

	// Cost-cap gate (post-step). The check reads from the locked
	// snapshot above so two parallel completions can't both miss
	// the cap by tripping the gate against a stale total. The cap is
	// the run's EFFECTIVE ceiling (own max_cost_usd tightened by any
	// ancestor call_pipeline budget, #1427 2.4) — though a DAG never
	// contains a call_pipeline step so remainingBudget is normally 0
	// here, reading it keeps the linear and DAG gates identical.
	if cap := effectiveCostCap(in); cap > 0 && costNow > cap {
		f := &dagStepFailure{
			stepID:    step.ID,
			message:   costCapExceededMessage(costNow, cap, step.ID),
			isCostCap: true,
		}
		firstErr.CompareAndSwap(nil, f)
		dagCancel()
	}
}
