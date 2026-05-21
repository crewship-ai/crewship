package gatekeeper_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/skills"
)

// goldenSkillReview pins the Curator-prompt → decision mapping for
// the four canonical outcomes (ALLOW, DENY, ESCALATE, LLM failure →
// ESCALATE). The mock provider returns a stubbed JSON body keyed by
// the input scenario so the test is fully deterministic.
func TestSkillReviewEvaluator_Golden(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	baseReq := gatekeeper.SkillReviewRequest{
		SkillID:          "sk_review",
		SkillName:        "deploy-runner",
		SkillDescription: "Runs deploy.sh against the staging cluster",
		WorkspaceID:      "ws1",
		AgentName:        "Reviewer",
		CrewName:         "Ops",
		LifecycleSnap: skills.LifecycleSnapshot{
			Current: skills.LifecycleActive, LastUsedAt: now.Add(-3 * 24 * time.Hour),
			ActiveAssignments: 1, Now: now,
		},
		AssignedAgents: []string{"deployer-prod"},
		Stats:          gatekeeper.SkillStats{InvocationCount: 8, ErrorCount: 1, LookbackDays: 30, LastUsedAt: now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)},
	}

	tests := []struct {
		name         string
		llmResponse  string
		llmErr       error
		wantDec      keeper.Decision
		wantVerify   bool
		wantUnver    bool
		wantInPrompt []string
	}{
		{
			name:         "ALLOW — skill in active use",
			llmResponse:  `{"decision":"ALLOW","reason":"actively used and assigned","risk":2}`,
			wantDec:      keeper.DecisionAllow,
			wantVerify:   true,
			wantInPrompt: []string{"SKILL UNDER REVIEW", "deploy-runner", "deployer-prod"},
		},
		{
			name:        "DENY — abandoned + erroring",
			llmResponse: `{"decision":"DENY","reason":"no usage and 100% error rate","risk":8}`,
			wantDec:     keeper.DecisionDeny,
			wantUnver:   true,
		},
		{
			name:        "ESCALATE — mixed signals",
			llmResponse: `{"decision":"ESCALATE","reason":"high usage but high errors; operator should review","risk":6}`,
			wantDec:     keeper.DecisionEscalate,
		},
		{
			name:    "LLM error → ESCALATE (fail-soft for audit)",
			llmErr:  fmt.Errorf("connection refused"),
			wantDec: keeper.DecisionEscalate,
		},
		{
			name:        "unknown decision → ESCALATE",
			llmResponse: `{"decision":"MAYBE","reason":"why not","risk":5}`,
			wantDec:     keeper.DecisionEscalate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &mockProvider{content: tc.llmResponse, err: tc.llmErr}
			gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
			ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())

			got, err := ev.Evaluate(context.Background(), baseReq)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got.Decision != tc.wantDec {
				t.Errorf("Decision = %q, want %q (reason=%q)", got.Decision, tc.wantDec, got.Reason)
			}
			if got.VerifyAfterDecide != tc.wantVerify {
				t.Errorf("VerifyAfterDecide = %v, want %v", got.VerifyAfterDecide, tc.wantVerify)
			}
			if got.UnverifyAfterDecide != tc.wantUnver {
				t.Errorf("UnverifyAfterDecide = %v, want %v", got.UnverifyAfterDecide, tc.wantUnver)
			}
			// Lifecycle: assignment-trumps-timer means baseReq's snapshot
			// should always propose active.
			if got.ProposedLifecycle.Next != skills.LifecycleActive {
				t.Errorf("ProposedLifecycle.Next = %q, want active (assignment-trumps-timer)", got.ProposedLifecycle.Next)
			}
			// Prompt substring checks only meaningful on successful LLM call.
			if tc.llmErr == nil {
				for _, want := range tc.wantInPrompt {
					if !strings.Contains(p.capturedPrompt, want) {
						t.Errorf("prompt missing %q\n---\n%s\n---", want, p.capturedPrompt)
					}
				}
			}
		})
	}
}

func TestSkillReviewEvaluator_RejectsEmptySkillID(t *testing.T) {
	gk := gatekeeper.New(&mockProvider{}, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, newTestLogger())
	_, err := ev.Evaluate(context.Background(), gatekeeper.SkillReviewRequest{})
	if err == nil {
		t.Error("expected error on empty SkillID")
	}
}
