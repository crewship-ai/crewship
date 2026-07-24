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

// ErrRuntimeCycleDetected is returned when a call_pipeline chain revisits a
// pipeline slug already on the live call stack (#1427, 2.3). Save-time
// CycleDetect catches loops built through already-persisted definitions, but
// a B→A / A→B pair authored in the wrong order (the second save can't see the
// first as a draft) can slip past and only manifest at run time — where the
// depth ceiling would otherwise let it churn ~10 levels before ErrMaxDepthExceeded.
// The runtime slug-path guard rejects the revisit immediately instead.
var ErrRuntimeCycleDetected = errors.New("pipeline: call_pipeline cycle detected at runtime")

// ErrPinnedVersionNotFound is returned by Run when the caller pinned
// the run to a specific pipeline version (RunInput.PinnedVersion —
// schedules/webhooks with target_pipeline_version set) and that
// version row no longer exists. Deliberately a hard failure, never a
// silent fall-back to head: the pin exists precisely so an unexpected
// definition can't run at 3 AM, and quietly substituting head would
// recreate that hazard. Trigger dispatch paths match on this sentinel
// to produce a legible operator-facing error.
var ErrPinnedVersionNotFound = errors.New("pipeline: pinned routine version no longer exists")

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

	// egressAllowed gates the host of HTTP steps at the crew/workspace
	// policy layer. Production wiring (NewWiredExecutor with a DB)
	// installs NewCrewNetworkPolicyGate — the same crew network policy
	// (crews.network_mode + crews.allowed_domains) the sidecar proxy
	// enforces for agent_run container egress, so an http step cannot
	// reach a host the crew's agents are already forbidden from.
	// nil = the policy layer is absent (bare NewExecutor in unit
	// tests); the routine-level egress_targets check and the httpsafe
	// SSRF guard in runHTTPStep still apply.
	egressAllowed func(ctx context.Context, scope RunScope, host string) error

	// allowPrivateHTTP is a test-only escape hatch: when true,
	// runHTTPStep skips the httpsafe SSRF guards (URL string check +
	// safe dialer) so unit tests can target httptest.NewServer which
	// binds to 127.0.0.1. Production never flips this — leaving the
	// flag false is the secure default and the prod wiring path has no
	// setter for it. SetAllowPrivateHTTPForTesting is the only mutator.
	allowPrivateHTTP bool

	// credentialByType resolves a step's credential_ref.type to the
	// decrypted value of a matching ACTIVE credential in the running
	// workspace's vault. Production wiring (NewWiredExecutor with a
	// DB) installs NewVaultCredentialResolver. Nil = HTTP steps run
	// without credential injection (public endpoints only).
	credentialByType func(ctx context.Context, scope RunScope, credType string) (string, error)

	// codeRunner runs StepCode in a sandboxed container. Nil means
	// code steps return a clear "not configured" error rather than
	// trying to exec the script in-process.
	codeRunner CodeRunner

	// scriptRunner execs StepScript (a bundled script) in the crew's own
	// container. Nil means script steps return a clear "not configured"
	// error rather than silently succeeding. Production wiring installs
	// the OrchestratorRunner (which resolves the crew container).
	scriptRunner ScriptRunner

	// stepOverrides, when wired, patches each run's DSL at start with
	// per-step prompt/model overrides (v121) so an operator can nudge a
	// step without bumping the routine version. Nil = run as authored.
	stepOverrides *StepOverrideStore

	// signals is the shared in-process registry for run signals (Wave
	// 4.3). A wait:event step registers here and blocks; the signal
	// endpoint delivers a payload. Nil = wait:event fails closed.
	signals *SignalRegistry

	// waitpoints persists wait step state so long sleeps survive
	// process restarts. Nil = wait steps execute in-memory only
	// (useful for tests; production wiring uses the WaitpointStore
	// once Phase 2 lands).
	waitpoints WaitpointStore

	// signalWaits persists wait(event) step arm/delivery state
	// (pipeline_signal_waits, v154) so a signal survives a process
	// restart (#1409) — the durable counterpart to `signals` above,
	// mirroring how `waitpoints` is to wait(approval). Nil = wait:event
	// steps keep the pre-#1409 in-memory-only behaviour (blocking, no
	// park, no restart survival) — useful for tests, wrong for
	// production.
	signalWaits SignalWaitStore

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

	// notifier delivers non-blocking notify-step messages to the inbox.
	// Production wiring (NewWiredExecutor with a DB) installs a writer
	// backed by inbox.Insert. Nil = notify steps are a best-effort no-op
	// with a wiring warning (they never fail the run).
	notifier InboxNotifier

	// memberCheck reports whether a user is a member of a workspace, so a
	// notify step targeting `user:<id>` can degrade to a workspace notice
	// (rather than silently black-holing the message) when the id isn't a
	// member. Production wiring installs NewWorkspaceMemberChecker(db).
	// Nil = the guard is skipped (target trusted as-is).
	memberCheck func(ctx context.Context, workspaceID, userID string) (bool, error)

	// crewAudience resolves a `crew:<slug>` notify target to the crew's
	// human audience (its crew_members user ids) inside ONE workspace, so
	// a notify step can fan a notice out to every member. Production wiring
	// installs NewCrewAudienceResolver(db). Nil = crew: targets degrade to
	// a workspace notice (never a run failure).
	crewAudience func(ctx context.Context, workspaceID, crewSlug string) ([]string, error)

	// noticeCounter reports how many routine-update notices a run has
	// already delivered to a given recipient, so a notify step can enforce
	// a per-recipient soft cap (anti-spam) and drop further notices once a
	// run has flooded one inbox. Production wiring installs
	// NewRunNoticeCounter(db). Nil = the cap is skipped (delivery uncapped).
	noticeCounter func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error)

	// resumeCutoff is the process-boot fence for the boot resume scan
	// (see WithResumeCutoff). Zero = the scan uses its own entry time.
	resumeCutoff time.Time

	// resumeRetryBase / resumeRetryMax tune the backoff for resumed
	// runs that lose the concurrency-slot race (see
	// WithResumeRetryBackoff). Zero = production defaults.
	resumeRetryBase time.Duration
	resumeRetryMax  time.Duration

	// sleepFn / jitterFn make the per-step retry backoff (runStepWithRetry)
	// injectable so tests drive the retry schedule deterministically without
	// real wall-clock delays. Nil = production behaviour (real timer sleep,
	// full jitter via math/rand). Set only from tests.
	sleepFn  func(ctx context.Context, d time.Duration) bool
	jitterFn func(d time.Duration) time.Duration
}

