package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
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

	// egressAllowed gates the host of HTTP steps. Wired from server
	// boot using the workspace's existing allowlist mechanism (the
	// same one sidecar uses for agent_run egress). Nil = allow all,
	// useful for tests; production wiring sets a real allowlist.
	egressAllowed func(host string) bool

	// credentialByType resolves a credential type ("slack", "stripe",
	// etc.) to its decrypted value at run time. Nil = HTTP steps
	// run without credential injection (public endpoints only).
	credentialByType func(ctx context.Context, credType string) (string, error)

	// codeRunner runs StepCode in a sandboxed container. Nil means
	// code steps return a clear "not configured" error rather than
	// trying to exec the script in-process.
	codeRunner CodeRunner

	// waitpoints persists wait step state so long sleeps survive
	// process restarts. Nil = wait steps execute in-memory only
	// (useful for tests; production wiring uses the WaitpointStore
	// once Phase 2 lands).
	waitpoints WaitpointStore

	// ws is the WebSocket hub for live event push to subscribed
	// frontend clients. Nil = no broadcast (tests, headless mode);
	// production wiring passes ws.Hub via WithWSBroadcaster.
	ws WSBroadcaster

	// runs is the in-memory registry that tracks live runs for
	// cancel + concurrency. Nil = no concurrency gate, no
	// cancellation; tests skip this. Production wiring passes a
	// process-singleton RunRegistry.
	runs *RunRegistry

	// idempotency is the DB-backed dedupe store. Nil = no
	// idempotency layer; the IdempotencyKey field on RunInput is
	// silently ignored. Production wiring passes a real store.
	idempotency *IdempotencyStore

	// runStore persists per-run state to pipeline_runs (migration v83).
	// Nil = persistence disabled; runs stay in journal_entries +
	// in-memory only (pre-v83 behaviour). Production wiring passes a
	// real store at boot so list-active-runs + boot-recovery work.
	runStore *RunStore
}

// WithEgressGate wires the HTTP allowlist. Builders can call this
// pattern (NewExecutor + WithEgressGate + WithCredentialResolver +
// WithCodeRunner + WithWaitpointStore) to compose an executor with
// the optional capabilities turned on. The package's tests stay
// functional with the bare NewExecutor.
func (e *Executor) WithEgressGate(allowed func(host string) bool) *Executor {
	e.egressAllowed = allowed
	return e
}

// WithCredentialResolver wires HTTP credential injection. The
// resolver receives the step's CredentialRef.Type and returns the
// decrypted value (typically by querying the workspace credentials
// table + encryption.Decrypt).
func (e *Executor) WithCredentialResolver(fn func(ctx context.Context, credType string) (string, error)) *Executor {
	e.credentialByType = fn
	return e
}

// WithCodeRunner wires StepCode execution. Without it, code steps
// return a clear error instead of silently no-op'ing.
func (e *Executor) WithCodeRunner(r CodeRunner) *Executor {
	e.codeRunner = r
	return e
}

// WithWaitpointStore wires StepWait persistence. Without it, wait
// steps execute in-memory and don't survive a process restart.
func (e *Executor) WithWaitpointStore(s WaitpointStore) *Executor {
	e.waitpoints = s
	return e
}

// WithWSBroadcaster wires the WebSocket hub for live pipeline
// event push. Frontend clients subscribed to the workspace channel
// receive pipeline.run.* and pipeline.step.* events as they
// happen, so PipelineRunNode status updates without polling.
// Without it, journal entries still land for backfill but the UI
// catches up only on refresh.
func (e *Executor) WithWSBroadcaster(b WSBroadcaster) *Executor {
	e.ws = b
	return e
}

// WithRunRegistry wires cancel + concurrency tracking. Without it,
// the run registry features (Cancel API, concurrency_key gate) are
// silently absent — the executor still runs, just without those
// gates. One registry per process; production builds the singleton
// at server boot and passes it here.
func (e *Executor) WithRunRegistry(r *RunRegistry) *Executor {
	e.runs = r
	return e
}

// WithIdempotencyStore wires the dedupe layer. Without it, the
// IdempotencyKey field on RunInput is silently ignored (every
// request executes fresh). Production wires a DB-backed store at
// boot.
func (e *Executor) WithIdempotencyStore(s *IdempotencyStore) *Executor {
	e.idempotency = s
	return e
}

