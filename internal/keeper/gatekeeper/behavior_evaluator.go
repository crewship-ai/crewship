// BehaviorEvaluator (F4.2) is the sampled post-tool-call behavior
// monitor. Fired by the EventPostToolCall hook handler at a sampling
// frequency configured per crew (default every Nth call); not
// invoked synchronously on every tool call to keep the hot path
// cheap.
//
// Dual-mode decision surface, per PRD §6 F4.2:
//
//	mode=warn (default)
//	  ALLOW    → no-op
//	  WARN     → non-blocking inbox item; agent continues
//	  DENY     → degraded to WARN (warn-mode never blocks)
//	  ESCALATE → non-blocking inbox item flagged for operator review
//
//	mode=block
//	  ALLOW    → no-op
//	  WARN     → non-blocking inbox item
//	  DENY     → BlockedError to interrupt the next tool call AND a
//	             blocking inbox item
//	  ESCALATE → blocking inbox item; agent paused on this crew until
//	             operator resolves
//
// The actual mapping from (decision, mode, autonomy) to the policy
// Decision verb (block_inbox / block_journal / auto_log_inbox /
// auto_journal) is done by policy.Policy.DecideBehaviorDeny — this
// evaluator's job ends at producing the LLM verdict. The hook handler
// (C.8) composes the two: evaluator verdict + policy mapping = wire
// action.
//
// PRD note: "NO pre-LLM heuristic in MVP". We intentionally do not
// add deterministic short-circuits (e.g. "if same tool 20× in a row
// → auto-DENY"). The Behavior aux model is the single decision
// authority; the sampling cadence on the hook handler keeps the LLM
// call budget bounded.
package gatekeeper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/policy"
)

// BehaviorEvaluator wraps a Gatekeeper bound to the Behavior aux slot
// (per PRD §6 F3) and produces a dual-mode-aware result.
type BehaviorEvaluator struct {
	gk     *Gatekeeper
	logger *slog.Logger
	now    func() time.Time
}

// NewBehaviorEvaluator constructs the F4.2 evaluator. The provider on
// gk should be the Behavior aux-slot provider (llm.SlotBehavior).
func NewBehaviorEvaluator(gk *Gatekeeper, logger *slog.Logger) *BehaviorEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &BehaviorEvaluator{gk: gk, logger: logger, now: time.Now}
}

// BehaviorReviewRequest is the F4.2 input. RequestingAgent / CrewID
// route to the right policy; CurrentToolCall + RecentToolCalls give
// the LLM enough context to detect tight loops / scope creep.
type BehaviorReviewRequest struct {
	WorkspaceID     string
	CrewID          string
	AgentName       string
	CrewName        string
	BehaviorMode    policy.BehaviorMode // resolved before calling the evaluator
	AutonomyLevel   policy.AutonomyLevel
	ToolName        string
	ToolArgsSnippet string
	RecentToolCalls []string
}

// BehaviorReviewResult is the structured outcome. ShouldBlock is the
// hook handler's signal to throw BlockedError on the next tool call;
// it's set when (decision == DENY OR ESCALATE) AND mode == block AND
// autonomy != full. The pre-computed PolicyDecision lets the handler
// route the inbox/journal write without re-resolving policy.
type BehaviorReviewResult struct {
	Decision  BehaviorDecision
	Reason    string
	RiskScore int

	ShouldBlock    bool
	PolicyDecision policy.Decision

	Prompt         string
	RawLLMResponse string
}

// BehaviorDecision is the F4.2-specific decision verb. Wider than
// keeper.Decision because behavior monitoring needs WARN as a
// first-class outcome (a softer signal than DENY in warn mode).
type BehaviorDecision string

const (
	BehaviorAllow    BehaviorDecision = "ALLOW"
	BehaviorWarn     BehaviorDecision = "WARN"
	BehaviorDeny     BehaviorDecision = "DENY"
	BehaviorEscalate BehaviorDecision = "ESCALATE"
)

