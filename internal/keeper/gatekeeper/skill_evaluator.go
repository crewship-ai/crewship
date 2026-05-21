// SkillReviewEvaluator (F4.1) audits stored skills and decides
// whether each should remain verified. Triggered by the SkillReview
// routine (daily sweep) or ad-hoc operator action.
//
// Decision space (mapped from the Curator's LLM response):
//
//	ALLOW    → mark verified=true; bump lifecycle clock; no inbox
//	DENY     → mark verified=false; blocking inbox item for operator
//	ESCALATE → mixed signals; non-blocking inbox item for operator
//
// The Curator aux slot is the LLM behind the decision (PRD §6 F3 —
// Haiku-class model). The skill_evaluator never talks to the LLM
// directly; it forwards through the shared Gatekeeper so we get the
// same fail-closed semantics (provider unreachable → DENY) as the
// credential-access path.
//
// Lifecycle handoff: the evaluator computes a new LifecycleState via
// skills.EvaluateTransition (pure function) but does NOT persist it.
// The caller (the API handler in internal/api/keeper_phase2.go)
// performs the DB UPDATE inside the same transaction that records the
// keeper_requests row so a partial commit can't desync the two tables.
package gatekeeper

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillReviewEvaluator wraps a Gatekeeper with the per-skill snapshot
// loader the F4.1 prompt needs. Constructed once per process; safe
// for concurrent use because Gatekeeper itself is.
type SkillReviewEvaluator struct {
	gk     *Gatekeeper
	logger *slog.Logger
	now    func() time.Time
}

// NewSkillReviewEvaluator builds an F4.1 evaluator wired to the
// supplied Gatekeeper. The provider on gk should be the Curator
// aux-slot provider (llm.SlotCurator from llm.ResolveAux) — passing
// the lead-model provider works but burns lead tokens on a low-stakes
// audit, which is exactly what F3 is designed to avoid.
func NewSkillReviewEvaluator(gk *Gatekeeper, logger *slog.Logger) *SkillReviewEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &SkillReviewEvaluator{gk: gk, logger: logger, now: time.Now}
}

// SkillReviewRequest is the F4.1 evaluator input. The handler in
// keeper_phase2.go assembles it from a single SELECT against skills +
// skill_invocations + agent_skills + journal aggregates.
type SkillReviewRequest struct {
	SkillID          string
	SkillName        string
	SkillDescription string
	WorkspaceID      string
	AgentName        string // requesting / reviewing agent (the routine sets this to "system")
	CrewName         string
	LifecycleSnap    skills.LifecycleSnapshot
	AssignedAgents   []string
	Stats            SkillStats
	FailureSnippets  []string
}

// SkillReviewResult is the structured outcome of a F4.1 evaluation.
// Decision drives the verify/inbox wiring; ProposedLifecycle is the
// pure-function result the caller persists alongside the verification
// flip. ProposedLifecycle.Next == LifecycleSnap.Current is the no-op
// case (still safe to write — idempotent UPDATE).
type SkillReviewResult struct {
	Decision            keeper.Decision
	Reason              string
	RiskScore           int
	ProposedLifecycle   skills.Transition
	VerifyAfterDecide   bool // true when Decision == ALLOW
	UnverifyAfterDecide bool // true when Decision == DENY

	// Prompt + RawLLMResponse populated for the audit trail (keeper_
	// requests.ollama_prompt / .ollama_raw_response). Truncated by
	// the upstream Gatekeeper, so callers can persist them directly.
	Prompt         string
	RawLLMResponse string
}

