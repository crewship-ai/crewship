package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/dispatch"
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
		return fmt.Errorf("webhook run input is unreadable: %w", err)
	}

	// Resolved here, not carried from acceptance. This is where a revoked
	// permission, a deleted crew or a disabled agent takes effect.
	info, err := rt.h.resolver.ResolveAgent(ctx, in.AgentID, a.Item.WorkspaceID)
	if err != nil {
		return fmt.Errorf("resolve agent %s at dispatch: %w", in.AgentID, err)
	}

	return rt.h.runWebhookAgent(ctx, info, in.AgentID, a.RunID, in.Payload, nil, started)
}

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
