package gatekeeper_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/policy"
)

// TestBehaviorEvaluator_DualMode pins the F4.2 (decision, mode,
// autonomy) → (policy.Decision, ShouldBlock) matrix. The dual-mode
// degradation is the entire point of F4.2 — regress here if a future
// change touches applyBehaviorPolicy.
func TestBehaviorEvaluator_DualMode(t *testing.T) {
	tests := []struct {
		name         string
		llmResponse  string
		mode         policy.BehaviorMode
		level        policy.AutonomyLevel
		wantDec      gatekeeper.BehaviorDecision
		wantPolicy   policy.Decision
		wantBlock    bool
		wantInPrompt string
	}{
		// warn mode — never blocks
		{
			name:         "warn × ALLOW × guided → journal, no block",
			llmResponse:  `{"decision":"ALLOW","reason":"normal","risk":1}`,
			mode:         policy.BehaviorWarn,
			level:        policy.AutonomyGuided,
			wantDec:      gatekeeper.BehaviorAllow,
			wantPolicy:   policy.DecisionAutoJournal,
			wantBlock:    false,
			wantInPrompt: "Behavior mode: warn",
		},
		{
			name:        "warn × WARN × guided → inbox, no block",
			llmResponse: `{"decision":"WARN","reason":"tight loop","risk":4}`,
			mode:        policy.BehaviorWarn,
			level:       policy.AutonomyGuided,
			wantDec:     gatekeeper.BehaviorWarn,
			wantPolicy:  policy.DecisionAutoLogInbox,
			wantBlock:   false,
		},
		{
			name:        "warn × DENY × guided → degrades to inbox, no block",
			llmResponse: `{"decision":"DENY","reason":"anti-pattern","risk":7}`,
			mode:        policy.BehaviorWarn,
			level:       policy.AutonomyGuided,
			wantDec:     gatekeeper.BehaviorDeny,
			wantPolicy:  policy.DecisionAutoLogInbox,
			wantBlock:   false,
		},
		{
			name:        "warn × DENY × full → journal-only (full degrades inbox)",
			llmResponse: `{"decision":"DENY","reason":"anti-pattern","risk":7}`,
			mode:        policy.BehaviorWarn,
			level:       policy.AutonomyFull,
			wantDec:     gatekeeper.BehaviorDeny,
			wantPolicy:  policy.DecisionAutoJournal,
			wantBlock:   false,
		},

		// block mode — DENY blocks at strict/guided/trusted
		{
			name:         "block × DENY × guided → BlockInbox + block",
			llmResponse:  `{"decision":"DENY","reason":"destructive sequence","risk":9}`,
			mode:         policy.BehaviorBlock,
			level:        policy.AutonomyGuided,
			wantDec:      gatekeeper.BehaviorDeny,
			wantPolicy:   policy.DecisionBlockInbox,
			wantBlock:    true,
			wantInPrompt: "Behavior mode: block",
		},
		{
			name:        "block × DENY × strict → BlockInbox + block",
			llmResponse: `{"decision":"DENY","reason":"destructive sequence","risk":9}`,
			mode:        policy.BehaviorBlock,
			level:       policy.AutonomyStrict,
			wantDec:     gatekeeper.BehaviorDeny,
			wantPolicy:  policy.DecisionBlockInbox,
			wantBlock:   true,
		},
		{
			name:        "block × DENY × trusted → BlockJournal + block",
			llmResponse: `{"decision":"DENY","reason":"destructive sequence","risk":9}`,
			mode:        policy.BehaviorBlock,
			level:       policy.AutonomyTrusted,
			wantDec:     gatekeeper.BehaviorDeny,
			wantPolicy:  policy.DecisionBlockJournal,
			wantBlock:   true,
		},

		// escalate paths
		{
			name:        "block × ESCALATE × guided → BlockInbox + block",
			llmResponse: `{"decision":"ESCALATE","reason":"ambiguous","risk":6}`,
			mode:        policy.BehaviorBlock,
			level:       policy.AutonomyGuided,
			wantDec:     gatekeeper.BehaviorEscalate,
			wantPolicy:  policy.DecisionBlockInbox,
			wantBlock:   true,
		},
		{
			name:        "warn × ESCALATE × trusted → inbox, no block",
			llmResponse: `{"decision":"ESCALATE","reason":"ambiguous","risk":6}`,
			mode:        policy.BehaviorWarn,
			level:       policy.AutonomyTrusted,
			wantDec:     gatekeeper.BehaviorEscalate,
			wantPolicy:  policy.DecisionAutoLogInbox,
			wantBlock:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &mockProvider{content: tc.llmResponse}
			gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
			ev := gatekeeper.NewBehaviorEvaluator(gk, newTestLogger())

			res, err := ev.Evaluate(context.Background(), gatekeeper.BehaviorReviewRequest{
				WorkspaceID:     "ws1",
				CrewID:          "cr1",
				AgentName:       "agent-a",
				CrewName:        "Ops",
				BehaviorMode:    tc.mode,
				AutonomyLevel:   tc.level,
				ToolName:        "shell_exec",
				ToolArgsSnippet: `{"cmd":"rm -rf /tmp"}`,
				RecentToolCalls: []string{"shell_exec", "shell_exec", "shell_exec"},
			})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if res.Decision != tc.wantDec {
				t.Errorf("Decision = %q, want %q", res.Decision, tc.wantDec)
			}
			if res.PolicyDecision != tc.wantPolicy {
				t.Errorf("PolicyDecision = %q, want %q", res.PolicyDecision, tc.wantPolicy)
			}
			if res.ShouldBlock != tc.wantBlock {
				t.Errorf("ShouldBlock = %v, want %v", res.ShouldBlock, tc.wantBlock)
			}
			if tc.wantInPrompt != "" && !strings.Contains(p.capturedPrompt, tc.wantInPrompt) {
				t.Errorf("prompt missing %q\n---\n%s\n---", tc.wantInPrompt, p.capturedPrompt)
			}
		})
	}
}

func TestBehaviorEvaluator_LLMError_EscalatesAndNeverBlocks(t *testing.T) {
	p := &mockProvider{err: context.DeadlineExceeded}
	gk := gatekeeper.New(p, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newTestLogger())

	// Even in block mode, an LLM error must not block — fail-soft for
	// an audit-style evaluator (PR-C principle).
	res, err := ev.Evaluate(context.Background(), gatekeeper.BehaviorReviewRequest{
		WorkspaceID:   "ws1",
		CrewID:        "cr1",
		AgentName:     "agent-a",
		CrewName:      "Ops",
		BehaviorMode:  policy.BehaviorBlock,
		AutonomyLevel: policy.AutonomyGuided,
		ToolName:      "shell_exec",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != gatekeeper.BehaviorEscalate {
		t.Errorf("Decision = %q, want ESCALATE on LLM error", res.Decision)
	}
	if res.ShouldBlock {
		t.Error("ShouldBlock = true on LLM error; fail-soft requires false")
	}
}

func TestBehaviorEvaluator_RejectsEmptyTool(t *testing.T) {
	gk := gatekeeper.New(&mockProvider{}, "claude-haiku-4-5", newTestLogger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, newTestLogger())
	_, err := ev.Evaluate(context.Background(), gatekeeper.BehaviorReviewRequest{})
	if err == nil {
		t.Error("expected error on empty ToolName")
	}
}