// WithRunStore wires the per-run persistence layer (pipeline_runs
// table, migration v83). Without it, runs stay in journal_entries +
// in-memory RunRegistry only — restart loses active-run audit
// stories and the list-active-runs panel falls back to slow LIKE
// scans of the journal. Production wiring passes a *RunStore here
// at boot.
func (e *Executor) WithRunStore(s *RunStore) *Executor {
	e.runStore = s
	return e
}

// CodeRunner is the contract for executing StepCode. Production
// wires a Docker-backed runner; tests can pass a stub. The
// implementation is in internal/pipeline/runner_code.go.
type CodeRunner interface {
	RunCode(ctx context.Context, req CodeRunRequest) (CodeRunResult, error)
}

// CodeRunRequest is the input to a code-step run. Inputs from the
// pipeline render context land in InputEnv keyed
// CREWSHIP_INPUT_<NAME>; the runner is responsible for translating
// to env vars in the container.
type CodeRunRequest struct {
	WorkspaceID string
	Runtime     string // python | go | bash
	Version     string
	Code        string
	InputEnv    map[string]string
	TimeoutSec  int
	MaxBytes    int // stdout cap
}

// CodeRunResult is the output of a code-step run. Stdout becomes
// the step's downstream output; stderr lands in the journal entry
// for diagnostic purposes only.
type CodeRunResult struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	DurationMs int64
}

// WaitpointStore persists wait step state so a sleep that exceeds
// the process lifetime can resume after a restart. The interface is
// minimal; the real schema lands in Phase 2 with full waitpoints.
type WaitpointStore interface {
	// CreateApproval mints a token that completes the waitpoint
	// when called via /pipelines/waitpoints/{token}/approve.
	CreateApproval(ctx context.Context, req WaitpointApprovalRequest) (string, error)
	// WaitFor blocks until the named waitpoint resolves (returns
	// approved=true|false on completion) or the context is cancelled.
	WaitFor(ctx context.Context, token string) (bool, error)
}

// WaitpointApprovalRequest is the metadata stored alongside the
// waitpoint so the inbox/UI can render a meaningful approval card.
type WaitpointApprovalRequest struct {
	WorkspaceID    string
	PipelineRunID  string
	StepID         string
	Prompt         string
	InvokingCrewID string
	TimeoutSec     int
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
//
// Run is the gateway for the four production-readiness gates:
//  1. Idempotency — duplicate keys short-circuit to DEDUPED.
//  2. Concurrency — registry rejects when key is at capacity.
//  3. Cancellation — registry's child ctx is propagated downward.
//  4. Retry — handled per-step inside runStep, not here.
//
// Order matters: idempotency BEFORE concurrency. A duplicate key
// must NOT consume a concurrency slot — otherwise webhook
// redeliveries thunder-herd a busy queue. Concurrency-rejected runs
// also forget the idempotency reservation so the caller can retry.
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

	// Pre-allocate the runID so idempotency reserves the same id
	// the run will eventually emit to the journal.
	preallocRunID := in.RunIDOverride
	if preallocRunID == "" {
		preallocRunID = generateRunID()
	}
	in.RunIDOverride = preallocRunID

	// Idempotency check — if the caller supplied a key and we have
	// a store, dedupe before doing anything else.
	if in.IdempotencyKey != "" && e.idempotency != nil && in.Mode == ModeRun {
		resolvedID, isNew, idemErr := e.idempotency.LookupOrReserve(
			ctx, in.WorkspaceID, in.IdempotencyKey, preallocRunID, p.ID, DefaultIdempotencyTTL,
		)
		if idemErr != nil {
			return nil, fmt.Errorf("executor: idempotency: %w", idemErr)
		}
		if !isNew {
			// Duplicate — return a recovery handle pointing at the
			// original run. The HTTP handler maps this to 200 with
			// the original run's id so retried webhooks see a stable
			// success response.
			return &RunResult{
				RunID:        resolvedID,
				PipelineID:   p.ID,
				PipelineSlug: p.Slug,
				Status:       "DEDUPED",
				Deduped:      true,
			}, nil
		}
		// Fresh reservation — the actual run uses the id we just
		// reserved (preallocRunID). RunIDOverride already carries it.
	}

	// Concurrency + cancel registration. Skipped in dry-run / test-
	// run modes — those don't have side effects and shouldn't
	// compete for production slots. Reservation goes against the
	// rendered concurrency_key (template-substituted from inputs)
	// so per-tenant gating works without DSL edits.
	if e.runs != nil && in.Mode == ModeRun {
		key := renderConcurrencyKey(dsl.ConcurrencyKey, in.Inputs)
		regCtx, release, regErr := e.runs.Acquire(ctx, AcquireOpts{
			RunID:          preallocRunID,
			WorkspaceID:    in.WorkspaceID,
			PipelineID:     p.ID,
			PipelineSlug:   p.Slug,
			ConcurrencyKey: key,
			MaxConcurrent:  dsl.MaxConcurrent,
		})
		if regErr != nil {
			// Free the idempotency reservation so the caller can
			// retry the same key without waiting 24h. Best-effort —
			// failure here just means the key stays reserved.
			if in.IdempotencyKey != "" && e.idempotency != nil {
				_ = e.idempotency.Forget(ctx, in.WorkspaceID, in.IdempotencyKey)
			}
			return nil, regErr
		}
		defer release()
		ctx = regCtx
	}

	res, err := e.runDSL(ctx, in, 0)
	// Translate context-cancellation into a CANCELLED status when
	// the registry confirms the cancel was user-driven (vs. a parent
	// ctx going away unexpectedly). The runDSL loop returns the
	// partial result with FAILED in either case; we re-label here so
	// the caller can distinguish "user pressed Cancel" from
	// "unexpected shutdown."
	if err == nil && res != nil && e.runs != nil && e.runs.IsCancelRequested(preallocRunID) {
		res.Status = "CANCELLED"
		if res.ErrorMessage == "" {
			res.ErrorMessage = "run cancelled"
		}
	}
	return res, err
}

