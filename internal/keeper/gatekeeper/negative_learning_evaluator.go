// NegativeLearningEvaluator (F4.4) is the failure-driven lessons.md
// writer. Triggered by EventRunFailed, EntryGuardrailOutput
// warn|error, or EntryKeeperDecision DENY on /execute. The Negative
// aux slot decides whether the failure is signal worth persisting
// (ALLOW), noise to drop (DENY), or borderline (ESCALATE → operator).
//
// This is the first real consumer of Z.7's lesson_writer primitive
// (PRD §6 F4.4). ALLOW → consolidate.WriteLesson with
// LessonKindNegative + LessonSourceNegativeLearning so the lesson
// surfaces in the agent's next system-prompt assembly as guardrails
// ("you've previously failed at X — try Y first").
//
// Decision matrix:
//
//	ALLOW    → write lesson, idempotent by LessonID (the handler
//	           generates a stable ID from trigger + truncated snippet
//	           hash so re-fires of the same failure dedup at the
//	           writer rather than spam the file).
//	DENY     → drop (transient / duplicate); journal-only audit entry.
//	ESCALATE → operator review via inbox; no lesson write until
//	           operator decides.
//
// The pure-evaluation path does NOT call lesson_writer.WriteLesson —
// that's the API handler's job (C.9). The evaluator returns a
// LessonProposal struct describing what would be written; the handler
// performs the side effect after persisting the keeper_requests row.
// Separation keeps the evaluator testable without a temp dir.
package gatekeeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper"
)

// NegativeTrigger enumerates the four event kinds the F4.4 evaluator
// is sensitive to. Stored as a string on the inbound request so the
// hook handler can pass the same value verbatim from
// journal/EventName without a translation table.
type NegativeTrigger string

const (
	NegTriggerRunFailed         NegativeTrigger = "run_failed"
	NegTriggerGuardrailWarn     NegativeTrigger = "guardrail_warn"
	NegTriggerGuardrailError    NegativeTrigger = "guardrail_error"
	NegTriggerKeeperExecuteDeny NegativeTrigger = "keeper_execute_deny"
)

// NegativeLearningEvaluator wraps the Gatekeeper bound to the Negative
// aux slot.
type NegativeLearningEvaluator struct {
	gk     *Gatekeeper
	logger *slog.Logger
	now    func() time.Time
}

// NewNegativeLearningEvaluator constructs the F4.4 evaluator.
func NewNegativeLearningEvaluator(gk *Gatekeeper, logger *slog.Logger) *NegativeLearningEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &NegativeLearningEvaluator{gk: gk, logger: logger, now: time.Now}
}

// NegativeLearningRequest is the F4.4 input. PriorLesson lets the
// evaluator suppress duplicates — pre-loaded by the handler with the
// most-recent lessons.md entry on the same trigger kind (if any).
type NegativeLearningRequest struct {
	WorkspaceID    string
	CrewID         string
	AgentName      string
	CrewName       string
	AgentMemoryDir string // absolute path under which lessons.md lives
	Trigger        NegativeTrigger
	ToolName       string // optional — empty for run_failed
	FailureSnippet string // raw event payload, will be truncated
	PriorLesson    string // most-recent same-kind lesson; empty if none
}

// LessonProposal is the rendered lesson the handler will pass to
// consolidate.WriteLesson when Decision == ALLOW. ID is the stable
// idempotency key — same trigger + same snippet shape collapses to
// the same lesson row even if the evaluator fires twice on retries.
type LessonProposal struct {
	ID     string
	Kind   consolidate.LessonKind
	Source consolidate.LessonSource
	Rule   string
	Note   string
}

// NegativeLearningResult is the structured outcome.
type NegativeLearningResult struct {
	Decision       keeper.Decision
	Reason         string
	RiskScore      int
	WriteLesson    bool           // true when Decision == ALLOW
	Proposal       LessonProposal // populated when WriteLesson == true
	Prompt         string
	RawLLMResponse string
}

