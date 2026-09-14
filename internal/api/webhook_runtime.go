package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/work"
)

// webhookRunInput is what acceptance records so a dispatcher, possibly in a
// later process, can run the work without the request that produced it.
//
// It is the delivery's own content and nothing derived. Everything about the
// AGENT — is it still enabled, what crew is it in, which credentials does it
// carry — is resolved fresh at dispatch, because work waits for capacity and
// the answer can change while it waits. §3's I8 asks for the check at both
// ends; handing the runtime a snapshot taken at acceptance would be one end.
type webhookRunInput struct {
	AgentID string                 `json:"agent_id"`
	CrewID  string                 `json:"crew_id"`
	Payload webhook.WebhookPayload `json:"payload"`
}

// WebhookRuntime adapts the agent-webhook run onto the dispatcher's Runtime.
//
// There is one of these per server, and it is the ONLY way a webhook starts an
// agent now. The handler used to return a closure that did this directly, which
// made it a second owner of work the ledger already owned — review finding R3.
type WebhookRuntime struct {
	h        *WebhookHandler
	launches sync.Map // run id -> *webhookLaunch
}

// NewWebhookRuntime wires the handler's runtime dependencies to the dispatcher.
func NewWebhookRuntime(h *WebhookHandler) *WebhookRuntime { return &WebhookRuntime{h: h} }

// webhookLaunch is one attempt's launch state, from Run's entry until the
// dispatcher has settled the attempt and called Forget.
//
// It answers the question a cancel asks: "does a process exist, and can one
// still come into existence?" — and it answers from a PROTOCOL, not from
// evidence found afterwards. The orchestrator asks the launch's gate
// synchronously, immediately before it creates the agent's exec, and nothing
// external happens between the gate's answer and the creation. So:
//
//   - Before the gate has been passed, a recorded stop is a fact: the gate
//     refuses from then on, and no process can be created. There is nothing
//     to probe, and "absent" needs no probe to be true.
//   - Once the gate has been passed, a process may exist, may be running, may
//     already have finished. Nothing observed at the location proves it did
//     not — an absent probe while the creation is in flight is meaningless,
//     and an absent probe after the run returned is "it is gone", not "it
//     never was". That is unknown, and reconciliation.
//
// The second review proved why the journal cannot stand in for this: the
// exec.command entry is queued and its failure ignored, so its absence said
// nothing about whether a creation had been requested. The gate records the
// request durably on the attempt (runtime phase `requested`) BEFORE the
// creation, and a write that fails refuses the creation.
type webhookLaunch struct {
	mu sync.Mutex
	// cancel ends the run's own context. A stop before the gate uses it to
	// abort a preparation that honours its context; one that does not is
	// refused at the gate.
	cancel context.CancelFunc
	// location is set at launch (RunAgent entered) and nil before. It is the
	// immutable identity the provider's probes use.
	location *orchestrator.RunLocation
	// stopped records that a stop was requested. Before the gate it closes
	// the gate; after it, it is informational.
	stopped bool
	// requested is set when the gate admitted a creation: the attempt's
	// durable phase is `requested` and a process may exist from here on.
	requested bool
	// confirmed is set once a process is KNOWN to exist: the provider's probe
	// answered present, or the run produced a stream event.
	confirmed bool
	// returned is set when Run has returned. RunAgent is synchronous, so no
	// creation is pending after this.
	returned bool

	// The attempt the gate records against.
	workID     string
	runID      string
	generation int64
	store      *work.Store
}

// launchPhase is the answer to "what can a stop or a probe honestly say".
type launchPhase int

const (
	// launchPreparing: no location yet. Nothing exists, and the gate keeps it
	// that way once a stop is recorded.
	launchPreparing launchPhase = iota
	// launchDeclared: the location is recorded and RunAgent is running, but
	// the creation gate has not been passed. No process exists, and a stop
	// recorded now guarantees none will.
	launchDeclared
	// launchRequested: the gate admitted a creation and Run has not returned.
	// A process may exist; an absent probe proves nothing yet.
	launchRequested
	// launchSettled: the process was confirmed, or Run has returned. The
	// provider's probe is the truth, with one reservation: a process that was
	// requested and never confirmed is, when absent, an unknown outcome.
	launchSettled
)

func (l *webhookLaunch) phaseLocked() launchPhase {
	switch {
	case l.location == nil:
		return launchPreparing
	case l.confirmed || l.returned:
		return launchSettled
	case l.requested:
		return launchRequested
	default:
		return launchDeclared
	}
}

