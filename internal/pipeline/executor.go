package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/telemetry"
)

// ErrPipelineNotFound is returned by Run when a call_pipeline step
// references a slug that is not registered in the workspace.
var ErrPipelineNotFound = errors.New("pipeline: target pipeline not found")

// ErrMaxDepthExceeded is returned when call_pipeline recursion goes
// deeper than MaxNestedPipelineDepth. Save-time cycle detection
// catches loops, but a long chain (A→B→C→...→Z) is legal there and
// only flagged at runtime.
var ErrMaxDepthExceeded = fmt.Errorf("pipeline: max nested depth %d exceeded", MaxNestedPipelineDepth)

// suspendError is the internal sentinel a wait(approval) step returns when a
// top-level foreground run should PARK (return WAITING) instead of blocking
// the caller in WaitFor for up to the approval timeout. It carries the
// waitpoint token + step id so the executor can surface them on the WAITING
// RunResult. The run row is already MarkWaiting'd and its step outputs are
// persisted, so an approval-triggered resume (Executor.ResumeAfterApproval) or
// a boot-time resume re-enters from the wait step. Detected via errors.As; it
// must never be treated as a step failure.
type suspendError struct {
	token  string
	stepID string
}

func (s *suspendError) Error() string {
	return fmt.Sprintf("run suspended awaiting approval at step %q", s.stepID)
}

// ErrConcurrencyKeyEmpty is returned when a DSL declares a non-empty
// concurrency_key template but the rendered value is empty — typically
// because a referenced input was omitted at trigger time. Treating it
// as "no gate" would silently allow unlimited parallelism for a
// routine the author explicitly asked us to serialise, so we fail
// fast and surface the misconfiguration.
var ErrConcurrencyKeyEmpty = errors.New("pipeline: concurrency_key rendered to empty value (referenced input missing or empty)")

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

	// allowPrivateHTTP is a test-only escape hatch: when true,
	// runHTTPStep skips the httpsafe SSRF guards (URL string check +
	// safe dialer) so unit tests can target httptest.NewServer which
	// binds to 127.0.0.1. Production never flips this — leaving the
	// flag false is the secure default and the prod wiring path has no
	// setter for it. SetAllowPrivateHTTPForTesting is the only mutator.
	allowPrivateHTTP bool

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

	// resumeCutoff is the process-boot fence for the boot resume scan
	// (see WithResumeCutoff). Zero = the scan uses its own entry time.
	resumeCutoff time.Time

	// resumeRetryBase / resumeRetryMax tune the backoff for resumed
	// runs that lose the concurrency-slot race (see
	// WithResumeRetryBackoff). Zero = production defaults.
	resumeRetryBase time.Duration
	resumeRetryMax  time.Duration
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

