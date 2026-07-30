package gatekeeper_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// Credential tiers, at the decision boundary.
//
// L1–L4 existed on the credentials table and in the prompt, but every tier above
// L1 reached the judge with an identical decision space — so "npm read token" and
// "production database admin" differed by one line of text the model was free to
// ignore. These tests pin the consequences: what the tier refuses before spending
// a model call, what it asks the judge, and what it does with the answer.

func tierReq(level keeper.SecurityLevel, intent string) gatekeeper.EvalRequest {
	return gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			WorkspaceID:       "ws1",
			Intent:            intent,
		},
		SecurityLevel:  level,
		CredentialName: "prod-db-admin",
		AgentName:      "DevBot",
		CrewName:       "Dev Crew",
	}
}

// A long, plausible intent — so a test that fails is failing on the tier rule and
// not on the length gate.
const goodIntent = "I am migrating the orders table to add the shipped_at column, as agreed in the thread above, and need admin access to run the ALTER on the production replica first."

// The reason an operator marks a credential L4: the judge may vouch for the
// request but not grant it.
func TestTier_L4AllowBecomesEscalate(t *testing.T) {
	mock := &mockProvider{content: `{"decision":"ALLOW","reason":"looks fine","risk":2}`}
	g := gatekeeper.New(mock, "test-model", newTestLogger())

	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL4, goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionEscalate) {
		t.Fatalf("decision = %s, want ESCALATE — the model must not be able to grant L4", resp.Decision)
	}
	// The human opening the escalation has to learn why from the reason itself.
	if !strings.Contains(strings.ToLower(resp.Reason), "human") {
		t.Errorf("reason %q does not say a human has to approve", resp.Reason)
	}
	// A DENY-notify threshold is a risk comparison, so a critical decision scored
	// 2 by the model would never reach anybody.
	if resp.RiskScore < keeper.SecurityLevelL4.Tier().MinRisk {
		t.Errorf("risk = %d, want at least the L4 floor", resp.RiskScore)
	}
}

// L3 is high, not critical. If both escalated, L4 would mean nothing and the
// inbox would fill with routine infrastructure work.
func TestTier_L3AllowStillGrants(t *testing.T) {
	mock := &mockProvider{content: `{"decision":"ALLOW","reason":"corroborated by the thread","risk":3}`}
	g := gatekeeper.New(mock, "test-model", newTestLogger())

	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL3, goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Errorf("decision = %s, want ALLOW to stand at L3", resp.Decision)
	}
}

// A tier can only tighten. A judge that denies is never talked out of it.
//
// L1 is excluded because its fast path never reaches the judge at all — that is
// the tier's whole purpose, and it is covered by TestTier_ShortIntentIsFineAtL1.
func TestTier_DenyIsNeverRelaxed(t *testing.T) {
	for _, level := range []keeper.SecurityLevel{
		keeper.SecurityLevelL2, keeper.SecurityLevelL3, keeper.SecurityLevelL4,
	} {
		mock := &mockProvider{content: `{"decision":"DENY","reason":"no justification","risk":7}`}
		g := gatekeeper.New(mock, "test-model", newTestLogger())
		resp, err := g.Evaluate(context.Background(), tierReq(level, goodIntent))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", level, err)
		}
		if resp.Decision != string(keeper.DecisionDeny) {
			t.Errorf("%s turned a DENY into %s", level, resp.Decision)
		}
	}
}

// An intent too short to be a justification is refused before the model call:
// cheaper, and the message can say what to add instead of leaving the judge to
// guess. The bar scales with the tier.
func TestTier_ShortIntentIsRefusedBeforeTheModelCall(t *testing.T) {
	for _, tc := range []struct {
		level  keeper.SecurityLevel
		intent string
	}{
		{keeper.SecurityLevelL3, "need db access"},
		{keeper.SecurityLevelL4, "prod access"},
	} {
		t.Run(tc.level.String(), func(t *testing.T) {
			mock := &mockProvider{content: `{"decision":"ALLOW","reason":"sure","risk":1}`}
			g := gatekeeper.New(mock, "test-model", newTestLogger())

			resp, err := g.Evaluate(context.Background(), tierReq(tc.level, tc.intent))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Decision != string(keeper.DecisionDeny) {
				t.Errorf("decision = %s, want DENY for an intent below the tier minimum", resp.Decision)
			}
			if mock.capturedPrompt != "" {
				t.Error("the model was called for a request the tier could refuse on its own")
			}
			// Actionable, not just "denied": the agent has to know what would work.
			low := strings.ToLower(resp.Reason)
			if !strings.Contains(low, "intent") {
				t.Errorf("reason %q does not tell the agent what to fix", resp.Reason)
			}
		})
	}
}

