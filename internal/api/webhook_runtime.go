package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
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
	h *WebhookHandler
}

// NewWebhookRuntime wires the handler's runtime dependencies to the dispatcher.
func NewWebhookRuntime(h *WebhookHandler) *WebhookRuntime { return &WebhookRuntime{h: h} }

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

	// Resolved here, not carried from acceptance. This is where a revoked
	// permission, a deleted crew or a disabled agent takes effect.
	info, err := rt.h.resolver.ResolveAgent(ctx, in.AgentID, a.Item.WorkspaceID)
	if err != nil {
		// Nothing has been started, so this is safe to repeat — the agent may
		// simply be being edited.
		return fmt.Errorf("%w: resolve agent %s at dispatch: %w", errWebhookBeforeAgent, in.AgentID, err)
	}

	return rt.h.runWebhookAgent(ctx, info, in.AgentID, a.RunID, in.Payload, nil, started)
}

// errWebhookBeforeAgent marks the failures that provably happened BEFORE the
// agent ran: the crew runtime would not start, or the run record could not be
// written. Nothing left the machine, so a retry repeats nothing.
var errWebhookBeforeAgent = errors.New("webhook run failed before the agent started")

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
// It reports whether the runtime is now gone, and a false is not a failure: it
// means "asked, still there", which the dispatcher must turn into
// reconciliation rather than into a cancellation nobody performed.
func (rt *WebhookRuntime) Stop(ctx context.Context, locator string) (bool, error) {
	runID := runIDFromLocator(locator)
	if runID == "" {
		return false, fmt.Errorf("cannot read a run id out of locator %q", locator)
	}
	stopped, err := rt.h.orch.StopRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return stopped, nil
}

// Alive reports whether a runtime still exists for this attempt.
//
// An error is deliberately not a "no". Treating an unreachable container as an
// absent process is the same mistake as reading a missing locator as "nothing
// started" — it converts "I do not know" into "it is safe to start another".
func (rt *WebhookRuntime) Alive(ctx context.Context, locator string) (bool, error) {
	runID := runIDFromLocator(locator)
	if runID == "" {
		return false, fmt.Errorf("cannot read a run id out of locator %q", locator)
	}
	return rt.h.orch.RunIsAlive(ctx, runID)
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