// evalIfCondition decides whether a step.If render result counts as
// "true". Empty + the obvious falsey strings short-circuit to false;
// everything else is true. Case-insensitive to match how YAML/JSON
// values flow through templates ("False" from a Python service still
// reads as falsey).
//
// Mirrors GitHub Actions' `if:` evaluator on the easy cases (no full
// expression language — that's a deeper rabbit hole and Render
// already covers the substitution side).
func evalIfCondition(rendered string) bool {
	s := rendered
	// Trim ASCII whitespace without importing strings (keeps the
	// hot path allocation-free).
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	if s == "" {
		return false
	}
	// ASCII fold for the falsey-literal check
	low := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "false", "0", "null", "nil", "no", "off":
		return false
	}
	return true
}

// renderConcurrencyKey renders the DSL's concurrency_key template
// against the inputs map. We only support `{{ inputs.X }}` here —
// the full Render pipeline isn't reachable yet (no step outputs at
// reservation time). Empty template → empty key (no gate).
func renderConcurrencyKey(template string, inputs map[string]any) string {
	if template == "" {
		return ""
	}
	rc := RenderContext{Inputs: inputs, StepOutputs: map[string]string{}, Env: map[string]string{}}
	return Render(template, rc)
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
	// IdempotencyKey, when non-empty, makes Run dedupe via the wired
	// IdempotencyStore: a duplicate request with the same
	// (workspace_id, key) within the TTL returns the original run id
	// with Status="DEDUPED" instead of executing again.
	IdempotencyKey string
	// RunIDOverride lets the caller force a specific run id. Used by
	// the idempotency layer to ensure the reserved id is the one the
	// run actually emits to the journal. Leave empty for the default
	// (executor generates a fresh id).
	RunIDOverride string
	// TierOverride, when non-empty, replaces every agent_run step's
	// `complexity` for the duration of this run. Used by the eval
	// suite to run the same routine on multiple tiers without
	// editing + re-saving the DSL: e.g. one run with "fast" → Haiku,
	// another with "smart" → Opus. Step-level ModelOverride still
	// wins (explicit author intent overrides batch-level override).
	// Empty (default) preserves existing per-step tier resolution.
	TierOverride Complexity
	pipeline     *Pipeline
	dsl          *DSL
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

	// Honour the pre-allocated run id so idempotency reservations
	// and registry entries match the journal trail.
	runID := in.RunIDOverride
	if runID == "" {
		runID = generateRunID()
	}
	startedAt := time.Now()

	emit := &pipelineEmitContext{
		emitter:         e.emitter,
		ws:              e.ws,
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
		// Persist the run row alongside the journal event when
		// the RunStore is wired. Top-level only — nested
		// call_pipeline runs reuse the parent's row id rather
		// than minting their own (the call_pipeline trace lives
		// in journal_entries; tying nested runs into pipeline_runs
		// would conflate "this run completed" with "this nested
		// step completed"). Failure is non-fatal: best-effort
		// projection alongside the canonical journal.
		e.persistRunStart(ctx, in, runID, pipelineID, pipelineSlug, inputsForCtx, startedAt)
		// Deferred terminal write — captures result via closure so
		// every return path (linear / DAG / cost-cap / retry-
		// exhaust / cancel) lands the same finalized row. The
		// persist helper short-circuits when result is unset
		// (recursive helper returns early).
		defer e.persistRunTerminal(ctx, runID, in, pipelineID, result, startedAt)
	}

	// DAG dispatch — if any step declares `needs:` AND we're not in
	// dry-run (which still wants the linear "what would execute"
	// preview), switch to the parallel scheduler. The linear loop
	// below stays the no-DAG path, so existing pipelines keep their
	// exact behaviour.
	if in.Mode != ModeDryRun && hasNeeds(dsl) {
		return e.runDAG(ctx, in, depth, dsl, result, pipelineID, pipelineSlug, runID, emit, inputsForCtx, renderEnv, startedAt)
	}

	for i := range dsl.Steps {
		// Cancel pre-emption — if the run was cancelled (or its
		// parent ctx tripped) between steps, exit cleanly here so
		// the partial result records FailedAtStep correctly. The
		// outer Run() promotes this to Status=CANCELLED when the
		// cancel was user-initiated.
		if err := ctx.Err(); err != nil {
			result.Status = "FAILED"
			if i > 0 {
				result.FailedAtStep = dsl.Steps[i-1].ID
			} else if len(dsl.Steps) > 0 {
				result.FailedAtStep = dsl.Steps[0].ID
			}
			result.ErrorMessage = err.Error()
			emit.emitRunFailed(ctx, result.FailedAtStep, err.Error())
			result.DurationMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}

		step := dsl.Steps[i]

		stepStart := time.Now()
		// Build the rendered prompt for both run + dry-run paths.
		ctxRender := RenderContext{
			Inputs:      inputsForCtx,
			StepOutputs: result.StepOutputs,
			Env:         renderEnv,
		}
		renderedPrompt := Render(step.Prompt, ctxRender)

		// Conditional execution. We evaluate the rendered If string
		// for truthiness BEFORE tier resolution / runner dispatch so
		// a skipped step doesn't burn any tokens or DB lookups.
		// Skipped steps still appear in the journal so observers see
		// "this branch wasn't taken."
		if step.If != "" {
			if !evalIfCondition(Render(step.If, ctxRender)) {
				emit.emitStepSkipped(ctx, step, step.If)
				result.StepOutputs[step.ID] = "<skipped>"
				continue
			}
		}

		// Apply per-run tier override if the caller passed one.
		// ModelOverride still wins inside Resolver — author's
		// explicit pin beats a batch-level "run everything on fast".
		// We mutate a local copy, never the DSL's Step in-place,
		// so concurrent runs of the same DSL with different overrides
		// don't race.
		stepForResolve := step
		if in.TierOverride != "" && step.Type == StepAgentRun && step.ModelOverride == "" {
			stepForResolve.Complexity = in.TierOverride
		}
		tier, fallback, err := e.resolver.Resolve(ctx, in.WorkspaceID, stepForResolve)
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

			output, stepCost, stepDur, stepErr := e.runStepWithRetry(ctx, step, renderedPrompt, tier, fallback, in, runID, pipelineID, emit, ctxRender, depth)
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

			// Cost-cap gate. Checked AFTER the step completes (we
			// can't refund work already done) but BEFORE the next
			// step kicks off so a runaway pipeline halts as soon as
			// the budget is breached. Per-RunInput max would also
			// be useful but DSL-level is the more common case
			// (templates pin the budget regardless of caller).
			if dsl.MaxCostUSD > 0 && result.CostUSD > dsl.MaxCostUSD {
				result.Status = "FAILED"
				result.FailedAtStep = step.ID
				result.ErrorMessage = fmt.Sprintf("cost cap exceeded: $%.4f > $%.4f after step %q", result.CostUSD, dsl.MaxCostUSD, step.ID)
				emit.emitRunFailed(ctx, step.ID, result.ErrorMessage)
				if in.Mode == ModeRun && in.pipeline != nil {
					_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "FAILED")
				}
				result.DurationMs = time.Since(startedAt).Milliseconds()
				return result, nil
			}
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

// runStepWithRetry wraps runStep with the per-step retry policy.
// Distinct concern from OnFail (which handles validation failure):
// retry covers EXECUTION error — the step's runner returned an
// error before we could even validate. HTTP 5xx, code timeout,
// network blip, transient agent crash all fit here.
//
// Order of operations on failure:
//  1. The step's underlying runner errors (HTTP 5xx, etc.)
//  2. retry policy decides: retry-and-sleep, or surface
//  3. If retries exhausted (or no policy), return error to caller
//  4. Caller (runDSL) marks run FAILED
//
// We don't retry on context cancellation — ctx.Err() short-circuits
// out so a Cancel takes effect immediately rather than sleeping
// through the backoff.
func (e *Executor) runStepWithRetry(
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
) (string, float64, int64, error) {
	rp := step.Retry
	if rp == nil || rp.MaxAttempts <= 1 {
		return e.runStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth)
	}

	maxAttempts := rp.MaxAttempts
	if maxAttempts > 10 {
		// Cap to keep a runaway retry from monopolising the run
		// budget. Trigger.dev defaults max=10; we follow.
		maxAttempts = 10
	}
	initialDelay := time.Duration(rp.InitialDelayMs) * time.Millisecond
	if initialDelay <= 0 {
		initialDelay = time.Second
	}
	maxDelay := time.Duration(rp.MaxDelayMs) * time.Millisecond
	if maxDelay <= 0 {
		maxDelay = time.Minute
	}

	var (
		lastOut string
		lastDur int64
		lastErr error
		costSum float64
	)
	delay := initialDelay
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", costSum, 0, err
		}
		out, c, dur, err := e.runStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth)
		costSum += c
		if err == nil {
			return out, costSum, dur, nil
		}
		lastOut, lastDur, lastErr = out, dur, err
		if !shouldRetry(err, rp.RetryOn) || attempt == maxAttempts {
			break
		}
		emit.emitStepRetry(ctx, step, attempt, err.Error(), delay)
		// Full jitter: actual sleep is uniform in [0, delay). Without
		// jitter, N agents that hit the same upstream 429/5xx all
		// retry in lockstep and stampede the recovery moment. AWS
		// blogged the canonical analysis; Trigger.dev/Stripe follow
		// the same pattern. We keep the deterministic upper bound
		// for tests by floor'ing very small delays.
		actualDelay := delay
		if delay > 50*time.Millisecond {
			actualDelay = time.Duration(mathrand.Int64N(int64(delay)))
		}
		select {
		case <-ctx.Done():
			return "", costSum, 0, ctx.Err()
		case <-time.After(actualDelay):
		}
		if rp.Backoff == "exponential" {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
	return lastOut, costSum, lastDur, lastErr
}

