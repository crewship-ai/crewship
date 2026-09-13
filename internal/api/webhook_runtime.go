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
// It answers the question a cancel asks before any process exists: "has
// anything been created, and can anything still be?" Before the agent is
// launched the honest answer to Stop is "nothing is there, and nothing will
// be" — but only if the second half is enforced, which is what the gate does.
// A stop recorded here is checked, under the same lock, at every step that
// leads to the agent, so a preparation that completes after the stop cannot
// launch an agent behind a cancellation that was already confirmed. That is
// the difference between "the container was still starting, so we could not
// find a process" (reconciliation, the previous behaviour) and "no process
// existed and none was allowed to" (cancelled, a fact).
//
// Once the agent is launched the location is the identity: Stop and Alive
// go to the provider's own probes at that location, and a stop that does not
// take is reported as "still there", never as a cancellation.
type webhookLaunch struct {
	mu sync.Mutex
	// cancel ends the run's own context. A stop before launch uses it to
	// abort a preparation that honours its context; one that does not is
	// caught by the gate at its next step.
	cancel context.CancelFunc
	// location is set when the agent is launched and nil before. It is the
	// immutable launch identity, independent of the credential HOME registry
	// that RunAgent's cleanup releases.
	location *orchestrator.RunLocation
	// stopped records that a stop was requested. Set before launch it closes
	// the gate; set after launch it is informational.
	stopped bool
}

// webhookLaunchGate is what runWebhookAgent asks before each pre-agent step
// and at the moment of launch. Implemented by *webhookLaunch; tests that call
// runWebhookAgent directly pass nothing.
type webhookLaunchGate interface {
	// Enter is called before a preparation step (crew container start, run
	// record). It fails once a stop was requested, and the caller then returns
	// without performing the step.
	Enter(step string) error
	// Launch is called with the agent's launch identity immediately before
	// RunAgent. It fails once a stop was requested; success records the
	// location, after which Stop and Alive probe the provider.
	Launch(orchestrator.RunLocation) error
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
	launch := &webhookLaunch{cancel: cancel}
	rt.launches.Store(a.RunID, launch)

	// Resolved here, not carried from acceptance. This is where a revoked
	// permission, a deleted crew or a disabled agent takes effect.
	info, err := rt.h.resolver.ResolveAgent(runCtx, in.AgentID, a.Item.WorkspaceID)
	if err != nil {
		// Nothing has been started, so this is safe to repeat — the agent may
		// simply be being edited.
		return fmt.Errorf("%w: resolve agent %s at dispatch: %w", errWebhookBeforeAgent, in.AgentID, err)
	}

	return rt.h.runWebhookAgent(runCtx, info, in.AgentID, a.RunID, in.Payload, nil, started, launch)
}

// errWebhookBeforeAgent marks the failures that provably happened BEFORE the
// agent ran: the crew runtime would not start, or the run record could not be
// written. Nothing left the machine, so a retry repeats nothing.
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
// to repeat, and they are the two listed above.
//
// This replaces a dispatcher default that retried everything unrecognised,
// which read every failure as harmless.
func (rt *WebhookRuntime) Classify(a dispatch.Assignment, err error) dispatch.Outcome {
	switch {
	case err == nil:
		return dispatch.OutcomeSucceeded
	case errors.Is(err, errWebhookBeforeAgent):
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

// Stop signals the runtime for this attempt.
//
// Before the agent is launched there is nothing to signal, and the answer is
// "stopped" only because the gate now guarantees nothing will be launched: the
// stop is recorded under the launch lock, and the run's context is cancelled
// so a preparation that honours it ends early. After launch the request goes to
// the provider's own probe at the recorded location, and a false is not a
// failure: it means "asked, still there", which the dispatcher must turn into
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
	location := launch.location
	if location == nil {
		// Nothing launched, and the gate now refuses to. Ending the run's
		// context lets a preparation that honours it stop early; one that
		// does not is caught at its next step. The context is NOT cancelled
		// after launch: a running CLI is stopped by the provider's probe at
		// its location, and cutting its stream would only make the run
		// return early while the process carried on.
		launch.cancel()
		launch.mu.Unlock()
		return true, nil
	}
	launch.mu.Unlock()
	if runner, ok := rt.h.orch.(interface {
		StopRunAt(context.Context, orchestrator.RunLocation) (bool, error)
	}); ok {
		return runner.StopRunAt(ctx, *location)
	}
	return rt.h.orch.StopRun(ctx, runID)
}

// Alive reports whether a runtime still exists for this attempt.
//
// Before launch the answer is a definite no: no process was created, and the
// launch state is the record of that. After launch it is the provider's own
// answer at the recorded location. An error is deliberately not a "no":
// treating an unreachable container as an absent process is the same mistake
// as reading a missing locator as "nothing started" — it converts "I do not
// know" into "it is safe to start another".
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
	location := launch.location
	launch.mu.Unlock()
	if location == nil {
		return false, nil
	}
	if runner, ok := rt.h.orch.(interface {
		RunIsAliveAt(context.Context, orchestrator.RunLocation) (bool, error)
	}); ok {
		return runner.RunIsAliveAt(ctx, *location)
	}
	return rt.h.orch.RunIsAlive(ctx, runID)
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
