package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// ErrPipelineNotFound is returned by Run when a call_pipeline step
// references a slug that is not registered in the workspace.
var ErrPipelineNotFound = errors.New("pipeline: target pipeline not found")

// ErrMaxDepthExceeded is returned when call_pipeline recursion goes
// deeper than MaxNestedPipelineDepth. Save-time cycle detection
// catches loops, but a long chain (A→B→C→...→Z) is legal there and
// only flagged at runtime.
var ErrMaxDepthExceeded = fmt.Errorf("pipeline: max nested depth %d exceeded", MaxNestedPipelineDepth)

// Executor runs a parsed DSL against an AgentRunner, emitting journal
// entries as it goes. One Executor instance is reusable across many
// pipeline runs — the per-run state lives in Run's stack frame, not
// on the receiver.
//
// Executor does NOT own the DB. The store, resolver, runner, and
// emitter are all injected so the executor can be unit-tested with
// in-memory fakes and deployed in production with the real wires.
type Executor struct {
	store    *Store
	resolver *Resolver
	pipes    PipelineResolver // for call_pipeline lookups; usually == store
	runner   AgentRunner
	emitter  Emitter
}

// NewExecutor wires the dependencies together. emitter and pipes may
// be nil — emitter falls back to nopEmitter, pipes to store. runner
// is required (the executor cannot run agent_run without it); pass a
// stub if you only intend to use ModeDryRun.
func NewExecutor(store *Store, resolver *Resolver, runner AgentRunner, emitter Emitter) *Executor {
	return &Executor{
		store:    store,
		resolver: resolver,
		pipes:    store, // default: same store satisfies PipelineResolver
		runner:   runner,
		emitter:  ensureEmitter(emitter),
	}
}

// WithPipelineResolver overrides the default pipes resolver. Used by
// tests to inject a fake that returns a hand-built DSL for nested
// pipelines without DB writes.
func (e *Executor) WithPipelineResolver(p PipelineResolver) *Executor {
	e.pipes = p
	return e
}

// Run executes a saved pipeline by id. Loads the pipeline, parses its
// DSL, and dispatches to runDSL. Production callers (sidecar handler,
// main API handler) hit this path; tests can also exercise runDSL
// directly with an in-memory DSL.
func (e *Executor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	if in.Mode == "" {
		in.Mode = ModeRun
	}
	p, err := e.store.GetByID(ctx, in.PipelineID)
	if err != nil {
		return nil, fmt.Errorf("executor: load pipeline: %w", err)
	}
	dsl, err := Parse([]byte(p.DefinitionJSON))
	if err != nil {
		return nil, fmt.Errorf("executor: parse stored DSL: %w", err)
	}
	in.pipeline = p
	in.dsl = dsl
	return e.runDSL(ctx, in, 0)
}

// RunDefinition executes an in-memory DSL without a persisted
// pipeline row. Used by the test_run gate before save and by
// dry-run preview against unsaved drafts.
//
// authorCrewID, authorAgentID, and workspaceID must be supplied
// since there's no pipelines row to read them from. The resulting
// run is journaled with a synthetic pipeline_id ("draft-" + uuid)
// so observers can tell drafts from saved pipelines.
func (e *Executor) RunDefinition(ctx context.Context, dsl *DSL, in RunInput) (*RunResult, error) {
	if in.Mode == "" {
		in.Mode = ModeTestRun
	}
	if in.WorkspaceID == "" {
		return nil, errors.New("executor: workspace_id required for RunDefinition")
	}
	if in.AuthorCrewID == "" {
		return nil, errors.New("executor: author_crew_id required for RunDefinition")
	}
	in.dsl = dsl
	in.pipeline = nil // unsaved
	return e.runDSL(ctx, in, 0)
}