// The same short intent is fine at a low tier — the gate is the tier's, not a
// blanket rule.
func TestTier_ShortIntentIsFineAtL1(t *testing.T) {
	g := gatekeeper.New(nil, "", newTestLogger())
	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL1, "publish the npm package"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Errorf("decision = %s, want the L1 fast path to still apply", resp.Decision)
	}
}

// The judge has to be told what tier it is looking at and what that tier means,
// or the tier is just a number in a prompt again.
func TestTier_PromptCarriesTheTierAndItsChecks(t *testing.T) {
	mock := &mockProvider{content: `{"decision":"DENY","reason":"no","risk":5}`}
	g := gatekeeper.New(mock, "test-model", newTestLogger())

	if _, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL4, goodIntent)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt := mock.capturedPrompt
	if prompt == "" {
		t.Fatal("no prompt captured")
	}
	p := keeper.SecurityLevelL4.Tier()
	if !strings.Contains(prompt, p.Label) {
		t.Errorf("prompt does not name the tier %q", p.Label)
	}
	// "critical" alone does not tell the judge what is at stake.
	if !strings.Contains(prompt, p.Blast) {
		t.Errorf("prompt does not describe the blast radius")
	}
	for _, check := range p.Checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt is missing the tier check %q", check)
		}
	}
	// And it must say the model cannot grant this on its own, so the model does
	// not waste its answer on an ALLOW that will be overridden.
	if !strings.Contains(strings.ToLower(prompt), "cannot be granted") {
		t.Error("prompt does not tell the judge that L4 needs a human")
	}
}

// A lower tier must not inherit the higher tier's questions — otherwise every
// npm token read gets interrogated about production blast radius and the judge
// learns to ignore the block.
func TestTier_LowTierPromptStaysShort(t *testing.T) {
	mock := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":2}`}
	g := gatekeeper.New(mock, "test-model", newTestLogger())

	if _, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevelL2, goodIntent)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, check := range keeper.SecurityLevelL4.Tier().Checks {
		if strings.Contains(mock.capturedPrompt, check) {
			t.Errorf("an L2 prompt carries the L4 check %q", check)
		}
	}
}

// A corrupt or future level is a level whose blast radius we do not know, and the
// safe reading is the strictest tier. The opposite would make a garbage value the
// cheapest bypass in the system.
func TestTier_UnknownLevelIsTreatedAsCritical(t *testing.T) {
	mock := &mockProvider{content: `{"decision":"ALLOW","reason":"fine","risk":1}`}
	g := gatekeeper.New(mock, "test-model", newTestLogger())

	resp, err := g.Evaluate(context.Background(), tierReq(keeper.SecurityLevel(9), goodIntent))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionEscalate) {
		t.Errorf("decision = %s, want ESCALATE for an unknown level", resp.Decision)
	}
}

// /execute is a different question from /access — the command runs WITH the
// credential — so the tier floor has to apply there too.
func TestTier_ExecuteAtL4AlsoEscalates(t *testing.T) {
	mock := &mockProvider{content: `{"decision":"ALLOW","reason":"harmless","risk":2}`}
	g := gatekeeper.New(mock, "test-model", newTestLogger())

	req := tierReq(keeper.SecurityLevelL4, goodIntent)
	req.Command = "psql -c 'select 1'"
	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionEscalate) {
		t.Errorf("decision = %s, want ESCALATE for an L4 execute", resp.Decision)
	}
}
