package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// maxForeachItems bounds how many elements a foreach may fan out over. The
// items array is Render()'d from a template that can reference untrusted input
// (a webhook body, an upstream step output), so without a cap a caller could
// drive an unbounded allocation / run explosion. 10k is far above any real
// routine and keeps the pre-sized results slice bounded.
const maxForeachItems = 10000

// runForeachStep executes a foreach step: it renders the items template to a
// JSON array and fans the body out over each element (#1419, part 1).
//
// Concurrency reuses the DAG's bounded-wave discipline — a buffered-channel
// semaphore caps how many items run at once (Foreach.Parallelism, clamped to
// dagWaveConcurrency) so a 500-element array can't spawn 500 concurrent crew
// runtimes and stampede the provider. Items run their body SEQUENTIALLY within
// the item; failure of one item cancels the rest (fail-fast, like the DAG).
// Cost is summed across items and returned as the step's cost so max_cost_usd
// bounds the whole fan-out. The output is the JSON array of per-item results
// (each item's last body-step output, parsed as JSON when it is valid JSON,
// else kept as a raw string) in input order.
func (e *Executor) runForeachStep(ctx context.Context, step Step, in RunInput, parentRender RenderContext, runID, pipelineID string, emit *pipelineEmitContext, depth int) (string, float64, int64, error) {
	stepStart := time.Now()
	fe := step.Foreach
	if fe == nil {
		return "", 0, 0, fmt.Errorf("foreach step %q missing foreach block", step.ID)
	}

	// Resolve the items template to a JSON array.
	itemsRaw := Render(fe.Items, parentRender)
	items, err := decodeForeachItems(itemsRaw)
	if err != nil {
		return "", 0, 0, fmt.Errorf("foreach step %q: %w", step.ID, err)
	}
	if len(items) == 0 {
		// An empty array is a valid no-op: emit an empty result array.
		return "[]", 0, time.Since(stepStart).Milliseconds(), nil
	}
	if len(items) > maxForeachItems {
		// Bound the fan-out before the per-item allocation below — items is
		// derived from an untrusted template (webhook body / step output).
		return "", 0, 0, fmt.Errorf("foreach step %q: %d items exceeds the maximum of %d — bound the input array", step.ID, len(items), maxForeachItems)
	}

	concurrency := fe.Parallelism
	if concurrency <= 0 || concurrency > dagWaveConcurrency {
		concurrency = dagWaveConcurrency
	}

	type itemResult struct {
		out  string
		cost float64
	}
	results := make([]itemResult, len(items))

	feCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var firstErr atomic.Value // holds *foreachItemErr

	for i := range items {
		if feCtx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(idx int, item any) {
			defer wg.Done()
			defer func() { <-sem }()
			if feCtx.Err() != nil {
				return
			}
			out, cost, ierr := e.runForeachItem(feCtx, step, item, in, parentRender, runID, pipelineID, emit, depth)
			results[idx] = itemResult{out: out, cost: cost}
			if ierr != nil {
				firstErr.CompareAndSwap(nil, &foreachItemErr{index: idx, err: ierr})
				cancel() // fail-fast: stop scheduling further items
			}
		}(i, items[i])
	}
	wg.Wait()

	var total float64
	for _, r := range results {
		total += r.cost
	}
	if f := firstErr.Load(); f != nil {
		fi := f.(*foreachItemErr)
		return "", total, time.Since(stepStart).Milliseconds(),
			fmt.Errorf("foreach step %q: item %d failed: %w", step.ID, fi.index, fi.err)
	}

	// Collect per-item outputs into a JSON array, preserving structure when
	// an item's output is itself valid JSON (so a body producing objects
	// yields [{...},{...}] rather than an array of JSON strings).
	collected := make([]any, len(results))
	for i, r := range results {
		collected[i] = jsonOrString(r.out)
	}
	arr, mErr := json.Marshal(collected)
	if mErr != nil {
		return "", total, time.Since(stepStart).Milliseconds(),
			fmt.Errorf("foreach step %q: marshal results: %w", step.ID, mErr)
	}
	return string(arr), total, time.Since(stepStart).Milliseconds(), nil
}

// foreachItemErr carries the failing item's index alongside its error so the
// aggregate message can point at which element blew up.
type foreachItemErr struct {
	index int
	err   error
}