// RunInput carries everything the executor needs to start a run.
// The unexported pipeline + dsl fields are populated internally by
// Run / RunDefinition; callers leave them zero.
type RunInput struct {
	PipelineID      string // optional; required only for Run (not RunDefinition)
	WorkspaceID     string
	AuthorCrewID    string // populated from pipeline row in Run
	AuthorAgentID   string
	InvokingCrewID  string
	InvokingAgentID string
	Inputs          map[string]any
	Mode            RunMode
	pipeline        *Pipeline
	dsl             *DSL
}

// runDSL is the actual step loop. depth bounds call_pipeline recursion
// across nested invocations; the top-level Run starts depth at 0.
func (e *Executor) runDSL(ctx context.Context, in RunInput, depth int) (*RunResult, error) {
	if depth >= MaxNestedPipelineDepth {
		return nil, ErrMaxDepthExceeded
	}

	dsl := in.dsl
	pipelineID := ""
	pipelineSlug := dsl.Name
	if in.pipeline != nil {
		pipelineID = in.pipeline.ID
		pipelineSlug = in.pipeline.Slug
		// Author identity comes from the persisted row, NOT from the
		// caller's claim. This is the security gate for cross-crew
		// reuse: invoker cannot impersonate author.
		in.AuthorCrewID = in.pipeline.AuthorCrewID
		in.AuthorAgentID = in.pipeline.AuthorAgentID
	}
	if pipelineID == "" {
		pipelineID = "draft-" + generateRunID()
	}

	runID := generateRunID()
	startedAt := time.Now()

	emit := &pipelineEmitContext{
		emitter:         e.emitter,
		workspaceID:     in.WorkspaceID,
		authorCrewID:    in.AuthorCrewID,
		invokingCrewID:  in.InvokingCrewID,
		invokingAgentID: in.InvokingAgentID,
		pipelineID:      pipelineID,
		pipelineSlug:    pipelineSlug,
		runID:           runID,
	}

	// Render-context env carries safe runtime metadata that templates
	// can reference. Only pre-approved keys go in — never raw env vars.
	renderEnv := map[string]string{
		"author_crew_id":    in.AuthorCrewID,
		"invoking_crew_id":  in.InvokingCrewID,
		"invoking_agent_id": in.InvokingAgentID,
		"run_id":            runID,
		"pipeline_slug":     pipelineSlug,
	}

	inputsForCtx := mergeInputs(in.Inputs, dsl)
	result := &RunResult{
		RunID:        runID,
		PipelineID:   pipelineID,
		PipelineSlug: pipelineSlug,
		StepOutputs:  make(map[string]string, len(dsl.Steps)),
	}

	if in.Mode != ModeDryRun && depth == 0 {
		emit.emitRunStarted(ctx, in.Mode, fmt.Sprintf("%v", inputsForCtx), len(dsl.Steps))
	}

	for i := range dsl.Steps {
		step := dsl.Steps[i]

		stepStart := time.Now()
		// Build the rendered prompt for both run + dry-run paths.
		ctxRender := RenderContext{
			Inputs:      inputsForCtx,
			StepOutputs: result.StepOutputs,
			Env:         renderEnv,
		}
		renderedPrompt := Render(step.Prompt, ctxRender)

		tier, fallback, err := e.resolver.Resolve(ctx, in.WorkspaceID, step)
		if err != nil {
			result.Status = "FAILED"
			result.FailedAtStep = step.ID
			result.ErrorMessage = "tier resolver: " + err.Error()
			emit.emitStepFailed(ctx, step, "tier_resolution", err.Error())
			emit.emitRunFailed(ctx, step.ID, err.Error())
			result.DurationMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}

		switch in.Mode {
		case ModeDryRun:
			ds := DryRunStep{
				StepID:      step.ID,
				StepType:    string(step.Type),
				WouldPass:   renderedPrompt,
				TierAdapter: tier.Adapter,
				TierModel:   tier.Model,
			}
			switch step.Type {
			case StepAgentRun:
				ds.WouldCallAgent = step.AgentSlug
				ds.EstimatedCost = estimateStepCost(step, renderedPrompt)
				result.CostUSD += ds.EstimatedCost
			case StepCallPipeline:
				ds.WouldCallSlug = step.PipelineSlug
				// For dry-run we do not recurse into nested pipelines
				// in MVP — that would require resolving them all and
				// rendering N nested step plans. Phase 2 may unfold.
			}
			result.WouldExecute = append(result.WouldExecute, ds)
			result.StepOutputs[step.ID] = "<dry-run>"
			continue

		case ModeRun, ModeTestRun:
			emit.emitStepStarted(ctx, step, i, tier)

			output, stepCost, stepDur, stepErr := e.runStep(ctx, step, renderedPrompt, tier, fallback, in, runID, pipelineID, emit, ctxRender, depth)
			if stepErr != nil {
				result.Status = "FAILED"
				result.FailedAtStep = step.ID
				result.ErrorMessage = stepErr.Error()
				emit.emitRunFailed(ctx, step.ID, stepErr.Error())
				if in.Mode == ModeRun && in.pipeline != nil {
					_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "FAILED")
				}
				result.DurationMs = time.Since(startedAt).Milliseconds()
				return result, nil
			}
			result.StepOutputs[step.ID] = output
			result.CostUSD += stepCost
			emit.emitStepCompleted(ctx, step, output, stepDur, stepCost)
		}
		_ = stepStart
	}

	result.DurationMs = time.Since(startedAt).Milliseconds()
	if len(dsl.Steps) > 0 {
		lastID := dsl.Steps[len(dsl.Steps)-1].ID
		result.Output = result.StepOutputs[lastID]
	}

	switch in.Mode {
	case ModeDryRun:
		result.Status = "DRY_RUN_OK"
	case ModeRun, ModeTestRun:
		result.Status = "COMPLETED"
		emit.emitRunCompleted(ctx, result.DurationMs, result.CostUSD)
		if in.Mode == ModeRun && in.pipeline != nil {
			_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "COMPLETED")
		}
	}

	return result, nil
}

