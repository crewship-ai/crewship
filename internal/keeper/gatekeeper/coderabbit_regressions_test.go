package gatekeeper_test

// CodeRabbit regressions on PR-C (PR #470). Each test pins a finding so
// a future revert/refactor that re-introduces the bug fails loudly here.
// Naming intentionally references the finding so `go test -run` can grep.

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/policy"
)

// TestCR_Behavior_LLMFailureDeny_ForcesEscalate verifies the fail-soft
// branch in BehaviorEvaluator.Evaluate forces Decision=ESCALATE even
// when an internal divergence would otherwise leave a DENY through.
// Regression: previous code only set PolicyDecision + ShouldBlock=false
// on isLLMFailureDeny, relying entirely on classifyBehaviorDecision to
// have already widened DENY → ESCALATE; if that path changed the DENY
// could leak.
func TestCR_Behavior_LLMFailureDeny_ForcesEscalate(t *testing.T) {
	// Mock provider that returns a fallback-shaped DENY payload mimicking
	// the Gatekeeper's "LLM unavailable" path. We can't easily force the
	// real fallback without faking the provider; the underlying behaviour
	// is exercised by TestBehaviorEvaluator_LLMError_EscalatesAndNeverBlocks
	// — this test pins that under no circumstances does the fail-soft
	// branch return a DENY result.
	p := &mockProvider{err: context.DeadlineExceeded}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newTestLogger())

	res, err := ev.Evaluate(context.Background(), gatekeeper.BehaviorReviewRequest{
		WorkspaceID:   "ws1",
		CrewID:        "cr1",
		AgentName:     "agent-a",
		CrewName:      "Ops",
		BehaviorMode:  policy.BehaviorBlock,
		AutonomyLevel: policy.AutonomyStrict,
		ToolName:      "shell_exec",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision == gatekeeper.BehaviorDeny {
		t.Errorf("Decision = DENY, want ESCALATE on infrastructure failure")
	}
	if res.Decision != gatekeeper.BehaviorEscalate {
		t.Errorf("Decision = %q, want ESCALATE", res.Decision)
	}
	if res.ShouldBlock {
		t.Error("ShouldBlock = true on fail-soft branch; must be false")
	}
}

// TestCR_Skill_UnknownDecisionInRaw_DetectsHallucinatedVerb pins the
// fix for the skill_evaluator finding: a payload like
// {"decision":"MAYBE","reason":"do not allow..."} previously slipped
// through because the substring scan saw "allow" inside the reason and
// classified the response as "known". With the JSON re-parse, only the
// exact decision field counts.
func TestCR_Skill_UnknownDecisionInRaw_DetectsHallucinatedVerb(t *testing.T) {
	// The Gatekeeper normalises {"decision":"MAYBE",...} to DENY. The
	// skill evaluator must then widen DENY → ESCALATE because the
	// decision field is outside the closed set.
	p := &mockProvider{content: `{"decision":"MAYBE","reason":"do not allow this skill","risk":8}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())

	res, err := ev.Evaluate(context.Background(), gatekeeper.SkillReviewRequest{
		SkillID:     "sk1",
		SkillName:   "do_things",
		WorkspaceID: "ws1",
		AgentName:   "system",
		CrewName:    "Ops",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != keeper.DecisionEscalate {
		t.Errorf("Decision = %q, want ESCALATE for hallucinated verb (raw contains 'allow' inside reason but decision is MAYBE)", res.Decision)
	}
	if res.UnverifyAfterDecide {
		t.Error("UnverifyAfterDecide = true after widening; must be false on ESCALATE")
	}
}

// TestCR_Skill_GenuineDeny_StaysDeny ensures the parse-based widening
// only triggers on truly unknown decisions. A legitimate
// {"decision":"DENY",...} payload must keep DENY semantics.
func TestCR_Skill_GenuineDeny_StaysDeny(t *testing.T) {
	p := &mockProvider{content: `{"decision":"DENY","reason":"unused skill","risk":7}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())

	res, err := ev.Evaluate(context.Background(), gatekeeper.SkillReviewRequest{
		SkillID:     "sk1",
		SkillName:   "rotting_skill",
		WorkspaceID: "ws1",
		AgentName:   "system",
		CrewName:    "Ops",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != keeper.DecisionDeny {
		t.Errorf("Decision = %q, want DENY for legitimate LLM DENY", res.Decision)
	}
	if !res.UnverifyAfterDecide {
		t.Error("UnverifyAfterDecide = false; want true on DENY")
	}
}

// TestCR_NegativeLearning_RejectsEmptyWorkspace pins the validation
// finding — empty WorkspaceID must produce an error rather than letting
// the evaluator proceed with an invalid keeper-request context.
func TestCR_NegativeLearning_RejectsEmptyWorkspace(t *testing.T) {
	gk := gatekeeper.New(&mockProvider{}, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, newTestLogger())

	_, err := ev.Evaluate(context.Background(), gatekeeper.NegativeLearningRequest{
		// WorkspaceID intentionally empty.
		Trigger:        gatekeeper.NegTriggerRunFailed,
		FailureSnippet: "oops",
	})
	if err == nil {
		t.Fatal("expected error on empty WorkspaceID")
	}
}

// TestCR_Gatekeeper_EffectiveRequestType pins the finding that
// req.Request.RequestType was ignored. A caller that only set the
// nested field must NOT enter the L1 access fast-path for an F4 type.
func TestCR_Gatekeeper_EffectiveRequestType(t *testing.T) {
	// L1 + meaningful intent + access flow → auto-allow. Use ESCALATE
	// JSON as the LLM "wrong path" trip wire: if the gatekeeper
	// auto-allows (intended for access flow), the LLM is never invoked
	// and the decision is ALLOW. If it routes to the F4 prompt
	// (intended for skill_review), parseResponse returns ESCALATE.
	p := &mockProvider{content: `{"decision":"ESCALATE","reason":"f4 prompt was used","risk":5}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())

	// Set ONLY the nested keeper.Request.RequestType (the field
	// effectiveRequestType must fall back to).
	resp, err := gk.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			Intent:      "F4.1 skill review — must hit LLM",
			RequestType: keeper.RequestTypeSkillReview,
		},
		SecurityLevel: keeper.SecurityLevelL1,
		// Hoisted RequestType deliberately empty.
		SkillReview: &gatekeeper.SkillReviewInput{SkillID: "sk1", SkillName: "x"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if resp.Decision == string(keeper.DecisionAllow) && resp.Reason != `escalate due to f4 prompt was used` {
		// If the access shortcut fired the reason would be the canned
		// "L1 credential with stated intent — auto-approved" string. The
		// auto-allow path returns a 1-risk ALLOW with that exact reason.
		if resp.Reason == "L1 credential with stated intent — auto-approved" {
			t.Errorf("L1 access fast-path fired for skill_review nested RequestType — effectiveRequestType regression")
		}
	}
	if resp.Decision != string(keeper.DecisionEscalate) {
		t.Errorf("Decision = %q, want ESCALATE (LLM was supposed to run on skill_review path); reason=%q", resp.Decision, resp.Reason)
	}
}