// runForeachItem runs the foreach body for a single item. The item is bound
// into the body's render context under the foreach's `As` name (default
// "item") as a synthetic input, so body steps read it as {{ inputs.item }}.
// Body steps also see the parent's inputs and every pre-foreach step output
// (seeded into the item's step-output map) plus their own prior body steps.
//
// Each body step goes through the normal per-step machinery (runLinearStep at
// depth+1 — so no run-row persistence, no cancel-classification, but full
// tier resolution / retry / validation / cost accounting). The item's result
// is the LAST body step's output.
func (e *Executor) runForeachItem(ctx context.Context, step Step, item any, in RunInput, parentRender RenderContext, runID, pipelineID string, emit *pipelineEmitContext, depth int) (string, float64, error) {
	fe := step.Foreach
	as := fe.As
	if as == "" {
		as = "item"
	}

	// Per-item inputs = parent inputs + the loop variable.
	itemInputs := make(map[string]any, len(in.Inputs)+1)
	for k, v := range in.Inputs {
		itemInputs[k] = v
	}
	itemInputs[as] = item

	// Per-item step-output map seeded with the parent's outputs so a body
	// step can reference an upstream (pre-foreach) step. Each item gets its
	// OWN map, so parallel items never race on shared state.
	// No size hint: len+len tripped CodeQL's allocation-size-overflow on an
	// unbounded sum; the map is small (per-item, seeded from parent outputs)
	// so pre-sizing is not worth the flagged arithmetic.
	subOutputs := make(map[string]string)
	for k, v := range parentRender.StepOutputs {
		subOutputs[k] = v
	}

	// Sub-DSL wrapping the body so runLinearStep's render context + egress
	// gate see the same routine-level egress allowlist. MaxCostUSD stays 0
	// here — the whole fan-out's spend is bounded by the OUTER step's cost
	// being summed and checked against the run's effective cap.
	subDSL := &DSL{Name: subDSLName(in), EgressTargets: parentRender.EgressTargets, Steps: fe.Steps}

	subIn := in
	subIn.Inputs = itemInputs
	subIn.dsl = subDSL

	renderEnv := parentRender.Env
	runMeta := parentRender.Metadata
	subResult := &RunResult{StepOutputs: subOutputs}
	startedAt := time.Now()

	lastID := ""
	for bi := range fe.Steps {
		bs := fe.Steps[bi]
		lastID = bs.ID
		// depth+1: the body is a nested unit — persistence + cancel
		// classification (top-level only) stay off, exactly as a
		// call_pipeline child.
		if term := e.runLinearStep(ctx, bs, bi, subIn, runID, pipelineID, emit, itemInputs, renderEnv, runMeta, depth+1, subResult, subDSL, startedAt); term != nil {
			switch term.Status {
			case "WAITING":
				return "", subResult.CostUSD, fmt.Errorf("foreach body cannot park on a wait step (step %q)", term.CurrentStep)
			default:
				return "", subResult.CostUSD, fmt.Errorf("body step %q failed: %s", term.FailedAtStep, term.ErrorMessage)
			}
		}
	}
	return subResult.StepOutputs[lastID], subResult.CostUSD, nil
}

// subDSLName returns a stable name for a foreach body sub-DSL — the enclosing
// run's name when known, else a placeholder. Used only for render-context
// plumbing; never persisted.
func subDSLName(in RunInput) string {
	if in.dsl != nil && in.dsl.Name != "" {
		return in.dsl.Name
	}
	return "foreach-body"
}

// decodeForeachItems parses a rendered items template into a slice. The
// rendered value must be a JSON array; anything else (object, scalar, empty)
// is a config error surfaced to the author.
func decodeForeachItems(raw string) ([]any, error) {
	trimmed := raw
	if trimmed == "" {
		return nil, fmt.Errorf("foreach items rendered empty (expected a JSON array)")
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return nil, fmt.Errorf("foreach items is not valid JSON: %w", err)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("foreach items must render to a JSON array, got %T", v)
	}
	return arr, nil
}

// jsonOrString returns the parsed JSON value when s is valid JSON, else the
// raw string — so a body producing structured output collects as structure
// while plain-text output collects as a string.
func jsonOrString(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}