// runStep dispatches one non-dry-run step to either the AgentRunner
// (agent_run) or back through runDSL (call_pipeline). It also handles
// the validation gate + escalation chain on validation failure.
//
// Returns (output, costUSD, durationMs, error). Error is non-nil only
// when the step ultimately failed after exhausting the fallback chain
// (or when the step type is unsupported).
func (e *Executor) runStep(
	ctx context.Context,
	step Step,
	renderedPrompt string,
	primary AdapterModel,
	fallback []AdapterModel,
	in RunInput,
	runID, pipelineID string,
	emit *pipelineEmitContext,
	parentRender RenderContext,
	depth int,
) (output string, costUSD float64, durationMs int64, err error) {

	switch step.Type {
	case StepAgentRun:
		return e.runAgentStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit)
	case StepCallPipeline:
		return e.runCallPipelineStep(ctx, step, in, parentRender, depth)
	default:
		return "", 0, 0, fmt.Errorf("unsupported step type %q", step.Type)
	}
}

// runAgentStep invokes the AgentRunner for an agent_run step,
// applies the validation gate, and escalates through the fallback
// tier chain on validation failure if on_fail = escalate_tier.
//
// Each attempt logs to the journal so observers can see the
// escalation chain unfold ("trivial failed → fast attempted →
// moderate succeeded"). The final returned output comes from the
// attempt that satisfied the validation gate.
func (e *Executor) runAgentStep(
	ctx context.Context,
	step Step,
	prompt string,
	primary AdapterModel,
	fallback []AdapterModel,
	in RunInput,
	runID, pipelineID string,
	emit *pipelineEmitContext,
) (string, float64, int64, error) {

	attempts := append([]AdapterModel{primary}, fallback...)
	onFail := step.OnFail
	if onFail == "" {
		onFail = OnFailEscalateTier
	}

	totalCost := 0.0
	startTotal := time.Now()
	var lastValidationReason string

	for i, am := range attempts {
		stepStart := time.Now()
		req := AgentStepRequest{
			WorkspaceID:     in.WorkspaceID,
			AuthorCrewID:    in.AuthorCrewID,
			AgentSlug:       step.AgentSlug,
			Adapter:         am.Adapter,
			Model:           am.Model,
			Prompt:          prompt,
			TimeoutSec:      step.TimeoutSec,
			PipelineID:      pipelineID,
			PipelineRunID:   runID,
			StepID:          step.ID,
			InvokingCrewID:  in.InvokingCrewID,
			InvokingAgentID: in.InvokingAgentID,
		}
		res, err := e.runner.RunStep(ctx, req)
		if err != nil {
			emit.emitStepFailed(ctx, step, "agent_run_error", err.Error())
			// Treat outright runner failure (network / timeout / 5xx)
			// the same as a non-retryable validation failure: we
			// escalate to the next tier if escalate_tier is set, else
			// abort. This is conservative — Phase 2 will distinguish
			// retry-able errors from permanent ones.
			if onFail == OnFailEscalateTier && i < len(attempts)-1 {
				continue
			}
			return "", totalCost, time.Since(startTotal).Milliseconds(), err
		}
		totalCost += res.CostUSD

		// Validation gate (cheap structural checks first — bail
		// before we spend rubric-grader tokens on output that
		// already fails byte-level rules).
		ok, reason := validateOutput(res.Output, step.Validation)
		if !ok {
			lastValidationReason = reason
			emit.emitValidationFailed(ctx, step, reason, onFail)
			switch onFail {
			case OnFailAbort:
				return "", totalCost, time.Since(startTotal).Milliseconds(),
					fmt.Errorf("validation failed: %s", reason)
			case OnFailRetryStep:
				return "", totalCost, time.Since(startTotal).Milliseconds(),
					fmt.Errorf("validation failed (retry_step not yet implemented): %s", reason)
			case OnFailEscalateTier:
				continue
			}
		}

		// Outcomes (rubric-based grading) — runs only if structural
		// validation passed. Crewship's answer to Anthropic Managed
		// Agents "outcomes" feature. The grader is a separate agent
		// in the author crew, not a raw LLM call, so the no-API-key
		// invariant survives.
		if step.Outcomes != nil {
			gradeRes, gradeCost, gradeErr := e.runOutcomesGrader(ctx, step, res.Output, in)
			totalCost += gradeCost
			if gradeErr != nil {
				// Grader infrastructure failure: surface but treat
				// as non-fatal-by-default (we don't want a flaky
				// grader to block the worker's output). Emit a
				// validation_failed entry for observability and
				// fall through to returning the worker's output.
				emit.emitValidationFailed(ctx, step, "grader error: "+gradeErr.Error(), OnFailAbort)
				return res.Output, totalCost, time.Since(stepStart).Milliseconds(), nil
			}
			if gradeRes.passed {
				return res.Output, totalCost, time.Since(stepStart).Milliseconds(), nil
			}
			// Grader rejected the output. Attach the grader's
			// feedback as the validation reason so the
			// escalate/retry path has actionable detail.
			reason = "outcomes failed: " + gradeRes.feedback
			lastValidationReason = reason
			emit.emitValidationFailed(ctx, step, reason, outcomesOnFail(step))
			switch outcomesOnFail(step) {
			case OnFailAbort:
				return "", totalCost, time.Since(startTotal).Milliseconds(),
					fmt.Errorf("outcomes failed: %s", reason)
			case OnFailRetryStep:
				// Append grader feedback to the prompt so the
				// next worker attempt has the failure reason in
				// context. We don't yet implement a per-step
				// retry budget (separate from tier escalation);
				// for now retry_step degrades to abort with
				// feedback embedded in the error.
				return "", totalCost, time.Since(startTotal).Milliseconds(),
					fmt.Errorf("outcomes failed and retry_step requires per-step budget (not yet implemented): %s", reason)
			case OnFailEscalateTier:
				// fall through to escalation
			}
		} else if ok {
			// No outcomes configured + validation passed = done.
			return res.Output, totalCost, time.Since(stepStart).Milliseconds(), nil
		}
		// Either validation failed with escalate_tier, or outcomes
		// failed with escalate_tier — both fall through to next
		// fallback tier in the for-loop.
	}

	// Exhausted all tiers; surface the last failure reason
	// (validation OR outcomes — they share lastValidationReason).
	return "", totalCost, time.Since(startTotal).Milliseconds(),
		fmt.Errorf("step failed after exhausting tiers: %s", lastValidationReason)
}

