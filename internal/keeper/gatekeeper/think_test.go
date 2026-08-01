package gatekeeper_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// The judge asks for a verdict in 256 tokens and is fail-closed: anything it
// cannot parse is a DENY. A reasoning model spends that entire budget on its
// chain of thought and answers with content:"" — so a correctly configured
// Keeper on a thinking model denies every request, and logs nothing to say why.
//
// Measured against a live Ollama on qwen3.5:9b at these exact settings: with no
// think flag, 1063 chars of reasoning and an empty verdict; with think:false, a
// parseable verdict in 45 tokens. The judge must therefore turn reasoning OFF
// rather than merely report that it happened.
//
// This is about the budget, not about model quality: the fix is what makes the
// 256-token contract satisfiable at all.
func TestGatekeeper_SuppressesThinkingOnTheJudgeCall(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"proportional","risk":2}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	// L2 so the call actually reaches the LLM (L1 short-circuits to ALLOW).
	_, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "Push the release tag so CI can build the artifact",
		},
		SecurityLevel:  keeper.SecurityLevelL2,
		CredentialName: "github-deploy-token",
		AgentName:      "DeployBot",
		CrewName:       "Platform",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if p.capturedReq.Think == nil {
		t.Fatal("judge left Think unset — a thinking model will burn the 256-token budget and the verdict comes back empty, which is a DENY")
	}
	if *p.capturedReq.Think {
		t.Errorf("Think = true, want false")
	}
	// Guard the premise: if this budget ever grows, revisit whether suppressing
	// reasoning is still the right trade.
	if p.capturedReq.MaxTokens != 256 {
		t.Errorf("MaxTokens = %d, want 256 — the budget this fix exists for", p.capturedReq.MaxTokens)
	}
}
