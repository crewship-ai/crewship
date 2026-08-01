package gatekeeper_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// The conversation history is "the last N messages" — bounded in COUNT, not in
// tokens. The reference deployment runs num_ctx 4096, and the prompt is assembled
// policy → tier → evidence → history → request → criteria. A model server
// truncates from the front, so the first thing a long conversation pushes out is
// the operator's watch policy and the credential's tier.
//
// That is not lost context, it is a silent security downgrade: the judge keeps
// answering, in the right format, having never been told the rules it was meant
// to apply. Nothing in the response distinguishes it from a considered verdict —
// the same property that let #1624 run for milestones.
//
// So the budget is enforced HERE, where the prompt is built and the order is
// known, rather than being left to whoever truncates last.
func TestBuildPrompt_BudgetTruncatesHistoryNotPolicy(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":3}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger(),
		gatekeeper.WithWatchSpecResolver(func(context.Context, string) string {
			return "Flag any request that mentions production outside a declared incident."
		}))

	// ~40k characters of conversation against a 1000-token (~4k char) budget.
	huge := strings.Repeat("the agent said something at length. ", 1100)

	_, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agt_riley",
			Intent:            "Rotate the expiring TLS certificates on staging-web-01 and reload nginx",
		},
		SecurityLevel:      keeper.SecurityLevelL3,
		CredentialName:     "KT_SSH_STAGING",
		AgentName:          "riley",
		CrewName:           "ops",
		ConvHistory:        huge,
		PromptBudgetTokens: 1000,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	prompt := p.capturedPrompt
	if prompt == "" {
		t.Fatal("no prompt was built")
	}

	// The incompressible sections must all be present.
	for _, must := range []string{
		"WORKSPACE WATCH POLICY",       // the operator's rules
		"CREDENTIAL TIER",              // the tier policy
		"CURRENT REQUEST TO EVALUATE",  // what is being asked
		"KT_SSH_STAGING",               // the credential
		"Respond with ONLY valid JSON", // the output contract
	} {
		if !strings.Contains(prompt, must) {
			t.Errorf("budget dropped an incompressible section: %q missing", must)
		}
	}

	// And the thing that actually shrank is the history.
	if strings.Count(prompt, "the agent said something at length.") > 200 {
		t.Errorf("history was not truncated: prompt is %d chars", len(prompt))
	}
	// ~4 chars per token is the estimator; allow generous slack over the budget
	// so this asserts "bounded", not an exact tokenizer.
	if len(prompt) > 1000*4*2 {
		t.Errorf("prompt is %d chars, far over a 1000-token budget", len(prompt))
	}
}

// A truncated history must SAY it was truncated. A judge shown three messages
// out of ninety, with nothing to mark the cut, reads an absence of corroboration
// as evidence of its absence — and the criteria explicitly ask it to weigh
// whether the conversation supports the request.
func TestBuildPrompt_TruncationIsDisclosedToTheJudge(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":3}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	_, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agt_riley",
			Intent:            "Rotate the expiring TLS certificates on staging-web-01 and reload nginx",
		},
		SecurityLevel:      keeper.SecurityLevelL3,
		CredentialName:     "KT_SSH_STAGING",
		AgentName:          "riley",
		ConvHistory:        strings.Repeat("chatter. ", 5000),
		PromptBudgetTokens: 900,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !strings.Contains(strings.ToLower(p.capturedPrompt), "truncat") {
		t.Error("the history was cut without telling the judge, so missing corroboration is indistinguishable from absent corroboration")
	}
}

// No budget means the pre-existing behaviour, byte for byte. An operator who has
// not set one must not discover that their prompts silently changed shape.
func TestBuildPrompt_NoBudgetLeavesHistoryIntact(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":3}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	history := strings.Repeat("a distinctive line of conversation. ", 500)
	_, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agt_riley",
			Intent:            "Rotate the expiring TLS certificates on staging-web-01 and reload nginx",
		},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "KT_SSH_STAGING",
		AgentName:      "riley",
		ConvHistory:    history,
		// PromptBudgetTokens deliberately unset.
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !strings.Contains(p.capturedPrompt, history) {
		t.Error("history was trimmed with no budget configured")
	}
}