// SetAllowPrivateHTTPForTesting is a test-only knob that disables the
// httpsafe SSRF guard on runHTTPStep so unit tests can target
// httptest.NewServer (which binds to 127.0.0.1). Production code never
// calls this — leaving the flag at its zero value keeps the secure
// default in place, and the prod wiring chain in cmd/crewshipd has no
// access to this setter by convention.
func (e *Executor) SetAllowPrivateHTTPForTesting(v bool) {
	e.allowPrivateHTTP = v
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

// WithResumeCutoff sets the process-boot timestamp the boot resume
// scan fences on: only pipeline_runs rows started BEFORE the cutoff
// (i.e. by a previous process lifetime) are considered for resume.
// Rows started at-or-after the cutoff were created by this lifetime
// (scheduler tick, HTTP trigger) and are skipped. When unset, the
// scan defaults to its own entry time — correct as long as the scan
// runs before any work source starts, which the boot wiring
// guarantees; the explicit cutoff is the defense-in-depth layer
// against boot-ordering regressions.
func (e *Executor) WithResumeCutoff(t time.Time) *Executor {
	e.resumeCutoff = t
	return e
}

// WithResumeRetryBackoff tunes the retry cadence the resume scan uses
// when a re-entered run loses the concurrency-slot race
// (ErrConcurrencyLimitReached): base is the first delay, max caps the
// exponential growth. Zero values fall back to the production
// defaults (2s base, 60s cap). Tests pass millisecond values to keep
// the retry path fast.
func (e *Executor) WithResumeRetryBackoff(base, max time.Duration) *Executor {
	e.resumeRetryBase = base
	e.resumeRetryMax = max
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

// WaitpointResumer is the optional capability a WaitpointStore can
// implement to support boot-time resume of runs parked on a `wait`
// step: instead of minting a duplicate approval (second inbox card,
// second token), the resumed wait step looks up the waitpoint the
// previous lifetime created for (run, step) and re-registers its
// listener on the original token. Detected via type assertion so
// test stubs that only implement WaitpointStore keep working.
type WaitpointResumer interface {
	// FindApprovalForStep returns the most recent approval-kind
	// waitpoint token for the (pipelineRunID, stepID) pair, or ""
	// when none exists (the kill happened before CreateApproval
	// landed — the resumed step then creates a fresh one).
	FindApprovalForStep(ctx context.Context, pipelineRunID, stepID string) (string, error)
}

// WaitpointStatusReader is the optional capability a WaitpointStore
// can implement to expose the terminal status of a waitpoint. The
// wait step uses it after WaitFor returns approved=false to tell a
// human denial apart from a timeout (or cancellation) — without it,
// every negative outcome surfaces as "denied", which misleads
// operators when a waitpoint simply expired during downtime.
// Detected via type assertion so test stubs that only implement
// WaitpointStore keep working.
type WaitpointStatusReader interface {
	// WaitpointStatus returns the waitpoint's current status string
	// (pending | approved | denied | timed_out | cancelled).
	WaitpointStatus(ctx context.Context, token string) (string, error)
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

	// Boot-time resume re-validation (TOCTOU guard): buildResumePlan
	// vetted the persisted state against the definition AS OF the boot
	// scan, but runResumedRun's concurrency-slot retry loop can wait
	// unboundedly and every retry reloads the pipeline fresh right
	// above. An edit landing in that window would resume old restored
	// outputs against a changed definition — exactly the hazard the
	// scan-time gate exists to close. Re-check against the definition
	// that will actually execute; runResumedRun maps this error to an
	// interrupted row.
	if in.resume {
		if reason := resumeDefinitionDrift(in.resumeDefinitionHash, p.DefinitionHash, dsl, in.restoredOutputs, in.resumeCurrentStepID); reason != "" {
			return nil, fmt.Errorf("%w: %s", ErrResumeDefinitionChanged, reason)
		}
	}

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
	//
	// Inputs MUST be defaults-merged before rendering the key. A
	// routine declaring `default: "global"` on the input referenced
	// by the key would otherwise trip ErrConcurrencyKeyEmpty when the
	// caller omits it — defeating the whole point of having a default.
	// runDSL does its own mergeInputs later for template-render
	// purposes; we duplicate it here because Acquire happens BEFORE
	// runDSL.
	if e.runs != nil && in.Mode == ModeRun {
		mergedInputs := mergeInputs(in.Inputs, dsl)
		key, gated, keyErr := renderConcurrencyKey(ctx, dsl.ConcurrencyKey, mergedInputs)
		if keyErr != nil {
			// Author wanted a gate but the rendered value is empty.
			// Free the idempotency reservation (mirrors the Acquire
			// failure path below) and surface the misconfiguration.
			if in.IdempotencyKey != "" && e.idempotency != nil {
				_ = e.idempotency.Forget(ctx, in.WorkspaceID, in.IdempotencyKey)
			}
			return nil, fmt.Errorf("%w: template %q", keyErr, dsl.ConcurrencyKey)
		}
		_ = gated // reserved for future telemetry — distinguishes "no gate" from "gated"
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
	// TriggeredVia / TriggeredByID feed the run-record's audit
	// trail so dashboards can answer "which runs came from a
	// schedule vs a webhook vs a manual click." Default empty =
	// "manual" via RunRecord's normaliseInsert step. Schedules,
	// webhooks, and call_pipeline expansions populate these so
	// the projection accurately reflects the trigger.
	TriggeredVia  TriggeredVia
	TriggeredByID string
	pipeline      *Pipeline
	dsl           *DSL

	// resume marks this input as a boot-time re-entry of a run from a
	// previous process lifetime (W6 resume-from-step). Set only by
	// ResumeInterruptedRuns — external callers cannot reach it, which
	// keeps the resume semantics (skip restored steps, re-attach
	// waitpoints, no fresh pipeline_runs insert) off the public API.
	resume bool
	// restoredOutputs is the step-outputs map recovered from the
	// run's persisted pipeline_runs row. Steps present here are
	// treated as already completed: their outputs feed template
	// rendering but they are not re-executed.
	restoredOutputs map[string]string
	// restoredCostUSD seeds the run's accumulated cost so the
	// max_cost_usd guardrail keeps counting across the restart
	// instead of resetting to zero.
	restoredCostUSD float64
	// resumeDefinitionHash / resumeCurrentStepID carry the run row's
	// stamped definition hash and in-flight step id so Run can
	// re-validate the freshly loaded definition against them on every
	// resume re-entry (the scan-time gate alone leaves a TOCTOU
	// window — see resumeDefinitionDrift). Set only by runResumedRun.
	resumeDefinitionHash string
	resumeCurrentStepID  string
}

// costCapExceededMessage is the single wording for max_cost_usd
// breaches. The live post-step gates (linear loop + DAG scheduler)
// and the resume-time gate all share it so operators see one
// consistent failure regardless of where the breach was caught.
func costCapExceededMessage(costUSD, capUSD float64, stepID string) string {
	return fmt.Sprintf("cost cap exceeded: $%.4f > $%.4f after step %q", costUSD, capUSD, stepID)
}

// runDSL is the actual step loop. depth bounds call_pipeline recursion
// across nested invocations; the top-level Run starts depth at 0.
func (e *Executor) runDSL(ctx context.Context, in RunInput, depth int) (result *RunResult, err error) {
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

	// Open the outermost OTel span for this routine run. Every step
	// span beneath becomes a child via ctx propagation. Top-level only —
	// nested call_pipeline invocations live as child spans under the
	// step.run that triggered them, not as siblings, so the trace tree
	// mirrors the DSL composition.
	if depth == 0 {
		runSpanCtx, runSpan := telemetry.StartRoutineRunSpan(ctx, pipelineSlug, runID, pipelineID)
		ctx = runSpanCtx
		defer func() {
			// On panic the named `err` stays nil and the span would
			// close as OK — operators reading the trace would never
			// see the crash. Stamp the recovered value as an error
			// before re-panicking so the runtime's normal unwind
			// still happens. RecoverPanic preserves the original
			// crash stack across this and outer recovers via a
			// *telemetry.PanicWithStack wrapper.
			if r := recover(); r != nil {
				telemetry.RecoverPanic(runSpan, r)
			}
			telemetry.RecordError(runSpan, err)
			runSpan.End()
		}()
	}

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
	// Update in.Inputs to the defaults-merged map so any downstream
	// consumer that takes `in` (the outcomes grader, persistence,
	// nested call_pipeline runs) sees the same effective input set
	// templates render against. Without this, a curl POST with `{}`
	// reaches the grader as `in.Inputs = {}` and rubrics that
	// reference "the original input" can't resolve anything.
	in.Inputs = inputsForCtx
	result = &RunResult{
		RunID:        runID,
		PipelineID:   pipelineID,
		PipelineSlug: pipelineSlug,
		StepOutputs:  make(map[string]string, len(dsl.Steps)),
	}

	// Boot-time resume: seed the result with the step outputs the
	// previous lifetime persisted so (a) templates of later steps
	// render against the completed work and (b) the step loops below
	// skip everything that already ran. Cost is restored too so the
	// max_cost_usd guardrail counts across the restart.
	if in.resume {
		for k, v := range in.restoredOutputs {
			result.StepOutputs[k] = v
		}
		result.CostUSD = in.restoredCostUSD
	}

	if in.Mode != ModeDryRun && depth == 0 {
		if in.resume {
			// The pipeline_runs row already exists (status=running
			// from before the restart) — no fresh insert. Journal a
			// resumed marker instead of a second run.started.
			emit.emitRunResumed(ctx, in.Mode, len(in.restoredOutputs), len(dsl.Steps))
		} else {
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
		}
		// Deferred terminal write — captures result via closure so
		// every return path (linear / DAG / cost-cap / retry-
		// exhaust / cancel) lands the same finalized row. The
		// persist helper short-circuits when result is unset
		// (recursive helper returns early).
		defer e.persistRunTerminal(ctx, runID, in, pipelineID, result, startedAt)
	}

	// Resume-time cost-cap gate. The live gate runs AFTER each step
	// completes — so a hard kill that lands after a step-boundary
	// flush persisted an already-at-or-over-budget CostUSD but before
	// that post-step gate ran leaves a row whose restored cost would
	// otherwise buy one more step (or DAG wave) past max_cost_usd.
	// Re-check the restored total here, before either scheduler can
	// dispatch anything, and fail through the same path a live breach
	// uses (same status, same wording) so resumed and live breaches
	// are indistinguishable to operators. >= rather than the live
	// gate's >: at exactly the cap the budget is fully consumed and
	// there is nothing left to spend on another step.
	if in.resume && dsl.MaxCostUSD > 0 && result.CostUSD >= dsl.MaxCostUSD {
		// Attribute the breach to the last restored step in source
		// order — the closest analogue to the live gate's "after
		// step X" — falling back to the stamped in-flight step.
		lastRestored := in.resumeCurrentStepID
		for i := range dsl.Steps {
			if _, ok := in.restoredOutputs[dsl.Steps[i].ID]; ok {
				lastRestored = dsl.Steps[i].ID
			}
		}
		result.Status = "FAILED"
		result.FailedAtStep = lastRestored
		result.ErrorMessage = costCapExceededMessage(result.CostUSD, dsl.MaxCostUSD, lastRestored)
		emit.emitRunFailed(ctx, lastRestored, result.ErrorMessage)
		if in.Mode == ModeRun && in.pipeline != nil {
			_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "FAILED")
		}
		result.DurationMs = time.Since(startedAt).Milliseconds()
		return result, nil
	}

	// Agentless guarantee — runtime belt-and-braces behind the
	// save-time validator. A definition that reaches the executor with
	// agentless=true AND an LLM-capable step (row written before the
	// validator existed, or smuggled past it) fails here, before either
	// scheduler can dispatch anything. One check covers linear, DAG,
	// and dry-run paths alike.
	if dsl.Agentless {
		for i := range dsl.Steps {
			st := &dsl.Steps[i]
			if st.Type != StepAgentRun && st.Type != StepCallPipeline {
				continue
			}
			result.Status = "FAILED"
			result.FailedAtStep = st.ID
			result.ErrorMessage = fmt.Sprintf("agentless routine contains %s step %q — token-zero guarantee violated; fix and re-save the definition", st.Type, st.ID)
			emit.emitRunFailed(ctx, st.ID, result.ErrorMessage)
			if in.Mode == ModeRun && in.pipeline != nil {
				_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "FAILED")
			}
			result.DurationMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}
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

		// Resume skip: a step whose output was restored from the
		// previous lifetime already ran to completion (or was
		// deliberately skipped — "<skipped>" restores the same way).
		// Its output is in result.StepOutputs for downstream
		// templates; re-executing it would double side effects.
		if in.resume {
			if _, done := result.StepOutputs[step.ID]; done {
				continue
			}
		}

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
				e.persistStepOutputs(ctx, in, depth, runID, result.StepOutputs, result.CostUSD, startedAt)
				continue
			}
		}

		// Stamp the in-flight step BEFORE dispatch so a hard kill
		// mid-step leaves current_step_id pointing at the step that
		// was running — boot-time resume re-executes from here.
		e.persistStepEntry(ctx, in, depth, runID, step.ID)

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
			// Suspend: the wait step parked on an approval. NOT a failure —
			// return WAITING promptly so the caller (and slot) is released.
			// MarkWaiting + step-output persistence already happened in
			// runWaitStep; the deferred terminal write skips WAITING.
			var susp *suspendError
			if errors.As(stepErr, &susp) {
				result.Status = "WAITING"
				result.CurrentStep = susp.stepID
				result.WaitpointToken = susp.token
				result.DurationMs = time.Since(startedAt).Milliseconds()
				return result, nil
			}
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
			// Flush the outputs map at the step boundary so a kill
			// between steps loses at most the in-flight step.
			e.persistStepOutputs(ctx, in, depth, runID, result.StepOutputs, result.CostUSD, startedAt)

			// Cost-cap gate. Checked AFTER the step completes (we
			// can't refund work already done) but BEFORE the next
			// step kicks off so a runaway pipeline halts as soon as
			// the budget is breached. Per-RunInput max would also
			// be useful but DSL-level is the more common case
			// (templates pin the budget regardless of caller).
			if dsl.MaxCostUSD > 0 && result.CostUSD > dsl.MaxCostUSD {
				result.Status = "FAILED"
				result.FailedAtStep = step.ID
				result.ErrorMessage = costCapExceededMessage(result.CostUSD, dsl.MaxCostUSD, step.ID)
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

	// Wrap every step type in a routine.step span so the trace tree shows
	// step boundaries even for transform / http / code steps that have no
	// inner LLM span. Attempt is 0 here — agent_run's own tier-escalation
	// chain produces sibling step spans at attempt 1, 2, … through
	// runAgentStep's internal loop where we don't have visibility from
	// this dispatch level.
	ctx, span := telemetry.StartRoutineStepSpan(ctx, step.ID, string(step.Type), 0)
	defer func() {
		// Same panic-safety pattern as runDSL. This is the INNERMOST
		// recover in the runStep → runDSL → RunAgent chain, so
		// RecoverPanic captures debug.Stack() here — the captured
		// stack points at the original crash location, not at any
		// later re-panic site. Outer recovers reuse this wrapper.
		if r := recover(); r != nil {
			telemetry.RecoverPanic(span, r)
		}
		telemetry.RecordError(span, err)
		span.End()
	}()

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
		return e.runWaitStep(ctx, step, parentRender, in, runID, depth)
	case StepTransform:
		return e.runTransformStep(step, parentRender)
	default:
		return "", 0, 0, fmt.Errorf("unsupported step type %q", step.Type)
	}
}