// shouldRetry tests whether the error matches the policy's RetryOn
// allowlist. Empty list = retry on any error (most permissive).
// Substring match is intentional — error wrapping makes exact-match
// brittle, and the typical patterns ("timeout", "5xx", "rate limit")
// are durable substrings.
func shouldRetry(err error, retryOn []string) bool {
	if err == nil {
		return false
	}
	if len(retryOn) == 0 {
		return true
	}
	msg := err.Error()
	for _, sub := range retryOn {
		if sub == "" {
			continue
		}
		if containsCaseFold(msg, sub) {
			return true
		}
	}
	return false
}

// containsCaseFold is strings.Contains with ASCII case folding.
// Keeps "Timeout" / "timeout" / "TIMEOUT" all matching the same
// retry allowlist entry — error message casing is inconsistent
// across runners and we don't want callers second-guessing it.
func containsCaseFold(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	return indexCaseFold(s, substr) >= 0
}

func indexCaseFold(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a := s[i+j]
			b := substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
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
	case StepHTTP:
		return e.runHTTPStep(ctx, step, parentRender)
	case StepCode:
		return e.runCodeStep(ctx, step, parentRender, in)
	case StepWait:
		return e.runWaitStep(ctx, step, parentRender, in, runID)
	case StepTransform:
		return e.runTransformStep(step, parentRender)
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
// failed.
//
// Order matters. Cheap byte-level checks first (length, must/not_contain)
// so a junk output fails fast without paying the JSON parse + schema
// compile cost. Schema validation runs only when the byte-level checks
// pass AND the schema field is non-empty.
//
// Schema gate semantics: when v.Schema is set, output MUST be parseable
// as JSON and MUST validate against the schema. A non-JSON output with
// a schema present fails the gate with a clear reason — this is the
// correct behaviour because routines that declare a schema do so
// because downstream steps consume the output as structured data.
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
	if len(v.Schema) > 0 {
		if ok, reason := validateAgainstSchema(output, v.Schema); !ok {
			return false, reason
		}
	}
	return true, ""
}