// Evaluate runs the F4.4 prompt and decides whether to propose a
// lesson write. Does NOT touch the filesystem; the handler performs
// the WriteLesson call.
func (e *NegativeLearningEvaluator) Evaluate(ctx context.Context, req NegativeLearningRequest) (NegativeLearningResult, error) {
	if e.gk == nil {
		return NegativeLearningResult{}, fmt.Errorf("negative_learning_evaluator: nil gatekeeper")
	}
	if !validNegativeTrigger(req.Trigger) {
		return NegativeLearningResult{}, fmt.Errorf("negative_learning_evaluator: invalid trigger %q", req.Trigger)
	}
	if strings.TrimSpace(req.FailureSnippet) == "" {
		return NegativeLearningResult{}, fmt.Errorf("negative_learning_evaluator: empty failure_snippet (no signal)")
	}

	evalReq := EvalRequest{
		Request: keeper.Request{
			ID:                fmt.Sprintf("neglearn_%s_%d", req.AgentName, e.now().UnixNano()),
			RequestingAgentID: req.AgentName,
			RequestingCrewID:  req.CrewID,
			Intent:            fmt.Sprintf("F4.4 negative learning from %s", req.Trigger),
			WorkspaceID:       req.WorkspaceID,
			CreatedAt:         e.now(),
			RequestType:       keeper.RequestTypeNegativeLearning,
		},
		CredentialName: "n/a",
		SecurityLevel:  keeper.SecurityLevelL1,
		AgentName:      req.AgentName,
		CrewName:       req.CrewName,
		RequestType:    keeper.RequestTypeNegativeLearning,
		NegativeLesson: &NegativeLearningInput{
			TriggerKind:    string(req.Trigger),
			FailureSnippet: req.FailureSnippet,
			ToolName:       req.ToolName,
			PriorLesson:    req.PriorLesson,
		},
	}

	resp, err := e.gk.Evaluate(ctx, evalReq)
	if err != nil {
		// Fail-soft: LLM unreachable → ESCALATE (operator review),
		// never silently write a lesson or silently drop one.
		e.logger.Error("negative_learning_evaluator: gatekeeper error → ESCALATE",
			"agent", req.AgentName, "trigger", req.Trigger, "error", err)
		return NegativeLearningResult{
			Decision:  keeper.DecisionEscalate,
			Reason:    fmt.Sprintf("Negative LLM error: %v — operator review", err),
			RiskScore: 5,
		}, nil
	}

	out := NegativeLearningResult{
		Reason:         resp.Reason,
		RiskScore:      resp.RiskScore,
		Prompt:         resp.Prompt,
		RawLLMResponse: resp.RawLLMResponse,
	}

	// Fail-soft widening — same principle as the other F4 evaluators.
	// A fallback DENY (LLM unreachable / unparseable) must not silently
	// drop a potentially valuable lesson.
	if resp.Decision == string(keeper.DecisionDeny) &&
		(isLLMFailureDeny(resp.Reason) || isUnknownDecisionInRaw(resp.RawLLMResponse)) {
		out.Decision = keeper.DecisionEscalate
		out.Reason = "Negative Curator unavailable or unparseable — operator review (underlying: " + resp.Reason + ")"
		return out, nil
	}

	switch resp.Decision {
	case string(keeper.DecisionAllow):
		out.Decision = keeper.DecisionAllow
		out.WriteLesson = true
		out.Proposal = buildLessonProposal(req, resp.Reason)
	case string(keeper.DecisionDeny):
		out.Decision = keeper.DecisionDeny
	case string(keeper.DecisionEscalate):
		out.Decision = keeper.DecisionEscalate
	default:
		out.Decision = keeper.DecisionEscalate
		out.Reason = fmt.Sprintf("Unparseable decision %q — operator review", resp.Decision)
	}
	return out, nil
}

// buildLessonProposal renders the LessonEntry-shaped intent the
// handler will pass to consolidate.WriteLesson. The Rule body uses
// the LLM's reason as the human-readable takeaway; the Note carries
// the trigger + tool for downstream filtering. ID is a deterministic
// hash so repeat fires of the identical failure converge on the
// same lesson row (idempotency at the lesson_writer level too).
func buildLessonProposal(req NegativeLearningRequest, llmReason string) LessonProposal {
	// Stable ID: sha256(trigger + tool + first 256 chars of snippet).
	// Truncating the snippet to 256 chars makes the idempotency key
	// resilient to small differences (timestamps in the payload, line
	// numbers shifting) while still distinguishing genuinely different
	// failures. 16 hex chars = 64-bit space, plenty for per-agent.
	snip := req.FailureSnippet
	if len(snip) > 256 {
		snip = snip[:256]
	}
	h := sha256.Sum256([]byte(string(req.Trigger) + "|" + req.ToolName + "|" + snip))
	id := "neg_" + hex.EncodeToString(h[:8])

	rule := strings.TrimSpace(llmReason)
	if rule == "" {
		rule = fmt.Sprintf("Avoid the failure mode observed in %s.", req.Trigger)
	}

	note := fmt.Sprintf("trigger=%s", req.Trigger)
	if req.ToolName != "" {
		note += "; tool=" + req.ToolName
	}

	return LessonProposal{
		ID:     id,
		Kind:   consolidate.LessonKindNegative,
		Source: consolidate.LessonSourceNegativeLearning,
		Rule:   rule,
		Note:   note,
	}
}

func validNegativeTrigger(t NegativeTrigger) bool {
	switch t {
	case NegTriggerRunFailed, NegTriggerGuardrailWarn,
		NegTriggerGuardrailError, NegTriggerKeeperExecuteDeny:
		return true
	}
	return false
}
