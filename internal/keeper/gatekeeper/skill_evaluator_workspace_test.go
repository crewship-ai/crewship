package gatekeeper_test

// CRE-138 defense-in-depth: an empty WorkspaceID means the paymaster
// middleware rejects the LLM call pre-flight ("paymaster: workspace_id
// required"), which the gatekeeper swallows into deny-by-default — so a
// caller that forgets the billing workspace turns an audit sweep into a
// deny-all. The evaluator must catch it BEFORE the LLM call and ESCALATE
// (operator review), consistent with its fail-soft contract and with the
// memory-health evaluator's early guard.

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/skills"
)

func TestSkillReviewEvaluator_EmptyWorkspaceEscalatesWithoutLLMCall(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"should never be consulted","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())

	got, err := ev.Evaluate(context.Background(), gatekeeper.SkillReviewRequest{
		SkillID:   "sk_nows",
		SkillName: "no-workspace",
		// WorkspaceID deliberately empty
		LifecycleSnap: skills.LifecycleSnapshot{
			Current: skills.LifecycleActive, LastUsedAt: now.Add(-24 * time.Hour),
			ActiveAssignments: 1, Now: now,
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Decision != keeper.DecisionEscalate {
		t.Errorf("Decision = %q, want ESCALATE (empty workspace must not collapse to DENY)", got.Decision)
	}
	if got.UnverifyAfterDecide {
		t.Error("UnverifyAfterDecide = true — an unbillable review must not unverify the skill")
	}
	if p.capturedPrompt != "" {
		t.Error("LLM provider was called despite empty WorkspaceID — the paymaster would reject this pre-flight; the evaluator must short-circuit")
	}
	// Lifecycle proposal must still be computed (it is deterministic and
	// needs no LLM), so operators see the transition context in the reason.
	if got.ProposedLifecycle.Next == "" {
		t.Error("ProposedLifecycle.Next is empty — deterministic transition must still be proposed")
	}
}