// validateAgainstSchema parses `output` as JSON and validates it
// against the supplied schema bytes (JSON Schema draft 2020-12 by
// default; library auto-detects $schema if specified).
//
// Failure modes return distinct reasons so a CodeRabbit-style review
// can tell at a glance which class of problem the run hit:
//
//   - "schema invalid"         — the schema itself can't compile.
//     Author bug; the routine should have been rejected at save time
//     once the parser-side schema validator lands. Returning false
//     here is correct: a misshapen schema can't accept anything.
//   - "output not valid JSON"  — output has no JSON structure but
//     a schema was declared. Worker model didn't follow the contract.
//   - "schema validation: ..." — output parsed but failed the schema.
//     Reason includes the first violation (limit at ~200 chars to
//     keep journal lines bounded).
//
// The library is goroutine-safe and the compiled schema can be
// cached per-pipeline. We deliberately don't cache here in MVP —
// most pipelines have at most a handful of schema-gated steps and
// schema compile dominated by syntactic pre-checks is sub-millisecond.
// A cache (keyed on schema-bytes hash) is a Phase 2 optimisation
// once we have throughput data justifying the complexity.
func validateAgainstSchema(output string, schemaBytes json.RawMessage) (ok bool, reason string) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("inline://schema.json", strings.NewReader(string(schemaBytes))); err != nil {
		return false, "schema invalid: " + truncate(err.Error(), 200)
	}
	schema, err := compiler.Compile("inline://schema.json")
	if err != nil {
		return false, "schema invalid: " + truncate(err.Error(), 200)
	}
	var doc any
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		return false, "output not valid JSON: " + truncate(err.Error(), 200)
	}
	if err := schema.Validate(doc); err != nil {
		return false, "schema validation: " + truncate(err.Error(), 200)
	}
	return true, ""
}