// outcomesOnFail returns the OnFail action for outcomes failures,
// defaulting to abort. We don't reuse the step's OnFail because
// validation failures and outcomes failures may want different
// escalation strategies — a banned-token validation might warrant
// escalate_tier, but a rubric miss might warrant retry_step with
// grader feedback (when retry budgets land in Phase 2).
func outcomesOnFail(step Step) OnFailAction {
	if step.Outcomes != nil && step.Outcomes.OnFail != "" {
		return step.Outcomes.OnFail
	}
	return OnFailAbort
}

// runCallPipelineStep handles a call_pipeline step by looking up the
// nested pipeline, parsing its DSL, and invoking runDSL recursively
// with depth+1. Cycle detection at save time prevents loops; the
// depth ceiling here is the safety net.
//
// parentRender + depth are threaded from the calling runDSL frame so
// (a) nested input templates resolve against the parent's actual
// inputs and step outputs (not against literal placeholders), and
// (b) recursion depth accumulates across levels — without that the
// safety ceiling never fires for legitimately deep call chains.
func (e *Executor) runCallPipelineStep(ctx context.Context, step Step, parent RunInput, parentRender RenderContext, depth int) (string, float64, int64, error) {
	stepStart := time.Now()
	target, err := e.pipes.GetBySlug(ctx, parent.WorkspaceID, step.PipelineSlug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", 0, 0, fmt.Errorf("call_pipeline: %w (slug=%q)", ErrPipelineNotFound, step.PipelineSlug)
		}
		return "", 0, 0, fmt.Errorf("call_pipeline: lookup: %w", err)
	}
	dsl, err := Parse([]byte(target.DefinitionJSON))
	if err != nil {
		return "", 0, 0, fmt.Errorf("call_pipeline: parse target: %w", err)
	}

	// Render nested input values against the parent's render context
	// before handing them to the nested run. String values pass
	// through Render (templates resolved); non-string values land
	// verbatim. Maps/slices are not deep-rendered — DSL authors who
	// need that should use a transform step (Phase 2). Today most
	// nested-input use cases are scalar pass-through or single-level
	// templated strings.
	nestedInputs := make(map[string]any, len(step.NestedInputs))
	for k, v := range step.NestedInputs {
		if s, ok := v.(string); ok {
			nestedInputs[k] = Render(s, parentRender)
		} else {
			nestedInputs[k] = v
		}
	}

	nestedIn := RunInput{
		WorkspaceID:     parent.WorkspaceID,
		AuthorCrewID:    target.AuthorCrewID, // nested runs in nested pipeline's author context
		AuthorAgentID:   target.AuthorAgentID,
		InvokingCrewID:  parent.AuthorCrewID, // parent's author IS the invoker for the nested call
		InvokingAgentID: parent.AuthorAgentID,
		Inputs:          nestedInputs,
		Mode:            parent.Mode,
		pipeline:        target,
		dsl:             dsl,
	}
	// depth+1 so the runtime safety ceiling fires for legitimately
	// deep chains (A→B→C→...). Save-time cycle detection catches
	// loops; this ceiling catches accidental long chains.
	nested, err := e.runDSL(ctx, nestedIn, depth+1)
	if err != nil {
		return "", 0, 0, fmt.Errorf("call_pipeline %q: %w", step.PipelineSlug, err)
	}
	if nested.Status != "COMPLETED" {
		return "", nested.CostUSD, time.Since(stepStart).Milliseconds(),
			fmt.Errorf("nested pipeline %q failed at step %q: %s", step.PipelineSlug, nested.FailedAtStep, nested.ErrorMessage)
	}
	return nested.Output, nested.CostUSD, time.Since(stepStart).Milliseconds(), nil
}

