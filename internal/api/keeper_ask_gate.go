package api

// Keeper judges the credential ASK, not only its use (#2392).
//
// Before #2392 a CREDENTIAL escalation raised as an ASK (no value) went
// straight to a human's inbox — the Keeper judged /keeper/execute and
// /keeper/request, but never the request to raise the ask in the first place.
// So a prompt-injected or off-task agent could spam credential requests at the
// operator with no judge between them.
//
// CreateEscalation now asks the same judge that guards credential ACCESS
// whether it even makes sense for THIS agent, in THIS task, to want THIS
// credential at THIS tier — before anything is staged:
//
//   - ALLOW    → stage the REQUESTED credential and route to the inbox as before.
//   - DENY     → nothing is staged, no inbox row, no human interrupted; the
//                agent gets the judge's reason.
//   - ESCALATE → stage + route to the inbox WITH the judge's note attached, so
//                the human sees why it was flagged.
//
// The judge NEVER sees or stores a value — there is none at ask time. It reasons
// only over the declared name/type/tier/purpose and the agent's conversation,
// exactly the inputs the access judge already weighs.

import (
	"context"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// errAskJudgeUnavailable is returned by JudgeCredentialAsk when no judge is
// configured (Keeper is off, or the instance has no gatekeeper). The caller
// treats it as "do not gate" — stage the ask and route to a human exactly as it
// did before #2392, so turning Keeper off never blocks a credential request.
// It is distinct from a judge that ran and errored: that is an ESCALATE, not a
// skip (a judge outage must not silently swallow a legitimate request).
var errAskJudgeUnavailable = errors.New("keeper: no credential-ask judge configured")

// CredentialAskInput is everything the ask judge needs. No value — an ask has
// none — and no credential id, because the credential is not staged yet.
type CredentialAskInput struct {
	WorkspaceID    string
	CrewID         string
	AgentID        string
	AgentName      string
	CredentialName string
	Purpose        string
	SecurityLevel  int
}

// CredentialAskJudge decides whether an agent's credential ASK should be raised.
// Implemented by KeeperHandler; wired into QueryHandler at router setup. A nil
// judge, or errAskJudgeUnavailable, means "do not gate".
type CredentialAskJudge interface {
	JudgeCredentialAsk(ctx context.Context, in CredentialAskInput) (keeper.GatekeeperResponse, error)
}

// JudgeCredentialAsk runs the credential-access judge over an ASK.
//
// It reuses the ACCESS evaluation deliberately rather than adding a fifth prompt
// template: the question an ask poses — is this intent legitimate, proportional
// to the tier, and corroborated by the conversation — is the exact question
// buildAccessPrompt already asks, and the tier policy (intent-length floor,
// L1 auto-allow, the L4 human-approval floor that turns ALLOW into ESCALATE)
// is precisely the behaviour an ask should inherit.
//
// No Evidence and HardGate=false: an ask has no agent_credentials binding yet
// (the credential does not exist), so the binding gate must not fire — there is
// nothing bound to weigh, and gating on its absence would deny every ask.
func (h *KeeperHandler) JudgeCredentialAsk(ctx context.Context, in CredentialAskInput) (keeper.GatekeeperResponse, error) {
	if h == nil || h.gatekeeper == nil {
		return keeper.GatekeeperResponse{}, errAskJudgeUnavailable
	}

	crewName := ""
	if in.CrewID != "" {
		_ = h.db.QueryRowContext(ctx,
			`SELECT COALESCE(name, '') FROM crews WHERE id = ?`, in.CrewID).Scan(&crewName)
	}

	level := keeper.SecurityLevel(in.SecurityLevel)
	if !level.Valid() {
		// An unset or out-of-range proposed tier resolves to L1, the same floor
		// createPendingCredential stores it at. The judge still applies the tier
		// policy for L1 (a real purpose is required); it is not a bypass.
		level = keeper.SecurityLevelL1
	}

	req := keeper.Request{
		RequestingAgentID: in.AgentID,
		RequestingCrewID:  in.CrewID,
		CredentialName:    in.CredentialName,
		SecurityLevel:     level,
		Intent:            in.Purpose,
		WorkspaceID:       in.WorkspaceID,
		CreatedAt:         time.Now().UTC(),
		RequestType:       keeper.RequestTypeAccess,
	}

	resp, err := h.gatekeeper.Evaluate(ctx, gatekeeper.EvalRequest{
		Request:            req,
		CredentialName:     in.CredentialName,
		SecurityLevel:      level,
		AgentName:          in.AgentName,
		CrewName:           crewName,
		ConvHistory:        h.loadConversationHistory(ctx, in.AgentID),
		Command:            "", // an ask runs nothing
		Evidence:           nil,
		HardGate:           false, // no binding exists for a credential not yet staged
		PromptBudgetTokens: h.promptBudget(),
		EscalateFrom:       h.escalateFrom(),
		RequestType:        keeper.RequestTypeAccess,
	})
	if err != nil {
		return keeper.GatekeeperResponse{}, err
	}
	return resp, nil
}

// SetCredentialAskJudge wires the Keeper ask judge (#2392). nil leaves ask
// gating off: CreateEscalation stages every ask and routes it to a human, the
// pre-#2392 behaviour.
func (h *QueryHandler) SetCredentialAskJudge(j CredentialAskJudge) { h.askJudge = j }

// askDecision is CreateEscalation's verdict on an ask after consulting the
// judge: whether to stage it, and any note to show the human on an ESCALATE.
type askDecision struct {
	// deny is set when the judge refused the ask outright — nothing is staged.
	deny bool
	// reason is the judge's explanation (shown to the agent on deny, or to the
	// human as a note on escalate).
	reason string
	// note, when non-empty, is prepended to the escalation context/inbox so the
	// human sees why the judge flagged it (ESCALATE, or a judge outage).
	note string
}

// judgeAsk consults the ask judge and maps its verdict to an askDecision. A nil
// judge or an unconfigured one means "do not gate" (stage, no note). A judge
// that ran and failed — or an infrastructure failure inside it — is an
// ESCALATE, never a DENY: a judge outage must not swallow a legitimate request,
// so it goes to a human with a note rather than being refused.
func (h *QueryHandler) judgeAsk(ctx context.Context, in CredentialAskInput) askDecision {
	if h.askJudge == nil {
		return askDecision{}
	}
	resp, err := h.askJudge.JudgeCredentialAsk(ctx, in)
	if errors.Is(err, errAskJudgeUnavailable) {
		return askDecision{}
	}
	if err != nil {
		h.logger.Warn("credential ask judge failed; routing to a human", "error", err,
			"agent_id", in.AgentID, "credential", in.CredentialName)
		return askDecision{note: "The Keeper could not evaluate this request automatically, so it is being sent to you for review."}
	}
	if resp.InfraFailure {
		h.logger.Warn("credential ask judge infra failure; routing to a human",
			"agent_id", in.AgentID, "credential", in.CredentialName, "reason", resp.Reason)
		return askDecision{note: "The Keeper could not evaluate this request automatically, so it is being sent to you for review."}
	}
	switch keeper.Decision(resp.Decision) {
	case keeper.DecisionDeny:
		return askDecision{deny: true, reason: resp.Reason}
	case keeper.DecisionEscalate:
		return askDecision{note: "The Keeper flagged this request for your review: " + resp.Reason, reason: resp.Reason}
	default: // ALLOW (and any unexpected value already floored by the tier policy)
		return askDecision{}
	}
}

// compile-time guard: KeeperHandler satisfies CredentialAskJudge.
var _ CredentialAskJudge = (*KeeperHandler)(nil)