// truncate returns s clipped to maxLen runes with an ellipsis if it
// got cut. Used by validateAgainstSchema to keep journal-line widths
// bounded; long jsonschema error chains can run several KB and would
// blow up the journal_entries.error_message column otherwise.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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

// persistRunStart inserts a fresh pipeline_runs row at run boundary
// when the RunStore is wired. Best-effort: failures log a warning
// and the run continues — journal_entries is canonical for audit,
// pipeline_runs is the query-optimized projection. Used only at
// depth==0 (top-level run) and skipped for ModeDryRun (no run row
// for previews).
//
// Skips entirely when pipelineID is empty — that's the unsaved-draft
// path used by RunDefinition (TestRun on a draft DSL). Inserting
// with empty pipelineID would violate the FK on pipeline_runs and
// fail the test_run gate; saved-pipeline runs always have a real
// pipelineID via the in.pipeline.ID field.
func (e *Executor) persistRunStart(ctx context.Context, in RunInput, runID, pipelineID, pipelineSlug string, inputs map[string]any, startedAt time.Time) {
	if e.runStore == nil {
		return
	}
	if pipelineID == "" {
		// Unsaved-draft run (TestRun on RunDefinition path) — no
		// matching pipelines row exists yet, so the FK insert would
		// fail. Skip the projection write; the journal entries that
		// already fired are sufficient for audit on draft runs.
		return
	}
	inputsRaw, _ := json.Marshal(inputs)
	if string(inputsRaw) == "null" {
		inputsRaw = []byte("{}")
	}
	rec := &RunRecord{
		ID:              runID,
		WorkspaceID:     in.WorkspaceID,
		PipelineID:      pipelineID,
		PipelineSlug:    pipelineSlug,
		Status:          RunStatusRunning,
		Mode:            in.Mode,
		StartedAt:       startedAt,
		InvokingCrewID:  in.InvokingCrewID,
		InvokingAgentID: in.InvokingAgentID,
		IdempotencyKey:  in.IdempotencyKey,
		InputsJSON:      string(inputsRaw),
	}
	if err := e.runStore.Insert(ctx, rec); err != nil {
		e.persistWarn("run start", runID, err)
	}
}