// RunScope identifies the run on whose behalf a step-level policy
// decision is made: the workspace the run executes in and the crew
// that authored the routine. Both come straight off RunInput —
// runHTTPStep snapshots them so the egress gate and the credential
// resolver can scope their lookups without threading the whole
// RunInput into policy closures.
type RunScope struct {
	WorkspaceID  string
	AuthorCrewID string
}

// WithEgressGate wires the crew/workspace-level HTTP host gate. The
// gate returns nil to allow, or a descriptive error to block (the
// error text lands verbatim in the operator-facing EgressBlockedError,
// so make it actionable). Builders can chain this pattern (NewExecutor
// + WithEgressGate + WithCredentialResolver + WithCodeRunner +
// WithWaitpointStore) to compose an executor with the optional
// capabilities turned on; production goes through NewWiredExecutor,
// which installs NewCrewNetworkPolicyGate whenever a DB is supplied.
func (e *Executor) WithEgressGate(gate func(ctx context.Context, scope RunScope, host string) error) *Executor {
	e.egressAllowed = gate
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
// resolver receives the run's scope plus the step's
// CredentialRef.Type and returns the decrypted value (production:
// NewVaultCredentialResolver — workspace credentials table +
// encryption.Decrypt). The returned value must never be logged.
func (e *Executor) WithCredentialResolver(fn func(ctx context.Context, scope RunScope, credType string) (string, error)) *Executor {
	e.credentialByType = fn
	return e
}

// WithSignalRegistry wires the in-process run-signal registry (Wave 4.3).
// Without it, wait:event steps fail closed.
func (e *Executor) WithSignalRegistry(s *SignalRegistry) *Executor {
	e.signals = s
	return e
}

// WithStepOverrides wires the per-step prompt/model override layer.
// Without it, runs execute the versioned DSL verbatim.
func (e *Executor) WithStepOverrides(s *StepOverrideStore) *Executor {
	e.stepOverrides = s
	return e
}

// WithCodeRunner wires StepCode execution. Without it, code steps
// return a clear error instead of silently no-op'ing.
func (e *Executor) WithCodeRunner(r CodeRunner) *Executor {
	e.codeRunner = r
	return e
}

// WithScriptRunner wires StepScript execution (bundled scripts exec'd in the
// crew container). Without it, script steps return a clear "not configured"
// error instead of silently no-op'ing.
func (e *Executor) WithScriptRunner(r ScriptRunner) *Executor {
	e.scriptRunner = r
	return e
}

// WithWaitpointStore wires StepWait persistence. Without it, wait
// steps execute in-memory and don't survive a process restart.
func (e *Executor) WithWaitpointStore(s WaitpointStore) *Executor {
	e.waitpoints = s
	return e
}

// WithSignalWaitStore wires wait(event) persistence (#1409). Without
// it, wait:event steps keep the pre-#1409 in-memory-only behaviour —
// blocking the run's goroutine with no park, and losing any signal
// delivered while nothing is registered to receive it.
func (e *Executor) WithSignalWaitStore(s SignalWaitStore) *Executor {
	e.signalWaits = s
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
	Runtime     string // expr | cel (python | go | bash reserved, unwired)
	Version     string
	Code        string
	InputEnv    map[string]string
	// Inputs carries the render context's inputs with their ORIGINAL
	// names and types (numbers stay numbers). The expr runner ignores
	// it (operates on rendered literals); the CEL runner exposes it as
	// the `inputs` map variable so expressions can do typed arithmetic
	// and field access (inputs.spend_usd > inputs.threshold_usd).
	Inputs     map[string]any
	TimeoutSec int
	MaxBytes   int // stdout cap
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
// routineStatusRunnable reports whether a routine's persisted governance
// status permits a real run. Empty == 'active' (legacy rows + pre-governance
// pipelines have no status set). Anything else — proposed / disabled / an
// unknown future state — is refused (fail-closed) so a new status can never
// silently run before the gate learns about it.
func routineStatusRunnable(status string) bool {
	return status == "" || status == "active"
}

// StatusRunnable is the exported form of routineStatusRunnable, for
// HTTP handlers that dispatch runs asynchronously and want to give the
// sender a synchronous 409 for a 'proposed'/'disabled' routine instead
// of a fire-and-forget 202 that dies in the background. The executor
// still re-checks at run time — the governance airbag chokepoint below
// stays authoritative for every dispatch path.
func StatusRunnable(status string) bool { return routineStatusRunnable(status) }

func (e *Executor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	if in.Mode == "" {
		in.Mode = ModeRun
	}
	p, err := e.store.GetByID(ctx, in.PipelineID)
	if err != nil {
		return nil, fmt.Errorf("executor: load pipeline: %w", err)
	}
	// Governance airbag — top-level chokepoint. Every real-run dispatch path
	// that starts at Run() (HTTP Run/InternalRun/RunBatch, the cron scheduler,
	// webhook dispatch, and the deferred-run dispatcher) funnels through here,
	// so enforcing the status gate at the executor means a 'proposed'
	// (unapproved) or 'disabled' (admin-killed) routine cannot run from ANY
	// top-level trigger — not just the handlers that remember to pre-check.
	// Nested call_pipeline invocations do NOT pass through Run() (they enter
	// runDSL directly), so runDSL carries its own copy of this gate (#1417).
	// dry_run / test_run are exempt: they preview/validate and carry no
	// persisted status to honor.
	if in.Mode == ModeRun && !routineStatusRunnable(p.Status) {
		return nil, fmt.Errorf("%w: status=%s", ErrRoutineNotActive, p.Status)
	}
	// Version pinning: a trigger carrying target_pipeline_version
	// (schedule, webhook, force-fire, resume of a pinned run) executes
	// that immutable version's definition instead of head. The pinned
	// clone keeps every governance/identity field from the live row —
	// only the definition (and its hash, stamped on the run row) is
	// substituted, so the status gate above and the author-identity
	// rules below still read the current row. A missing version is a
	// hard, legible failure — see ErrPinnedVersionNotFound.
	if in.PinnedVersion != nil {
		v, verr := e.store.GetVersion(ctx, p.ID, *in.PinnedVersion)
		if verr != nil {
			if errors.Is(verr, ErrNotFound) {
				return nil, fmt.Errorf("%w: routine %q has no version %d (was it deleted? update or unpin the trigger)",
					ErrPinnedVersionNotFound, p.Slug, *in.PinnedVersion)
			}
			return nil, fmt.Errorf("executor: load pinned version %d of %q: %w", *in.PinnedVersion, p.Slug, verr)
		}
		pinned := *p
		pinned.DefinitionJSON = v.DefinitionJSON
		pinned.DefinitionHash = v.DefinitionHash
		p = &pinned
	}
	dsl, err := Parse([]byte(p.DefinitionJSON))
	if err != nil {
		return nil, fmt.Errorf("executor: parse stored DSL: %w", err)
	}
	// Apply per-step prompt/model overrides (v121) over the versioned
	// DSL. No-op when the store isn't wired or has no rows for this
	// pipeline — the run then executes exactly as authored.
	if e.stepOverrides != nil {
		if ov, oerr := e.stepOverrides.OverridesFor(ctx, in.PipelineID); oerr == nil {
			applyStepOverrides(dsl.Steps, ov)
		} else {
			e.persistWarn("step overrides", in.PipelineID, oerr)
		}
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
		idemTTL := in.IdempotencyKeyTTL
		if idemTTL <= 0 {
			idemTTL = DefaultIdempotencyTTL
		}
		resolvedID, isNew, idemErr := e.idempotency.LookupOrReserve(
			ctx, in.WorkspaceID, in.IdempotencyKey, preallocRunID, p.ID, idemTTL,
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

	hookSlug := ""
	if in.pipeline != nil {
		hookSlug = in.pipeline.Slug
	}
	res, err := e.runHooksAround(ctx, in, preallocRunID, hookSlug, func() (*RunResult, error) {
		return e.runDSL(ctx, in, 0)
	})
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
// pipeline row. Used by the internal save gate (dry-run validation of
// a draft) and by dry-run preview against unsaved drafts.
//
// authorCrewID, authorAgentID, and workspaceID must be supplied
// since there's no pipelines row to read them from. The resulting
// run is journaled with a synthetic pipeline_id ("draft-" + uuid)
// so observers can tell drafts from saved pipelines.
func (e *Executor) RunDefinition(ctx context.Context, dsl *DSL, in RunInput) (*RunResult, error) {
	// Default to the safe preview, NOT live execution. RunDefinition runs an
	// UNSAVED draft, so a caller that forgets to set Mode should get a dry-run
	// (static validation, no agent invocation), not real steps with real side
	// effects. Both production callers (TestRun, InternalTestRun) pass ModeDryRun
	// explicitly; live execution must be opted into with Mode: ModeRun.
	if in.Mode == "" {
		in.Mode = ModeDryRun
	}
	if in.WorkspaceID == "" {
		return nil, errors.New("executor: workspace_id required for RunDefinition")
	}
	// A real (agent-invoking) draft run needs a crew to resolve agents/runtime
	// against. A ModeDryRun is static validation (parse + template render, no
	// agent invocation), so it works without a crew — letting the public
	// /test_run validate a draft before the author has pinned a crew.
	if in.AuthorCrewID == "" && in.Mode != ModeDryRun {
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
	// InvokingUserID is the workspace user who triggered the run, when
	// known (manual/UI/CLI triggers). Empty for unattended triggers
	// (schedule, nested call_pipeline). Consumed by notify steps that
	// target `to: trigger`; empty → the notification falls back to a
	// workspace-wide notice.
	InvokingUserID string
	Inputs         map[string]any
	Mode           RunMode
	// IdempotencyKey, when non-empty, makes Run dedupe via the wired
	// IdempotencyStore: a duplicate request with the same
	// (workspace_id, key) within the TTL returns the original run id
	// with Status="DEDUPED" instead of executing again.
	IdempotencyKey string
	// IdempotencyKeyTTL bounds how long the dedupe key is honored. Zero
	// uses DefaultIdempotencyTTL (24h). Lets a caller scope dedup tightly
	// (e.g. "same key only collides within 5 min").
	IdempotencyKeyTTL time.Duration
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
	// PinnedVersion, when non-nil, makes Run execute that immutable
	// pipeline_versions row's definition instead of head. Set by the
	// cron scheduler / webhook dispatch / schedule force-fire when the
	// trigger carries target_pipeline_version, and by the resume path
	// for runs that were started pinned. The executed version is
	// recorded on the run row (pipeline_runs.pipeline_version). A pin
	// pointing at a missing version fails the run with
	// ErrPinnedVersionNotFound — never a silent head fallback.
	// Per-step operator overrides (v121) still apply on top of the
	// pinned definition, same as they do on head.
	PinnedVersion *int
	// TriggeredVia / TriggeredByID feed the run-record's audit
	// trail so dashboards can answer "which runs came from a
	// schedule vs a webhook vs a manual click." Default empty =
	// "manual" via RunRecord's normaliseInsert step. Schedules,
	// webhooks, and call_pipeline expansions populate these so
	// the projection accurately reflects the trigger.
	TriggeredVia  TriggeredVia
	TriggeredByID string
	// Tags are workspace-scoped labels attached to the run for
	// filtering/grouping (trigger.dev parity). Persisted to run_tags.
	Tags []string
	// MetadataJSON is a JSON object stored on the run and exposed to
	// steps as {{ run.metadata.X }}. Empty defaults to "{}".
	MetadataJSON string
	// IsReplay + ReplayOf mark a run created by replaying a prior run.
	// IsReplay surfaces as {{ run.is_replay }} so steps can skip side
	// effects on replay; ReplayOf records the source run id.
	IsReplay bool
	ReplayOf string
	pipeline *Pipeline
	dsl      *DSL

	// remainingBudget bounds how many additional USD this run (and its
	// call_pipeline subtree) may spend, as dictated by an ANCESTOR run's
	// max_cost_usd minus what the ancestor had already spent when it
	// invoked this one (#1427, 2.4). 0 = no externally-imposed cap. The
	// run's EFFECTIVE cap is min-nonzero(dsl.MaxCostUSD, remainingBudget)
	// — see effectiveCostCap — so a nested routine can no longer overrun
	// its parent's budget by starting its own count from zero. Set only
	// by buildNestedRunInput; top-level callers leave it zero.
	remainingBudget float64
	// callPath is the ordered list of pipeline slugs currently on the
	// call_pipeline stack ABOVE this run (ancestors, excluding self).
	// runCallPipelineStep rejects a target already present here (or equal
	// to the current run's own slug) with ErrRuntimeCycleDetected before
	// dispatching it. Set only by buildNestedRunInput.
	callPath []string

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
	// resumeReason names the resume cause for the journal summary:
	// resumeReasonRestart (boot scan) or resumeReasonApproval
	// (waitpoint approved in-process). Set only by runResumedRun.
	resumeReason string
}

// costCapExceededMessage is the single wording for max_cost_usd
// breaches. The live post-step gates (linear loop + DAG scheduler)
// and the resume-time gate all share it so operators see one
// consistent failure regardless of where the breach was caught.
func costCapExceededMessage(costUSD, capUSD float64, stepID string) string {
	return fmt.Sprintf("cost cap exceeded: $%.4f > $%.4f after step %q", costUSD, capUSD, stepID)
}

// minNonZero returns the smaller of two caps treating 0 as "no cap"
// (unbounded). If both are 0 the result is 0 (unbounded); otherwise the
// smaller of the non-zero values wins.
func minNonZero(a, b float64) float64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

// effectiveCostCap resolves the budget ceiling a run must honour: the
// tighter of its OWN max_cost_usd and any remaining budget handed down by
// an ancestor call_pipeline frame (#1427, 2.4). 0 = unbounded. Every
// cost-cap gate (linear post-step, DAG post-step, resume-time, the retry
// loop's predictive guard) reads this so a nested run can't overrun the
// parent's budget by counting from zero against only its own cap.
func effectiveCostCap(in RunInput) float64 {
	self := 0.0
	if in.dsl != nil {
		self = in.dsl.MaxCostUSD
	}
	return minNonZero(self, in.remainingBudget)
}

// childRemainingBudget computes the budget a call_pipeline child may spend:
// this run's effective cap minus what it has already spent. 0 = unbounded
// (this run has no effective cap to pass down). Never negative — a run that
// has already met its cap hands the child 0-with-a-cap semantics via the
// pre-dispatch gate, which stops the child's first step.
func childRemainingBudget(in RunInput, spentUSD float64) float64 {
	cap := effectiveCostCap(in)
	if cap <= 0 {
		return 0
	}
	rem := cap - spentUSD
	if rem <= 0 {
		// Fully consumed. Return a tiny positive sentinel so the child
		// inherits a cap (any spend trips its gate) rather than 0 which
		// would read as "unbounded". The pre-dispatch gate in the parent
		// already refuses to start the child in this state, so this is a
		// defense-in-depth floor.
		return negligibleBudget
	}
	return rem
}

// negligibleBudget is an effectively-zero-but-nonzero budget floor handed to
// a nested run whose parent has already exhausted its cap, so the child
// inherits a real ceiling (0 would read as "unbounded" via minNonZero).
const negligibleBudget = 1e-9

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
		depth:           depth,
		dryRun:          in.Mode == ModeDryRun,
	}

	// Render-context env carries safe runtime metadata that templates
	// can reference. Only pre-approved keys go in — never raw env vars.
	renderEnv := map[string]string{
		"author_crew_id":    in.AuthorCrewID,
		"invoking_crew_id":  in.InvokingCrewID,
		"invoking_agent_id": in.InvokingAgentID,
		"run_id":            runID,
		"pipeline_slug":     pipelineSlug,
		// Replay signal: a step can `if: "{{ env.is_replay }}"` to skip
		// side effects when this run is a replay of a prior failure.
		"is_replay": boolToEnvStr(in.IsReplay),
		"replay_of": in.ReplayOf,
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
			emit.emitRunResumed(ctx, in.Mode, len(in.restoredOutputs), len(dsl.Steps), in.resumeReason)
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

		// Cancel classification (#1426, 2.1). Registered AFTER the terminal
		// write above so — defers being LIFO — it runs FIRST, re-labelling a
		// user-cancelled run from FAILED to CANCELLED before persistRunTerminal
		// reads result.Status AND before runHooksAround (which wraps this
		// frame) inspects the returned status. Doing it here rather than in
		// Run() means the cancel is honoured everywhere at once: no failed row,
		// no error_fingerprint (MarkTerminal only mints one for FAILED), no
		// failure-notification fan-out (TerminalNotifier skips CANCELLED), and
		// no on_failure hook (runHooksAround gates it on FAILED).
		defer func() {
			if result != nil && result.Status == "FAILED" &&
				e.runs != nil && e.runs.IsCancelRequested(runID) {
				result.Status = "CANCELLED"
				if result.ErrorMessage == "" {
					result.ErrorMessage = "run cancelled"
				}
			}
		}()
	}

	// Governance airbag (runtime, #1417). Run() enforces the status gate
	// for top-level dispatch, but nested call_pipeline invocations reach
	// runDSL directly (runCallPipelineStep → runDSL), bypassing that check.
	// Re-enforce here keyed on the persisted target's status so a
	// disabled/proposed nested routine cannot execute. Drafts have no
	// persisted row (in.pipeline == nil, e.g. RunDefinition) and stay
	// exempt; dry_run / test_run carry no status to honour.
	if in.Mode == ModeRun && in.pipeline != nil && !routineStatusRunnable(in.pipeline.Status) {
		return e.failRun(ctx, in, emit, result, "",
			fmt.Sprintf("%v: status=%s", ErrRoutineNotActive, in.pipeline.Status), false, startedAt), nil
	}

	// Runtime gates — each returns the terminal FAILED result when it
	// trips, before either scheduler can dispatch anything.
	if failed := e.resumeCostCapGate(ctx, in, emit, result, startedAt); failed != nil {
		return failed, nil
	}
	if failed := e.agentlessGate(ctx, in, emit, result, startedAt); failed != nil {
		return failed, nil
	}

	// DAG dispatch — dry-run always takes the linear "what would execute"
	// preview (graph rendering is the UI's concern). Otherwise the
	// scheduler is selected by parallelism mode:
	//   explicit (default): DAG only when a step declares `needs:` — the
	//     linear loop below stays the no-DAG path, so existing routines
	//     keep their exact behaviour.
	//   auto: derive independence from data flow and run the DAG so
	//     independent siblings fan out (bounded). call_pipeline can't run
	//     in a DAG, so a routine containing one falls back to linear.
	//   off: force the linear loop even if `needs:` are declared.
	// Run metadata is constant for the life of the run — parse it once
	// here instead of re-unmarshalling in.MetadataJSON for every step
	// (linear loop) or every step goroutine (DAG).
	runMeta := parseRunMetadata(in.MetadataJSON)

	if in.Mode != ModeDryRun {
		switch parallelismMode(dsl) {
		case ParallelismAuto:
			if !hasCallPipeline(dsl) {
				return e.runDAG(ctx, in, depth, deriveAutoNeeds(dsl), result, pipelineID, pipelineSlug, runID, emit, inputsForCtx, renderEnv, runMeta, startedAt)
			}
		case ParallelismOff:
			// force sequential — fall through to the linear loop.
		default: // explicit
			if hasNeeds(dsl) {
				return e.runDAG(ctx, in, depth, dsl, result, pipelineID, pipelineSlug, runID, emit, inputsForCtx, renderEnv, runMeta, startedAt)
			}
		}
	}

	for i := range dsl.Steps {
		// Cancel pre-emption — if the run was cancelled (or its
		// parent ctx tripped) between steps, exit cleanly here so
		// the partial result records FailedAtStep correctly. The
		// outer Run() promotes this to Status=CANCELLED when the
		// cancel was user-initiated.
		if err := ctx.Err(); err != nil {
			failedAt := ""
			if i > 0 {
				failedAt = dsl.Steps[i-1].ID
			} else if len(dsl.Steps) > 0 {
				failedAt = dsl.Steps[0].ID
			}
			return e.failRun(ctx, in, emit, result, failedAt, err.Error(), false, startedAt), nil
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

		if terminal := e.runLinearStep(ctx, step, i, in, runID, pipelineID, emit, inputsForCtx, renderEnv, runMeta, depth, result, dsl, startedAt); terminal != nil {
			return terminal, nil
		}
	}

	result.DurationMs = time.Since(startedAt).Milliseconds()
	if len(dsl.Steps) > 0 {
		lastID := dsl.Steps[len(dsl.Steps)-1].ID
		result.Output = result.StepOutputs[lastID]
	}

	switch in.Mode {
	case ModeDryRun:
		result.Status = "DRY_RUN_OK"
	case ModeRun:
		e.completeRun(ctx, in, emit, result)
	default:
		// Only ModeDryRun and ModeRun are supported. Any other (or empty)
		// mode must never return an empty terminal Status — fail loudly so a
		// caller sees a real error instead of a silently no-op'd run.
		result.Status = "FAILED"
		result.ErrorMessage = fmt.Sprintf("unknown run mode %q", in.Mode)
	}

	return result, nil
}

// resumeCostCapGate is the resume-time cost-cap gate. The live gate
// runs AFTER each step completes — so a hard kill that lands after a
// step-boundary flush persisted an already-at-or-over-budget CostUSD
// but before that post-step gate ran leaves a row whose restored cost
// would otherwise buy one more step (or DAG wave) past max_cost_usd.
// Re-check the restored total here, before either scheduler can
// dispatch anything, and fail through the same path a live breach
// uses (same status, same wording) so resumed and live breaches
// are indistinguishable to operators. >= rather than the live
// gate's >: at exactly the cap the budget is fully consumed and
// there is nothing left to spend on another step.
//
// Returns the terminal FAILED result when the gate trips, nil otherwise.
func (e *Executor) resumeCostCapGate(ctx context.Context, in RunInput, emit *pipelineEmitContext, result *RunResult, startedAt time.Time) *RunResult {
	dsl := in.dsl
	cap := effectiveCostCap(in)
	if !in.resume || cap <= 0 || result.CostUSD < cap {
		return nil
	}
	// Attribute the breach to the last restored step in source
	// order — the closest analogue to the live gate's "after
	// step X" — falling back to the stamped in-flight step.
	lastRestored := in.resumeCurrentStepID
	for i := range dsl.Steps {
		if _, ok := in.restoredOutputs[dsl.Steps[i].ID]; ok {
			lastRestored = dsl.Steps[i].ID
		}
	}
	return e.failRun(ctx, in, emit, result, lastRestored, costCapExceededMessage(result.CostUSD, cap, lastRestored), true, startedAt)
}

// agentlessGate enforces the agentless guarantee — runtime
// belt-and-braces behind the save-time validator. A definition that
// reaches the executor with agentless=true AND an LLM-capable step
// (row written before the validator existed, or smuggled past it)
// fails here, before either scheduler can dispatch anything. One
// check covers linear, DAG, and dry-run paths alike.
//
// Returns the terminal FAILED result when the gate trips, nil otherwise.
func (e *Executor) agentlessGate(ctx context.Context, in RunInput, emit *pipelineEmitContext, result *RunResult, startedAt time.Time) *RunResult {
	dsl := in.dsl
	if !dsl.Agentless {
		return nil
	}
	for i := range dsl.Steps {
		st := &dsl.Steps[i]
		if st.Type != StepAgentRun && st.Type != StepCallPipeline {
			continue
		}
		return e.failRun(ctx, in, emit, result, st.ID, fmt.Sprintf("agentless routine contains %s step %q — token-zero guarantee violated; fix and re-save the definition", st.Type, st.ID), true, startedAt)
	}
	return nil
}

// runLinearStep is the per-step execution body of the sequential loop
// in runDSL: render → `if:` skip eval → tier resolve → dispatch →
// post-step cost-cap gate. It mirrors executeOneStep in dag.go (the
// DAG scheduler's per-step body) so the two stay diffable side by
// side; they are deliberately NOT merged — the DAG body pays for
// mutex/atomic/fail-fast plumbing the sequential path doesn't need.
//
// A non-nil return is the run's terminal result (suspend, failure, or
// cost-cap breach) and the caller must return it as-is; nil means the
// step finished (ran, skipped, or dry-ran) and the loop continues.
func (e *Executor) runLinearStep(
	ctx context.Context,
	step Step,
	stepIdx int,
	in RunInput,
	runID, pipelineID string,
	emit *pipelineEmitContext,
	inputsForCtx map[string]any,
	renderEnv map[string]string,
	runMeta map[string]any,
	depth int,
	result *RunResult,
	dsl *DSL,
	startedAt time.Time,
) *RunResult {
	// Build the rendered prompt for both run + dry-run paths.
	ctxRender := buildStepRenderContext(inputsForCtx, result.StepOutputs, renderEnv, runMeta, dsl.EgressTargets)
	renderedPrompt := Render(step.Prompt, ctxRender)

	// Conditional execution. We evaluate the rendered If string
	// for truthiness BEFORE tier resolution / runner dispatch so
	// a skipped step doesn't burn any tokens or DB lookups.
	// Skipped steps still appear in the journal so observers see
	// "this branch wasn't taken."
	if step.If != "" {
		if !evalStepCondition(step.If, ctxRender) {
			emit.emitStepSkipped(ctx, step, step.If)
			result.StepOutputs[step.ID] = "<skipped>"
			e.persistStepOutputs(ctx, in, depth, runID, result.StepOutputs, result.CostUSD, startedAt)
			return nil
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
		emit.emitStepFailed(ctx, step, "tier_resolution", err.Error())
		res := e.failRun(ctx, in, emit, result, step.ID, err.Error(), false, startedAt)
		// Historical quirk, preserved: the stored message carries the
		// "tier resolver:" prefix while the journal event does not.
		res.ErrorMessage = "tier resolver: " + err.Error()
		return res
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
		return nil

	case ModeRun:
		// Pre-step budget gate (#1427, 2.4). Stop BEFORE spending when the
		// run has already met its effective cap — its own max_cost_usd
		// tightened by any remaining budget an ancestor call_pipeline frame
		// handed down. Without this, a nested run whose parent's budget was
		// already exhausted would still fire its first step. estimateStepCost
		// adds a coarse predictive nudge so a step whose estimate alone would
		// breach an already-tight remaining budget is caught up front rather
		// than only after the spend. The estimate is order-of-magnitude and
		// tiny for short prompts, so it never falsely trips a run with real
		// headroom; the authoritative stop stays the post-step gate below.
		if cap := effectiveCostCap(in); cap > 0 {
			if result.CostUSD >= cap || result.CostUSD+estimateStepCost(step, renderedPrompt) > cap {
				return e.failRun(ctx, in, emit, result, step.ID, costCapExceededMessage(result.CostUSD, cap, step.ID), true, startedAt)
			}
		}

		emit.emitStepStarted(ctx, step, stepIdx, tier)

		output, stepCost, stepDur, stepErr := e.runStepWithRetry(ctx, step, renderedPrompt, tier, fallback, in, runID, pipelineID, emit, ctxRender, depth, result.CostUSD)
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
			return result
		}
		if stepErr != nil {
			// Fold the failed attempt's spend into the run total
			// BEFORE bailing — runStepWithRetry reports the cost of
			// every tier it burned alongside the error. Dropping it
			// here made failed runs persist cost_usd=0, hiding
			// exactly the expensive retried/escalated failures from
			// spend analytics (the cost-cap branch below always
			// recorded it, so the two failure paths disagreed).
			result.CostUSD += stepCost
			return e.failRun(ctx, in, emit, result, step.ID, stepErr.Error(), true, startedAt)
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
		// the budget is breached. The cap is the run's EFFECTIVE
		// ceiling (own max_cost_usd tightened by an ancestor
		// call_pipeline frame's remaining budget, #1427 2.4) so a
		// nested run can't overrun its parent by counting from zero.
		if cap := effectiveCostCap(in); cap > 0 && result.CostUSD > cap {
			return e.failRun(ctx, in, emit, result, step.ID, costCapExceededMessage(result.CostUSD, cap, step.ID), true, startedAt)
		}
	}
	return nil
}

// failRun stamps the terminal FAILED state on result, emits the
// run-failed journal event, and finalizes DurationMs. recordInvocation
// controls the pipeline invocation-counter write — some failure paths
// historically never recorded one (tier resolution, cancel pre-emption)
// and that per-site behaviour is preserved. Returns result so callers
// can `return e.failRun(...), nil` in one line.
func (e *Executor) failRun(ctx context.Context, in RunInput, emit *pipelineEmitContext, result *RunResult, stepID, errMsg string, recordInvocation bool, startedAt time.Time) *RunResult {
	result.Status = "FAILED"
	result.FailedAtStep = stepID
	result.ErrorMessage = errMsg
	emit.emitRunFailed(ctx, stepID, errMsg)
	if recordInvocation && in.Mode == ModeRun && in.pipeline != nil {
		_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "FAILED")
	}
	result.DurationMs = time.Since(startedAt).Milliseconds()
	return result
}

// completeRun stamps the terminal COMPLETED state, emits the
// run-completed journal event, and records the invocation. The caller
// must finalize result.DurationMs first — the emit carries it.
func (e *Executor) completeRun(ctx context.Context, in RunInput, emit *pipelineEmitContext, result *RunResult) {
	result.Status = "COMPLETED"
	emit.emitRunCompleted(ctx, result.DurationMs, result.CostUSD)
	if in.Mode == ModeRun && in.pipeline != nil {
		_ = e.store.RecordInvocation(ctx, in.pipeline.ID, "COMPLETED")
	}
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
	priorCostUSD float64,
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

	// Per-step before hook (Wave 4.1): runs ahead of the step; its
	// failure fails the step (setup contract, like routine before_all).
	if step.Hooks != nil && step.Hooks.Before != nil {
		if _, herr := e.runStepHook(ctx, step.Hooks.Before, in, runID, parentRender); herr != nil {
			return "", 0, 0, fmt.Errorf("step %q before hook: %w", step.ID, herr)
		}
	}

	out, cost, dur, err := e.dispatchStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth, priorCostUSD)

	// Per-step after hook: best-effort once the step itself completes
	// (logged, never overrides the step's outcome).
	if err == nil && step.Hooks != nil && step.Hooks.After != nil {
		if _, herr := e.runStepHook(ctx, step.Hooks.After, in, runID, parentRender); herr != nil {
			e.persistWarn("step after hook", runID, herr)
		}
	}
	return out, cost, dur, err
}

// dispatchStep is the raw per-type step dispatch (no hooks). Split out so
// runStep can wrap it with per-step lifecycle hooks.
func (e *Executor) dispatchStep(
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
	priorCostUSD float64,
) (string, float64, int64, error) {
	switch step.Type {
	case StepAgentRun:
		return e.runAgentStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit)
	case StepCallPipeline:
		return e.runCallPipelineStep(ctx, step, in, parentRender, depth, runID, priorCostUSD)
	case StepHTTP:
		return e.runHTTPStep(ctx, step, parentRender, in)
	case StepCode:
		return e.runCodeStep(ctx, step, parentRender, in)
	case StepWait:
		return e.runWaitStep(ctx, step, parentRender, in, runID, depth)
	case StepTransform:
		return e.runTransformStep(step, parentRender)
	case StepNotify:
		return e.runNotifyStep(ctx, step, parentRender, in, runID)
	case StepScript:
		return e.runScriptStep(ctx, step, parentRender, in, runID)
	case StepForeach:
		return e.runForeachStep(ctx, step, in, parentRender, runID, pipelineID, emit, depth)
	default:
		return "", 0, 0, fmt.Errorf("unsupported step type %q", step.Type)
	}
}

// runStepHook runs a per-step hook against the step's parent render
// context (so it sees the same inputs/outputs the step does). Restricted
// to code | http | transform, like routine hooks.
func (e *Executor) runStepHook(ctx context.Context, hook *Step, in RunInput, runID string, parentRender RenderContext) (string, error) {
	return e.dispatchHookStep(ctx, hook, in, parentRender, "step hook")
}

// dispatchHookStep is the shared hook-step dispatch used by both per-step
// hooks (runStepHook) and routine lifecycle hooks (runRoutineHook). Only
// code | http | transform are allowed — hooks must not recurse or spend
// tokens. kind ("step hook" / "hook step") preserves each caller's
// historical error wording.
func (e *Executor) dispatchHookStep(ctx context.Context, hook *Step, in RunInput, render RenderContext, kind string) (string, error) {
	switch hook.Type {
	case StepHTTP:
		out, _, _, err := e.runHTTPStep(ctx, *hook, render, in)
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
		return "", fmt.Errorf("%s %q type %q not allowed (use code, http, or transform)", kind, hook.ID, hook.Type)
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
	// retry_step's retry budget is spent by runStepWithRetry (on EXECUTION
	// errors) before we reach here; for the validation / outcomes gate — a
	// non-transient failure — it behaves as the default (escalate_tier).
	if onFail == "" || onFail == OnFailRetryStep {
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
	// (validation OR outcomes — they share lastValidationReason). Wrapped in
	// errStepOutcomeExhausted so the per-step retry loop treats it as terminal
	// by default (#1429, 2.10) — retrying re-runs the whole tier chain. The
	// message is unchanged, so a retry_on predicate matching the text still
	// opts in.
	return "", totalCost, time.Since(startTotal).Milliseconds(),
		fmt.Errorf("%w: %s", errStepOutcomeExhausted, lastValidationReason)
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
func (e *Executor) runCallPipelineStep(ctx context.Context, step Step, parent RunInput, parentRender RenderContext, depth int, parentRunID string, priorCostUSD float64) (string, float64, int64, error) {
	stepStart := time.Now()

	// Runtime cycle guard (#1427, 2.3). Save-time CycleDetect catches loops
	// built through already-persisted definitions, but a B→A / A→B pair saved
	// in the wrong order can slip past it — the second save can't see the
	// first as a draft. Reject a target already on the live call stack
	// (ancestors + this run's own slug) BEFORE dispatching, so a genuine cycle
	// fails immediately instead of churning ~10 depth levels to
	// ErrMaxDepthExceeded. Legitimately deep-but-acyclic chains are unaffected.
	callStack := append(append([]string{}, parent.callPath...), parentRunSlug(parent))
	for _, slug := range callStack {
		if slug != "" && slug == step.PipelineSlug {
			return "", 0, 0, fmt.Errorf("call_pipeline %q: %w (path: %v)",
				step.PipelineSlug, ErrRuntimeCycleDetected, append(callStack, step.PipelineSlug))
		}
	}

	target, err := e.pipes.GetBySlug(ctx, parent.WorkspaceID, step.PipelineSlug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", 0, 0, fmt.Errorf("call_pipeline: %w (slug=%q)", ErrPipelineNotFound, step.PipelineSlug)
		}
		return "", 0, 0, fmt.Errorf("call_pipeline: lookup: %w", err)
	}
	// Governance gate (#1417, belt-and-suspenders). runDSL enforces this
	// too, but reject here as well so the sentinel wraps cleanly for
	// errors.Is at the call site — a disabled/proposed target is never
	// dispatched from a call_pipeline step.
	if parent.Mode == ModeRun && !routineStatusRunnable(target.Status) {
		return "", 0, 0, fmt.Errorf("call_pipeline %q: %w: status=%s",
			step.PipelineSlug, ErrRoutineNotActive, target.Status)
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

	nestedIn := buildNestedRunInput(parent, target, dsl, nestedInputs, parentRunID,
		childRemainingBudget(parent, priorCostUSD), callStack)
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

// parentRunSlug resolves the slug of the currently-running pipeline from a
// RunInput — the persisted row's slug when saved, else the parsed DSL name
// (drafts / RunDefinition). Used to seed the call_pipeline cycle guard.
func parentRunSlug(in RunInput) string {
	if in.pipeline != nil && in.pipeline.Slug != "" {
		return in.pipeline.Slug
	}
	if in.dsl != nil {
		return in.dsl.Name
	}
	return ""
}

// buildNestedRunInput assembles the RunInput for a call_pipeline child.
// Single seam so the properties that MUST cross the parent→child boundary
// stay in one place and testable (#1427, 3.7 / 3.8):
//   - author identity flips to the TARGET's crew/agent (the security gate for
//     cross-crew reuse — the invoker never impersonates the author);
//   - InvokingUserID + TierOverride PROPAGATE from the parent so a `to: trigger`
//     notify inside a child reaches the human who triggered the top run, and an
//     eval-suite tier override applies through the whole call tree (previously
//     both were silently dropped at the boundary);
//   - TriggeredVia=call_pipeline + TriggeredByID=<parent run id> stamp
//     parentage. NOTE: depth>0 runs deliberately persist NO pipeline_runs row
//     (see persistRunStart — nested runs reuse the parent's row id), so these
//     fields do not surface in RunTree today; they are set for correctness and
//     so a future decision to persist child rows makes the tree light up
//     without another executor change;
//   - remainingBudget carries the ancestor's leftover budget so effectiveCostCap
//     bounds the child (2.4);
//   - callPath threads the live call stack for the runtime cycle guard (2.3).
func buildNestedRunInput(parent RunInput, target *Pipeline, dsl *DSL, nestedInputs map[string]any, parentRunID string, remaining float64, callPath []string) RunInput {
	return RunInput{
		WorkspaceID:     parent.WorkspaceID,
		AuthorCrewID:    target.AuthorCrewID, // nested runs in nested pipeline's author context
		AuthorAgentID:   target.AuthorAgentID,
		InvokingCrewID:  parent.AuthorCrewID, // parent's author IS the invoker for the nested call
		InvokingAgentID: parent.AuthorAgentID,
		InvokingUserID:  parent.InvokingUserID, // 3.7 — propagate the human trigger
		TierOverride:    parent.TierOverride,   // 3.7 — propagate the batch/eval tier override
		TriggeredVia:    TriggeredViaCallPipeline,
		TriggeredByID:   parentRunID, // 3.8 — parentage for RunTree (once child rows persist)
		Inputs:          nestedInputs,
		Mode:            parent.Mode,
		remainingBudget: remaining,
		callPath:        callPath,
		pipeline:        target,
		dsl:             dsl,
	}
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
// path used by RunDefinition (dry-run validation of a draft DSL).
// Inserting with empty pipelineID would violate the FK on
// pipeline_runs; saved-pipeline runs always have a real pipelineID
// via the in.pipeline.ID field.
func (e *Executor) persistRunStart(ctx context.Context, in RunInput, runID, pipelineID, pipelineSlug string, inputs map[string]any, startedAt time.Time) {
	if e.runStore == nil {
		return
	}
	if pipelineID == "" {
		// Unsaved-draft run (RunDefinition path) — no
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
		InvokingUserID:  in.InvokingUserID,
		IdempotencyKey:  in.IdempotencyKey,
		InputsJSON:      string(inputsRaw),
		TriggeredVia:    in.TriggeredVia,
		TriggeredByID:   in.TriggeredByID,
		MetadataJSON:    in.MetadataJSON,
		IsReplay:        in.IsReplay,
		ReplayOf:        in.ReplayOf,
	}
	if in.pipeline != nil {
		// Stamp the definition content hash AS OF run start so the
		// boot resume scan can detect any edit since — including
		// in-place edits that keep every step id, which the step-id
		// existence gate alone cannot see. (pipeline_version is NOT
		// used for this: the content hash is the direct signal, and
		// it stays valid against pre-#996 rows where a dedup'd save
		// left head_version stale.) For a pinned run, in.pipeline already carries
		// the pinned version's definition + hash (substituted in Run).
		rec.DefinitionHash = in.pipeline.DefinitionHash
	}
	// Record which pinned version actually executed (nil = head).
	// buildResumePlan reads this back so a parked pinned run resumes
	// against the same immutable definition.
	rec.PipelineVersion = in.PinnedVersion
	if err := e.runStore.Insert(ctx, rec); err != nil {
		e.persistWarn("run start", runID, err)
	}
	if len(in.Tags) > 0 {
		if err := e.runStore.SetTags(ctx, in.WorkspaceID, runID, in.Tags); err != nil {
			e.persistWarn("run tags", runID, err)
		}
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
	_ = in // invoking_user_id is persisted at run-record creation (see persistRunStart)
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

// recordRunWarning persists a non-fatal, run-scoped warning (currently:
// a failed after_all/on_failure lifecycle hook) so it survives past the
// slog.Warn line persistWarn already emits and is visible via the run
// detail API/CLI. Best-effort like every other projection write in this
// file: a store failure here only logs (via persistWarn's own shape) —
// it must never fail, or mask the status of, the run it's attached to.
// No-op when runStore isn't wired, runID is empty (draft/RunDefinition
// runs), or err is nil.
func (e *Executor) recordRunWarning(ctx context.Context, runID, stage string, err error) {
	if err == nil || e.runStore == nil || runID == "" {
		return
	}
	if werr := e.runStore.AppendWarning(ctx, runID, stage, err.Error()); werr != nil {
		e.persistWarn(stage+" (warning persist)", runID, werr)
	}
}