// ErrBehaviorBlocked is the sentinel returned alongside ShouldBlock=true
// when the hook handler should interrupt the agent. The handler wraps
// this in hooks.BlockedError so the dispatcher's Block outcome path is
// reused unchanged (no parallel block protocol).
var ErrBehaviorBlocked = errors.New("keeper: behavior monitor blocked next tool call")

// Evaluate runs the F4.2 prompt against the Behavior aux model and
// resolves the dual-mode decision matrix.
func (e *BehaviorEvaluator) Evaluate(ctx context.Context, req BehaviorReviewRequest) (BehaviorReviewResult, error) {
	if e.gk == nil {
		return BehaviorReviewResult{}, fmt.Errorf("behavior_evaluator: nil gatekeeper")
	}
	if strings.TrimSpace(req.ToolName) == "" {
		return BehaviorReviewResult{}, fmt.Errorf("behavior_evaluator: empty tool_name")
	}

	mode := req.BehaviorMode
	if mode == "" {
		// Defensive default — if the handler forgot to resolve mode,
		// pick the safer of the two (warn) rather than block. PR-B's
		// resolver returns "warn" for missing rows so this should
		// never fire in practice; defense in depth for handler bugs.
		mode = policy.BehaviorWarn
	}

	evalReq := EvalRequest{
		Request: keeper.Request{
			ID:                fmt.Sprintf("behavior_%s_%d", req.AgentName, e.now().UnixNano()),
			RequestingAgentID: req.AgentName,
			RequestingCrewID:  req.CrewID,
			Intent:            fmt.Sprintf("F4.2 behavior check on tool %q", req.ToolName),
			WorkspaceID:       req.WorkspaceID,
			CreatedAt:         e.now(),
			RequestType:       keeper.RequestTypeBehavior,
		},
		CredentialName: "n/a",
		SecurityLevel:  keeper.SecurityLevelL1,
		AgentName:      req.AgentName,
		CrewName:       req.CrewName,
		RequestType:    keeper.RequestTypeBehavior,
		Behavior: &BehaviorInput{
			ToolName:        req.ToolName,
			ToolArgsSnippet: req.ToolArgsSnippet,
			RecentToolCalls: req.RecentToolCalls,
			BehaviorMode:    string(mode),
		},
	}

	resp, err := e.gk.Evaluate(ctx, evalReq)
	if err != nil {
		// Like the F4.1 path, fail-soft for an audit-style evaluator:
		// LLM unreachable → ESCALATE (operator review), never auto-block.
		e.logger.Error("behavior_evaluator: gatekeeper error → ESCALATE",
			"agent", req.AgentName, "tool", req.ToolName, "error", err)
		return BehaviorReviewResult{
			Decision:       BehaviorEscalate,
			Reason:         fmt.Sprintf("Behavior LLM error: %v — operator review", err),
			RiskScore:      5,
			PolicyDecision: policy.DecisionAutoLogInbox,
		}, nil
	}

	dec := classifyBehaviorDecision(resp.Decision, resp.Reason, resp.RawLLMResponse)
	out := BehaviorReviewResult{
		Decision:       dec,
		Reason:         resp.Reason,
		RiskScore:      resp.RiskScore,
		Prompt:         resp.Prompt,
		RawLLMResponse: resp.RawLLMResponse,
	}

	// Fail-soft override: when the underlying Gatekeeper returned its
	// fallback DENY (LLM unreachable, unparseable, unconfigured) we
	// surface ESCALATE and explicitly refuse to block, regardless of
	// mode/autonomy. Infrastructure flakiness must never interrupt an
	// agent's tool calls — the operator gets a non-blocking inbox
	// item instead. Distinct from a genuine ESCALATE from the LLM,
	// which CAN block in block × strict/guided per the matrix above.
	//
	// Decision MUST be forced to ESCALATE here too: classifyBehaviorDecision
	// returns BehaviorEscalate when isLLMFailureDeny(resp.Reason) is true,
	// but defense-in-depth so a future divergence between the two checks
	// can never leak a DENY through the fail-soft branch.
	if isLLMFailureDeny(resp.Reason) {
		out.Decision = BehaviorEscalate
		out.PolicyDecision = policy.DecisionAutoLogInbox
		out.ShouldBlock = false
		return out, nil
	}

	out.PolicyDecision, out.ShouldBlock = applyBehaviorPolicy(dec, mode, req.AutonomyLevel)
	return out, nil
}