// persistRunTerminal writes the finalized run state into pipeline_runs.
// Called via defer from runDSL so every exit path (linear / DAG /
// cost-cap / retry-exhaust / cancel) lands a coherent row. The
// closure-captured `result` reflects whatever runDSL ultimately set
// before returning. If result is nil (early-return path before
// result was constructed), we mark the run failed with a generic
// message so the row doesn't sit in 'running' forever.
//
// Skips entirely when pipelineID is empty (unsaved-draft path —
// matches the persistRunStart skip so we never try to update a row
// we never inserted).
//
// Uses a fresh context with a 5s timeout (NOT the run's ctx) because
// the run's ctx may have been cancelled (user clicked Cancel, parent
// deadline tripped). Persisting terminal state is the audit-of-
// record action that MUST land regardless — without the fresh ctx,
// MarkTerminal would fail with "context cancelled" and the row
// would stay in 'running' forever. 5s is generous for a single
// SQLite UPDATE.
func (e *Executor) persistRunTerminal(runCtx context.Context, runID string, in RunInput, pipelineID string, result *RunResult, startedAt time.Time) {
	if e.runStore == nil {
		return
	}
	if pipelineID == "" {
		// No row to update — see persistRunStart skip rationale.
		return
	}
	// Fresh context decoupled from the run's ctx so cancellation
	// doesn't drop the terminal write. We still cap at 5s so a hung
	// DB doesn't leak goroutines forever via the defer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dur := time.Since(startedAt).Milliseconds()
	terminal := MarkTerminalInput{
		RunID:      runID,
		DurationMs: dur,
	}
	if result == nil {
		terminal.Status = RunStatusFailed
		terminal.ErrorMessage = "run aborted before result was constructed"
		// If the run ctx was cancelled, surface the cancel cause for
		// post-mortem clarity — distinguishes user-initiated cancel
		// from a panic-style early return.
		if runCtx.Err() != nil {
			terminal.Status = RunStatusCancelled
			terminal.ErrorMessage = "run cancelled: " + runCtx.Err().Error()
		}
	} else {
		switch result.Status {
		case "COMPLETED":
			terminal.Status = RunStatusCompleted
		case "FAILED":
			terminal.Status = RunStatusFailed
		case "CANCELLED":
			terminal.Status = RunStatusCancelled
		case "DRY_RUN_OK":
			terminal.Status = RunStatusDryRunOK
		case "DEDUPED":
			// dedupe doesn't transition the existing row; leave it.
			return
		default:
			terminal.Status = RunStatusFailed
			terminal.ErrorMessage = "unknown terminal status: " + result.Status
		}
		terminal.Output = result.Output
		if result.ErrorMessage != "" {
			terminal.ErrorMessage = result.ErrorMessage
		}
		terminal.FailedAtStep = result.FailedAtStep
		terminal.CostUSD = result.CostUSD
		// Persist step outputs map so the run-detail UI can render
		// per-step content even after the goroutine terminates.
		if len(result.StepOutputs) > 0 {
			if err := e.runStore.AppendStepOutput(ctx, runID, result.StepOutputs, result.CostUSD, dur); err != nil {
				e.persistWarn("step outputs flush", runID, err)
			}
		}
	}
	if err := e.runStore.MarkTerminal(ctx, terminal); err != nil {
		e.persistWarn("run terminal", runID, err)
	}
	_ = in // reserved for future invoking_user_id passthrough
}

// persistWarn centralises the "best-effort persistence failed" log
// shape. pipeline_runs is a query-optimized projection; journal_entries
// is the canonical audit log and that write succeeded by definition
// (the emit happens before us). We don't escalate to error or fail the
// run, but we DO log at WARN — silently dropping these turns
// /run-records and boot recovery into a "silent wrong" surface in the
// exact failure modes this projection is supposed to cover.
func (e *Executor) persistWarn(stage, runID string, err error) {
	if err == nil {
		return
	}
	slog.Default().Warn("pipeline projection write failed",
		"stage", stage,
		"run_id", runID,
		"error", err.Error(),
	)
}