// resolveInputGuardAction reads the routine's per-routine input guard
// policy and returns the action string the AgentStepRequest carries
// down to runner_llm.go (which passes it to lookout.WithAction). Empty
// is the platform default — block on high-severity match — and is
// returned whenever the DSL omits the guardrails block or any of the
// nested fields, so a routine without explicit policy keeps the
// historical behaviour.
//
// Returning a string instead of a typed enum lets pipeline/types keep
// its zero dependency on lookout — adding the typed import would
// create a cycle the moment lookout takes a pipeline.GuardrailsConfig
// in any future enrichment.
func resolveInputGuardAction(dsl *DSL) string {
	if dsl == nil || dsl.Guardrails == nil || dsl.Guardrails.Input == nil {
		return ""
	}
	pi := dsl.Guardrails.Input.PromptInjection
	if pi == nil {
		return ""
	}
	switch pi.Action {
	case "block", "sanitize", "log":
		return pi.Action
	default:
		return ""
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
//
// Two quality wins layered on top of the bare tier-escalation chain:
//
//  1. Same-tier transient retry. Network blips, rate limits, and
//     truncated empty completions used to immediately jump to a
//     more expensive tier — wasteful, and the bigger model is no
//     more resilient to a 429 than the small one. Now each tier
//     gets retryAttemptsPerTier shots with backoff before we move
//     on. Empty output (string-trim length 0) is treated as the
//     same class of transient since a Haiku rate-limit truncation
//     reaches the runner as success+empty rather than as an error.
//
//  2. Feedback-loop on tier escalation. When validation fails on
//     tier N, the next tier was previously handed the SAME prompt —
//     so it had no idea what to do differently. Now we prepend a
//     short feedback block ("PREVIOUS ATTEMPT FAILED VALIDATION:
//     <reason>. Address this exactly.") so the next tier knows
//     what the schema gate or rubric grader rejected last time.
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
	basePrompt := prompt

	for i, am := range attempts {
		stepStart := time.Now()
		// Inject feedback from the previous tier's failure into the
		// retry prompt so the new tier knows what to fix.
		attemptPrompt := basePrompt
		if i > 0 && lastValidationReason != "" {
			attemptPrompt = injectValidationFeedback(basePrompt, lastValidationReason)
		}
		req := AgentStepRequest{
			WorkspaceID:      in.WorkspaceID,
			AuthorCrewID:     in.AuthorCrewID,
			AgentSlug:        step.AgentSlug,
			Adapter:          am.Adapter,
			Model:            am.Model,
			Prompt:           attemptPrompt,
			TimeoutSec:       step.TimeoutSec,
			PipelineID:       pipelineID,
			PipelineRunID:    runID,
			StepID:           step.ID,
			InvokingCrewID:   in.InvokingCrewID,
			InvokingAgentID:  in.InvokingAgentID,
			InputGuardAction: resolveInputGuardAction(in.dsl),
		}
		res, err := e.runRunnerWithTransientRetry(ctx, req, step, emit)
		if err != nil {
			emit.emitStepFailed(ctx, step, "agent_run_error", err.Error())
			// Treat outright runner failure as a candidate for tier
			// escalation: the next-bigger tier might be hosted on a
			// different region / API key combo and dodge the failure.
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

// stepPersistEnabled centralises the gate for per-step run-state
// writes: a wired RunStore, top-level run (nested call_pipeline runs
// share the parent's row), a real mode, and a saved pipeline (drafts
// have no row — see persistRunStart's skip rationale).
func (e *Executor) stepPersistEnabled(in RunInput, depth int) bool {
	return e.runStore != nil && depth == 0 && in.Mode != ModeDryRun && in.pipeline != nil
}

// persistStepEntry stamps status=running + current_step_id at step
// entry so a hard kill mid-step leaves a row pointing at the step
// that was in flight. Best-effort like every projection write.
func (e *Executor) persistStepEntry(ctx context.Context, in RunInput, depth int, runID, stepID string) {
	if !e.stepPersistEnabled(in, depth) {
		return
	}
	if err := e.runStore.MarkRunning(ctx, runID, stepID); err != nil {
		e.persistWarn("step entry", runID, err)
	}
}

// persistStepOutputs flushes the step-outputs map + running cost at a
// step boundary so boot-time resume can restore every completed step.
// Duration is wall time since this lifetime's start — on a resumed
// run it intentionally restarts from the resume point rather than
// pretending continuity across the gap the process was down.
//
// Takes the outputs map + cost explicitly (not *RunResult) so the DAG
// scheduler can pass a mutex-guarded snapshot without holding the
// lock across a DB write.
func (e *Executor) persistStepOutputs(ctx context.Context, in RunInput, depth int, runID string, stepOutputs map[string]string, costUSD float64, startedAt time.Time) {
	if !e.stepPersistEnabled(in, depth) {
		return
	}
	if err := e.runStore.AppendStepOutput(ctx, runID, stepOutputs, costUSD, time.Since(startedAt).Milliseconds()); err != nil {
		e.persistWarn("step outputs", runID, err)
	}
}

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
		TriggeredVia:    in.TriggeredVia,
		TriggeredByID:   in.TriggeredByID,
	}
	if in.pipeline != nil {
		// Stamp the definition content hash AS OF run start so the
		// boot resume scan can detect any edit since — including
		// in-place edits that keep every step id, which the step-id
		// existence gate alone cannot see. (pipeline_version is NOT
		// used for this: the version store dedupes by content hash,
		// so head_version can point at a stale row after an A→B→A
		// edit cycle.)
		rec.DefinitionHash = in.pipeline.DefinitionHash
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
		case "WAITING":
			// NON-terminal: the run parked on an approval. MarkWaiting
			// already set status=waiting + current_step; the deferred
			// terminal write must NOT overwrite it. The approval-triggered
			// (or boot-time) resume lands the eventual terminal row.
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