// webhookLaunchGate is what runWebhookAgent asks before each pre-agent step,
// at the moment of launch, and — through AgentRunRequest.ExecGate — what the
// orchestrator asks immediately before creating the exec. Implemented by
// *webhookLaunch; tests that call runWebhookAgent directly pass nothing.
type webhookLaunchGate interface {
	// Enter is called before a preparation step (crew container start, run
	// record). It fails once a stop was requested, and the caller then returns
	// without performing the step.
	Enter(step string) error
	// Launch is called with the agent's launch identity immediately before
	// RunAgent. It fails once a stop was requested; success records the
	// location.
	Launch(orchestrator.RunLocation) error
	// RequestCreation is the creation boundary, asked by the orchestrator
	// immediately before the exec is created. It fails once a stop was
	// requested, and it fails if the request cannot be recorded durably — in
	// both cases no process is created. Success means a process may exist
	// from now on.
	RequestCreation(ctx context.Context) error
}

func (l *webhookLaunch) Enter(step string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return fmt.Errorf("%w: stop requested before %s", errWebhookStoppedBeforeAgent, step)
	}
	return nil
}

func (l *webhookLaunch) Launch(location orchestrator.RunLocation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return fmt.Errorf("%w: stop requested before the agent was launched", errWebhookStoppedBeforeAgent)
	}
	l.location = &location
	return nil
}

func (l *webhookLaunch) RequestCreation(ctx context.Context) error {
	// The lock is held across the durable write on purpose: a Stop that
	// arrives while the request is being recorded must see either "not yet
	// requested" (and its recorded stop then refuses this creation) or
	// "requested" (and it probes). Never a creation that slips between.
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return fmt.Errorf("%w: stop requested before the process was created", errWebhookStoppedBeforeAgent)
	}
	if l.store == nil {
		return fmt.Errorf("%w: no ledger to record the creation request against", errWebhookBeforeAgent)
	}
	if err := l.store.MarkRuntimeRequested(ctx, l.workID, l.runID, l.generation); err != nil {
		// Not recorded, so not created. The attempt stays `starting`, and the
		// failure is a before-agent one: nothing happened.
		return fmt.Errorf("%w: the creation request could not be recorded: %w", errWebhookBeforeAgent, err)
	}
	l.requested = true
	return nil
}

// Locator is the runtime identity this attempt WILL have.
//
// It is derived from the run id alone, which is what lets it be written down
// before anything exists and recognised afterwards by a process that did not
// create it. It mirrors orchestrator.TmuxSessionName's shape; the agent slug is
// resolved at start, so the durable half is the run id.
func (rt *WebhookRuntime) Locator(a dispatch.Assignment) string {
	return "agent-run:" + a.RunID
}

// Run executes the attempt and blocks until it finishes.
func (rt *WebhookRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) error {
	var in webhookRunInput
	if err := json.Unmarshal([]byte(a.Item.InputJSON), &in); err != nil {
		// The input is immutable and was written at acceptance. If it cannot be
		// read, retrying will not help and the work is not the dispatcher's to
		// guess at.
		return fmt.Errorf("%w: %w", errWebhookInputUnreadable, err)
	}

	// The launch state exists from here, so a stop that arrives during agent
	// resolution or container start is recorded against THIS attempt and
	// closes its gate.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	launch := &webhookLaunch{
		cancel: cancel,
		workID: a.Item.ID, runID: a.RunID, generation: a.Generation,
		store: work.NewStore(rt.h.db),
	}
	rt.launches.Store(a.RunID, launch)
	// A stream event is proof the process exists; Run returning is proof no
	// creation is still pending. Both are recorded on the launch state BEFORE
	// they reach the dispatcher, so a Stop or an Alive that follows either
	// answers from the settled phase.
	confirmed := func() {
		launch.mu.Lock()
		launch.confirmed = true
		launch.mu.Unlock()
		if started != nil {
			started()
		}
	}
	defer func() {
		launch.mu.Lock()
		launch.returned = true
		launch.mu.Unlock()
	}()

	// Resolved here, not carried from acceptance. This is where a revoked
	// permission, a deleted crew or a disabled agent takes effect.
	info, err := rt.h.resolver.ResolveAgent(runCtx, in.AgentID, a.Item.WorkspaceID)
	if err != nil {
		// Nothing has been started, so this is safe to repeat — the agent may
		// simply be being edited.
		return fmt.Errorf("%w: resolve agent %s at dispatch: %w", errWebhookBeforeAgent, in.AgentID, err)
	}

	return rt.h.runWebhookAgent(runCtx, info, in.AgentID, a.RunID, in.Payload, nil, confirmed, launch)
}

// errWebhookBeforeAgent marks the failures that provably happened BEFORE the
// agent ran: the crew runtime would not start, the run record could not be
// written, the creation gate refused. Nothing left the machine, so a retry
// repeats nothing.
var errWebhookBeforeAgent = errors.New("webhook run failed before the agent started")