// Evaluate runs the F4.1 prompt against the Curator aux model and
// resolves the Decision triple-state into a SkillReviewResult.
// Fail-closed: any LLM error / unparseable response yields ESCALATE
// (operator review), not DENY — F4.1 is an audit, not a security
// gate, and silently unverifying skills on a flaky LLM is worse than
// surfacing the noise to the operator.
func (e *SkillReviewEvaluator) Evaluate(ctx context.Context, req SkillReviewRequest) (SkillReviewResult, error) {
	if e.gk == nil {
		return SkillReviewResult{}, fmt.Errorf("skill_evaluator: nil gatekeeper")
	}
	if strings.TrimSpace(req.SkillID) == "" {
		return SkillReviewResult{}, fmt.Errorf("skill_evaluator: empty skill_id")
	}

	transition := skills.EvaluateTransition(req.LifecycleSnap)

	evalReq := EvalRequest{
		Request: keeper.Request{
			ID:                req.SkillID + "_review",
			RequestingAgentID: req.AgentName,
			RequestingCrewID:  req.CrewName,
			Intent:            fmt.Sprintf("F4.1 skill review for %q", req.SkillName),
			WorkspaceID:       req.WorkspaceID,
			CreatedAt:         e.now(),
			RequestType:       keeper.RequestTypeSkillReview,
		},
		CredentialName: "n/a", // F4.1 is not a credential request
		SecurityLevel:  keeper.SecurityLevelL1,
		AgentName:      req.AgentName,
		CrewName:       req.CrewName,
		RequestType:    keeper.RequestTypeSkillReview,
		SkillReview: &SkillReviewInput{
			SkillID:          req.SkillID,
			SkillName:        req.SkillName,
			SkillDescription: req.SkillDescription,
			LifecycleState:   string(req.LifecycleSnap.Current),
			AssignedAgents:   req.AssignedAgents,
			Stats:            req.Stats,
			FailureSnippets:  req.FailureSnippets,
		},
	}

	resp, err := e.gk.Evaluate(ctx, evalReq)
	if err != nil {
		e.logger.Error("skill_evaluator: gatekeeper error → ESCALATE",
			"skill_id", req.SkillID, "error", err)
		return SkillReviewResult{
			Decision:          keeper.DecisionEscalate,
			Reason:            fmt.Sprintf("Curator LLM error: %v — operator review", err),
			RiskScore:         5,
			ProposedLifecycle: transition,
		}, nil
	}

	out := SkillReviewResult{
		Reason:            resp.Reason,
		RiskScore:         resp.RiskScore,
		ProposedLifecycle: transition,
		Prompt:            resp.Prompt,
		RawLLMResponse:    resp.RawLLMResponse,
	}

	// Audit-path widening: the underlying Gatekeeper fails closed to
	// DENY for any LLM error / unparseable response / unknown-decision
	// value (correct for the credential-access path — safer to refuse
	// than allow on a flaky model). For F4.1 audits, silent DENY would
	// unverify skills based on infrastructure flakiness OR a model
	// hallucinating a fourth decision verb; ESCALATE surfaces the
	// noise to the operator instead. Detect via:
	//   a) the Gatekeeper's fallback Reason strings, or
	//   b) the raw LLM response containing a decision value outside
	//      the closed set {ALLOW, DENY, ESCALATE}.
	if resp.Decision == string(keeper.DecisionDeny) &&
		(isLLMFailureDeny(resp.Reason) || isUnknownDecisionInRaw(resp.RawLLMResponse)) {
		e.logger.Warn("skill_evaluator: widening fail-closed DENY → ESCALATE for audit path",
			"skill_id", req.SkillID, "underlying_reason", resp.Reason)
		out.Decision = keeper.DecisionEscalate
		out.Reason = "Curator unavailable or returned unparseable response — operator review (underlying: " + resp.Reason + ")"
		return out, nil
	}

	switch resp.Decision {
	case string(keeper.DecisionAllow):
		out.Decision = keeper.DecisionAllow
		out.VerifyAfterDecide = true
	case string(keeper.DecisionDeny):
		out.Decision = keeper.DecisionDeny
		out.UnverifyAfterDecide = true
	case string(keeper.DecisionEscalate):
		out.Decision = keeper.DecisionEscalate
	default:
		// Anything else (the Gatekeeper already normalised to one of
		// the three valid values on the access path, but defense in
		// depth for future divergence).
		e.logger.Warn("skill_evaluator: unknown decision → ESCALATE",
			"decision", resp.Decision, "skill_id", req.SkillID)
		out.Decision = keeper.DecisionEscalate
		out.Reason = fmt.Sprintf("Curator returned unparseable decision %q — operator review", resp.Decision)
	}
	return out, nil
}

// isLLMFailureDeny detects the underlying Gatekeeper's three
// fail-closed DENY paths (provider nil, provider error, unparseable
// response). The Reason strings are stable constants in
// gatekeeper.Evaluate — if those change, this matcher must too.
func isLLMFailureDeny(reason string) bool {
	return strings.Contains(reason, "Keeper LLM unavailable") ||
		strings.Contains(reason, "Keeper LLM returned unparseable response") ||
		strings.Contains(reason, "Keeper LLM not configured")
}

// isUnknownDecisionInRaw detects the case where the LLM returned a
// well-formed JSON body but with a `decision` value outside the
// closed set {ALLOW, DENY, ESCALATE}. The Gatekeeper normalises such
// values to DENY (correct for the credential-access path); the F4.1
// audit path widens them to ESCALATE so a hallucinated decision verb
// doesn't silently unverify a skill.
//
// Implemented as a lightweight substring check on the raw response
// rather than a re-parse — avoids re-introducing the dependency on
// the JSON decode + adds defense against the LLM emitting decorated
// values like "decision: \"PROBABLY ALLOW\"" which the underlying
// parser would normalise to DENY too.
func isUnknownDecisionInRaw(raw string) bool {
	if raw == "" {
		return false
	}
	low := strings.ToUpper(raw)
	// If any of the three valid verbs appear in the raw response, treat
	// the DENY as honest (the LLM actually picked DENY). Only widen
	// when none of the three are present.
	return !strings.Contains(low, "ALLOW") &&
		!strings.Contains(low, "DENY") &&
		!strings.Contains(low, "ESCALATE")
}

// ensure llm.Provider import isn't dropped if future tests stub it
// directly. The build will fail without it once the F4.1 endpoint
// handler in internal/api adds the wiring.
var _ = llm.RoleUser