// classifyBehaviorDecision parses the LLM's decision string. The
// Behavior prompt explicitly enumerates four verbs (ALLOW, WARN, DENY,
// ESCALATE) so a clean response is one of those. The underlying
// Gatekeeper normalises non-ALLOW/DENY/ESCALATE values to DENY (its
// closed set) — we widen WARN back here by scanning the raw response
// before falling through to the normalised value.
func classifyBehaviorDecision(normalised, reason, raw string) BehaviorDecision {
	low := strings.ToUpper(raw)
	switch {
	case strings.Contains(low, `"WARN"`) || strings.Contains(low, `"WARN" `) || strings.Contains(low, `: "WARN"`):
		return BehaviorWarn
	}
	switch normalised {
	case string(keeper.DecisionAllow):
		return BehaviorAllow
	case string(keeper.DecisionEscalate):
		return BehaviorEscalate
	case string(keeper.DecisionDeny):
		// Distinguish "LLM genuinely picked DENY" from "LLM failed and
		// Gatekeeper fell back to DENY". The latter widens to ESCALATE
		// (same fail-soft principle as F4.1).
		//
		// isLLMFailureDeny is keyed by the Gatekeeper's fallback Reason
		// constants ("Keeper LLM unavailable" etc.), so pass `reason` —
		// not the raw LLM response (which on infra failure is empty).
		// isUnknownDecisionInRaw stays keyed by raw because that's where
		// a hallucinated decision verb would appear.
		if isLLMFailureDeny(reason) || isUnknownDecisionInRaw(raw) || raw == "" {
			return BehaviorEscalate
		}
		return BehaviorDeny
	}
	return BehaviorEscalate
}

// applyBehaviorPolicy maps (decision, mode, autonomy) onto the
// policy.Decision verb the hook handler writes to the inbox/journal,
// plus the ShouldBlock flag for the hooks.BlockedError path. Encoded
// here (not on policy.Policy) because the matrix is F4.2-specific:
// the warn-mode degradation rule (DENY in warn → WARN) is unique to
// behavior monitoring.
func applyBehaviorPolicy(d BehaviorDecision, mode policy.BehaviorMode, lvl policy.AutonomyLevel) (policy.Decision, bool) {
	switch d {
	case BehaviorAllow:
		// No action — but still log to the journal for telemetry.
		return policy.DecisionAutoJournal, false

	case BehaviorWarn:
		// Non-blocking inbox at strict/guided/trusted; journal-only at full.
		if lvl == policy.AutonomyFull {
			return policy.DecisionAutoJournal, false
		}
		return policy.DecisionAutoLogInbox, false

	case BehaviorDeny:
		if mode == policy.BehaviorWarn {
			// Degrade DENY → non-blocking inbox (the rule that makes
			// warn-mode genuinely non-blocking).
			if lvl == policy.AutonomyFull {
				return policy.DecisionAutoJournal, false
			}
			return policy.DecisionAutoLogInbox, false
		}
		// block mode: reuse policy.DecideBehaviorDeny so the
		// strict/guided × block → DecisionBlockInbox mapping stays in
		// one place. AutonomyFull × block is invalid (policy.Validate
		// catches at API boundary); defensive default if it slips
		// through is DecisionBlockInbox.
		dec := policy.Policy{AutonomyLevel: lvl, BehaviorMode: mode}.DecideBehaviorDeny()
		return dec, true

	case BehaviorEscalate:
		// Operator must see. In block mode + strict/guided we also
		// pause the agent until they ack — same wire as a block.
		if mode == policy.BehaviorBlock && (lvl == policy.AutonomyStrict || lvl == policy.AutonomyGuided) {
			return policy.DecisionBlockInbox, true
		}
		if lvl == policy.AutonomyFull {
			return policy.DecisionAutoLogInbox, false
		}
		return policy.DecisionAutoLogInbox, false
	}
	return policy.DecisionAutoLogInbox, false
}