// errWebhookStoppedBeforeAgent is the gate's refusal: a stop was requested
// while the attempt was still preparing, so the step that would have led to
// an agent was not taken. It is a before-agent failure — retrying it after a
// shutdown repeats nothing — and, when a user asked for the stop, the
// dispatcher's settle reads the cancel request first and records a confirmed
// cancellation.
var errWebhookStoppedBeforeAgent = fmt.Errorf("%w: stopped before the agent existed", errWebhookBeforeAgent)

// Classify says what a failure from Run means.
//
// The default is deliberately OutcomeUnclear, and that is the whole reason this
// method exists. A webhook agent turn can push a commit, post a comment, call a
// third-party API — and THEN fail. "RunAgent returned an error" is not evidence
// that nothing happened, so retrying on it repeats whatever did. Only the
// failures we can point at and say "this was before the agent existed" are safe
// to repeat: the ones wrapped in errWebhookBeforeAgent, and a creation the
// orchestrator's gate refused, which by that protocol created nothing.
//
// This replaces a dispatcher default that retried everything unrecognised,
// which read every failure as harmless.
func (rt *WebhookRuntime) Classify(a dispatch.Assignment, err error) dispatch.Outcome {
	switch {
	case err == nil:
		return dispatch.OutcomeSucceeded
	case errors.Is(err, errWebhookBeforeAgent), errors.Is(err, orchestrator.ErrExecRefused):
		return dispatch.OutcomeRetryable
	case errors.Is(err, errWebhookInputUnreadable):
		// The input is immutable; a retry reads the same unreadable bytes.
		return dispatch.OutcomeFailed
	default:
		return dispatch.OutcomeUnclear
	}
}

// errWebhookInputUnreadable is a permanent fault in immutable data.
var errWebhookInputUnreadable = errors.New("webhook run input is unreadable")

// Stop signals the runtime for this attempt, and says only what it can prove.
//
// Before the creation gate has been passed there is nothing to signal, and the
// answer is "stopped" because the gate now guarantees nothing will be created:
// the stop is recorded under the launch lock — the same lock the gate takes —
// and the run's context is cancelled so a preparation that honours it ends
// early. This covers both the container start and the stretch of RunAgent
// before the exec is created, and it needs no probe: a process that was never
// requested is absent by construction.
//
// Once the gate has admitted a creation and Run has not returned, a process
// may exist and creation may still be in flight. The kill probe is sent in
// case the process already exists, and the answer is "not confirmed" whatever
// the probe says. After the launch settles — a process confirmed, or Run
// returned — the provider's probe decides, and a false is not a failure: it
// means "asked, still there", which the dispatcher must turn into
// reconciliation rather than into a cancellation nobody performed.
func (rt *WebhookRuntime) Stop(ctx context.Context, locator string) (bool, error) {
	runID := runIDFromLocator(locator)
	if runID == "" {
		return false, fmt.Errorf("cannot read a run id out of locator %q", locator)
	}
	launch, found := rt.loadLaunch(runID)
	if !found {
		// Not an attempt this process is running. A restart lost the launch
		// state, and answering "stopped" for a process we cannot see is the
		// inference the whole protocol exists to refuse.
		return false, fmt.Errorf("this process holds no launch state for run %s; it cannot signal a runtime "+
			"it does not know the location of", runID)
	}
	launch.mu.Lock()
	launch.stopped = true
	phase := launch.phaseLocked()
	var location orchestrator.RunLocation
	if launch.location != nil {
		location = *launch.location
	}
	if phase == launchPreparing || phase == launchDeclared {
		// Nothing requested, and the gate now refuses to. Ending the run's
		// context lets a preparation that honours it stop early; one that
		// does not is refused at the gate. The context is NOT cancelled once
		// a creation was admitted: a running CLI is stopped by the provider's
		// probe at its location, and cutting its stream would only make the
		// run return early while the process carried on.
		launch.cancel()
		launch.mu.Unlock()
		return true, nil
	}
	launch.mu.Unlock()

	stopped, err := rt.stopAt(ctx, runID, location)
	if phase == launchRequested {
		// Best effort against a process that may already exist; the answer
		// stays "not confirmed" while the creation may still be completing,
		// and a present process is recorded so later probes answer from the
		// settled phase.
		if err == nil && !stopped {
			launch.mu.Lock()
			launch.confirmed = true
			launch.mu.Unlock()
		}
		return false, nil
	}
	return stopped, err
}