// validateOutput applies a step's Validation to the candidate output.
// Returns ok=true on success; otherwise reason describes which check
// failed. MVP supports must_not_contain / must_contain / min_length /
// max_length; full JSON Schema enforcement is deferred to Phase 2 to
// avoid pulling in the schema library before we know the semantics
// we want.
func validateOutput(output string, v *Validation) (ok bool, reason string) {
	if v == nil {
		return true, ""
	}
	if v.MinLength != nil && len(output) < *v.MinLength {
		return false, fmt.Sprintf("output length %d below min %d", len(output), *v.MinLength)
	}
	if v.MaxLength != nil && len(output) > *v.MaxLength {
		return false, fmt.Sprintf("output length %d exceeds max %d", len(output), *v.MaxLength)
	}
	for _, banned := range v.MustNotContain {
		if banned == "" {
			continue
		}
		if containsCaseSensitive(output, banned) {
			return false, "output contains banned token: " + banned
		}
	}
	for _, required := range v.MustContain {
		if required == "" {
			continue
		}
		if !containsCaseSensitive(output, required) {
			return false, "output missing required token: " + required
		}
	}
	// JSON Schema validation: Phase 2. Until then we accept the
	// schema field as documentation only.
	return true, ""
}

// containsCaseSensitive is a thin wrapper over strings.Contains; kept
// as a function so we can swap in a normalisation pass (e.g. NFC) in
// Phase 2 without touching every call site.
func containsCaseSensitive(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// estimateStepCost returns a coarse cost guess for a dry-run step.
// MVP uses a flat per-step number; Phase 2 will read pricing from
// internal/llm and produce model-aware estimates with token counts.
func estimateStepCost(_ Step, prompt string) float64 {
	// Rough heuristic: $1/M input tokens, ~4 chars/token. Output
	// guess at 25% of input. This is order-of-magnitude only — the
	// dry-run report explicitly labels it "estimated" so users
	// don't mistake it for a quote.
	tokensIn := float64(len(prompt)) / 4
	tokensOut := tokensIn * 0.25
	return (tokensIn + tokensOut) / 1_000_000
}

// mergeInputs takes the caller-supplied inputs and merges in the DSL's
// declared defaults so templates can reference any input the DSL
// promised, even when the caller omitted optional fields.
func mergeInputs(supplied map[string]any, dsl *DSL) map[string]any {
	out := make(map[string]any, len(dsl.Inputs))
	for _, spec := range dsl.Inputs {
		if v, ok := supplied[spec.Name]; ok {
			out[spec.Name] = v
			continue
		}
		if spec.Default != nil {
			out[spec.Name] = spec.Default
		}
	}
	// Preserve any extra inputs the caller passed that the DSL
	// didn't declare — useful for ad-hoc test runs.
	for k, v := range supplied {
		if _, already := out[k]; !already {
			out[k] = v
		}
	}
	return out
}

// generateRunID mints a "run_" CUID for journaling. Distinct from
// generatePipelineID so journal queries can pattern-match either
// kind without ambiguity.
func generateRunID() string {
	ts := time.Now().UnixMilli()
	c := runIDCounter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b[0] = byte(c)
	}
	var buf [40]byte
	out := append(buf[:0], 'r', 'u', 'n', '_', 'c')
	out = strconv.AppendInt(out, ts, 36)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	out = append(out,
		hexdigits[(tail>>12)&0xf],
		hexdigits[(tail>>8)&0xf],
		hexdigits[(tail>>4)&0xf],
		hexdigits[tail&0xf],
	)
	out = append(out, hex.EncodeToString(b)...)
	return string(out)
}

var runIDCounter atomic.Uint64