func (rt *WebhookRuntime) stopAt(ctx context.Context, runID string, location orchestrator.RunLocation) (bool, error) {
	if runner, ok := rt.h.orch.(interface {
		StopRunAt(context.Context, orchestrator.RunLocation) (bool, error)
	}); ok {
		return runner.StopRunAt(ctx, location)
	}
	return rt.h.orch.StopRun(ctx, runID)
}

func (rt *WebhookRuntime) aliveAt(ctx context.Context, runID string, location orchestrator.RunLocation) (bool, error) {
	if runner, ok := rt.h.orch.(interface {
		RunIsAliveAt(context.Context, orchestrator.RunLocation) (bool, error)
	}); ok {
		return runner.RunIsAliveAt(ctx, location)
	}
	return rt.h.orch.RunIsAlive(ctx, runID)
}

// Alive reports whether a runtime still exists for this attempt, and refuses
// to answer "no" while the answer could still change or could be wrong.
//
// Before the creation gate has been passed the answer is a definite no: no
// process was requested, and the launch state is the record of that — no
// probe and no journal row is consulted, because neither could add to it.
// While a creation is admitted and Run has not returned, a present probe
// confirms the process (that is how a silent CLI gets recorded as running)
// but an absent one is an error, not a no. Once the phase settles the
// provider's answer stands, with one reservation: a process that was
// requested and never confirmed is, when absent, an unknown outcome — it may
// have run and finished — and that is an error, and reconciliation, rather
// than a cancellation.
//
// An error is deliberately not a "no": treating an unreachable container as an
// absent process is the same mistake as reading a missing locator as "nothing
// started" — it converts "I do not know" into "it is safe to start another".
func (rt *WebhookRuntime) Alive(ctx context.Context, locator string) (bool, error) {
	runID := runIDFromLocator(locator)
	if runID == "" {
		return false, fmt.Errorf("cannot read a run id out of locator %q", locator)
	}
	launch, found := rt.loadLaunch(runID)
	if !found {
		return false, fmt.Errorf("this process holds no launch state for run %s; whether a runtime exists for it "+
			"cannot be answered from here", runID)
	}
	launch.mu.Lock()
	phase := launch.phaseLocked()
	requested, confirmed := launch.requested, launch.confirmed
	var location orchestrator.RunLocation
	if launch.location != nil {
		location = *launch.location
	}
	launch.mu.Unlock()

	if phase == launchPreparing || phase == launchDeclared {
		return false, nil
	}
	alive, err := rt.aliveAt(ctx, runID, location)
	if err != nil {
		return false, err
	}
	if alive {
		launch.mu.Lock()
		launch.confirmed = true
		launch.mu.Unlock()
		return true, nil
	}
	if phase == launchRequested {
		return false, fmt.Errorf("run %s: no process at %s yet, and its creation is still pending; "+
			"absence cannot be concluded until the launch settles", runID, location.RunID)
	}
	if requested && !confirmed {
		return false, fmt.Errorf("run %s: a process was requested for it and is gone without ever being "+
			"confirmed; what it did before ending is unknown", runID)
	}
	return false, nil
}

func (rt *WebhookRuntime) loadLaunch(runID string) (*webhookLaunch, bool) {
	v, ok := rt.launches.Load(runID)
	if !ok {
		return nil, false
	}
	launch, ok := v.(*webhookLaunch)
	return launch, ok
}

func runIDFromLocator(locator string) string {
	const prefix = "agent-run:"
	if len(locator) <= len(prefix) || locator[:len(prefix)] != prefix {
		return ""
	}
	return locator[len(prefix):]
}

var _ dispatch.Runtime = (*WebhookRuntime)(nil)

// runRecordAbsent reports whether no run record exists for runID.
//
// It is the "look it up by a stable id" half of the unclear-write rule. A
// failed write is not proof of absence — the row may be there and the response
// lost — and the run id is stable precisely so that question can be asked
// rather than assumed. An unreadable answer is not an absence either, which is
// why the error is returned instead of being folded into the bool.
//
// The record itself is the `run.started` journal entry the internal run-create
// route emits, carrying trace_id == run id. There is no agent_runs table any
// more (unified-journal phase J), and asking a table that does not exist is not
// a lookup — it is an error dressed as one, which this method's whole point is
// to avoid.
func (h *WebhookHandler) runRecordAbsent(ctx context.Context, runID string) (bool, error) {
	var n int
	if err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE trace_id = ? AND entry_type = ?`,
		runID, string(journal.EntryRunStarted)).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// Forget releases launch state only after the dispatcher has recorded the
// outcome. It is separate from RunAgent's cleanup of credential-refresh HOMEs.
func (rt *WebhookRuntime) Forget(locator string) { rt.launches.Delete(runIDFromLocator(locator)) }
