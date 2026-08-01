package gatekeeper_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/evidence"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

func boundFacts(bound bool) *evidence.Facts {
	return &evidence.Facts{
		Binding:      &evidence.Binding{Bound: bound, EnvVarName: "PROD_DB", BoundAt: "2026-06-14T09:00:00Z"},
		RecentDenies: &evidence.RecentDenies{Count: 2, Days: 7},
		OpenWork:     &evidence.OpenWork{},
	}
}

func l3Request(f *evidence.Facts) gatekeeper.EvalRequest {
	return gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agt_riley",
			Intent:            "Investigate the slow checkout queries by examining query plans on the orders table in production",
		},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "PROD_DB_ADMIN",
		AgentName:      "riley",
		CrewName:       "ops",
		ConvHistory:    "user: the checkout page is slow\nassistant: the logs show slow queries on orders",
		Evidence:       f,
		HardGate:       true,
	}
}

// The measurement this whole change exists for: the same prose that a
// prose-only prompt ALLOWed 3/3 was DENYed 3/3 once the computed facts were
// prepended. That only holds if the facts actually reach the prompt — and above
// the untrusted conversation, for the same reason the watch policy and the tier
// block sit there. A block whose authority the agent's own text can precede,
// restate or contradict has no authority.
func TestEvaluate_EvidenceReachesThePromptAboveTheConversation(t *testing.T) {
	p := &mockProvider{content: `{"decision":"DENY","reason":"unbound","risk":9}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	if _, err := g.Evaluate(context.Background(), l3Request(boundFacts(true))); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	prompt := p.capturedPrompt
	if !strings.Contains(prompt, "credential_bound_to_agent") {
		t.Fatalf("the evidence block never reached the prompt:\n%s", prompt)
	}
	evidenceAt := strings.Index(prompt, "credential_bound_to_agent")
	convAt := strings.Index(prompt, "CONVERSATION HISTORY")
	if convAt >= 0 && evidenceAt > convAt {
		t.Error("evidence renders BELOW the untrusted conversation — agent-authored text can then precede and reframe it")
	}
}

// A judge asked to rule on a credential the agent is not bound to is being asked
// the wrong question. The binding is not a hint to weigh, it is the operator's
// standing answer, and at the tiers that reach real infrastructure the model
// gets no vote: L3 is "administrative access to real infrastructure (SSH,
// database admin, cloud account)". Refusing before the call is cheaper, is not
// subject to model variance, and gives a reason a human can act on.
func TestEvaluate_UnboundCredentialIsRefusedWithoutAModelCall(t *testing.T) {
	for _, lvl := range []keeper.SecurityLevel{keeper.SecurityLevelL3, keeper.SecurityLevelL4} {
		p := &mockProvider{content: `{"decision":"ALLOW","reason":"sounds fine","risk":2}`}
		g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

		req := l3Request(boundFacts(false))
		req.SecurityLevel = lvl

		resp, err := g.Evaluate(context.Background(), req)
		if err != nil {
			t.Fatalf("Evaluate(%v): %v", lvl, err)
		}
		if resp.Decision != string(keeper.DecisionDeny) {
			t.Errorf("%v: decision = %q, want DENY — the agent holds no binding to this credential", lvl, resp.Decision)
		}
		if p.capturedPrompt != "" {
			t.Errorf("%v: the model was asked anyway; the gate must short-circuit", lvl)
		}
		if !strings.Contains(strings.ToLower(resp.Reason), "bound") {
			t.Errorf("%v: reason = %q, want it to name the missing binding", lvl, resp.Reason)
		}
	}
}

// L1/L2 are self-service tiers whose credentials are handed to the agent for the
// whole run. A missing binding there is a signal for the judge to weigh, not a
// refusal: the tier vocabulary says a tier may only TIGHTEN a verdict, and
// inventing a hard refusal at the bottom two tiers would be this file deciding
// policy that internal/keeper/tier.go owns.
func TestEvaluate_UnboundLowTierStillReachesTheJudge(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"routine","risk":2}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	req := l3Request(boundFacts(false))
	req.SecurityLevel = keeper.SecurityLevelL2
	req.CredentialName = "npm-read-token"

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if p.capturedPrompt == "" {
		t.Fatal("L2 was hard-gated; the judge must still decide at a self-service tier")
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Errorf("decision = %q, want the judge's ALLOW to stand", resp.Decision)
	}
}

// Evidence is optional everywhere. An instance with the toggle off, or one whose
// queries all failed, must produce exactly the prompt it produced before this
// change — never a hard gate derived from an absence.
func TestEvaluate_NoEvidenceChangesNothing(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":3}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	req := l3Request(nil)
	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if p.capturedPrompt == "" {
		t.Fatal("no evidence must not hard-gate — absence is not a verified negative")
	}
	if strings.Contains(p.capturedPrompt, "VERIFIED FACTS") {
		t.Error("an evidence header was rendered with no facts behind it")
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Errorf("decision = %q, want ALLOW", resp.Decision)
	}
}

// A binding whose query FAILED is nil, not false. Gather's whole discipline is
// omission over guessing, and the gate must honour it: refusing on a fact that
// was never established would turn a database blip into a blanket denial of
// every L3/L4 request — the #1624 shape with a new cause.
func TestEvaluate_UnestablishedBindingDoesNotGate(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":3}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	req := l3Request(&evidence.Facts{
		Binding:      nil, // the query failed
		RecentDenies: &evidence.RecentDenies{Count: 0, Days: 7},
	})

	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if p.capturedPrompt == "" {
		t.Error("gated on a binding that was never established — an outage must not deny every L3 request")
	}
}

// P8 end to end: with the escalation floor at L3, a judge ALLOW on an L3
// credential must come back as ESCALATE — a person confirms it.
//
// The check that matters is the VERDICT, not that a field was carried. The
// review on this branch found nine capabilities plumbed through config, CLI and
// docs with no consumer on the decision path; a test asserting the floor was
// stored would have passed against exactly that bug.
func TestEvaluate_EscalateFromPutsAHumanOnL3(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"cert rotation is corroborated","risk":4}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agt_riley",
			Intent:            "Rotate the expiring TLS certificates on staging-web-01 and reload nginx",
		},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "KT_SSH_STAGING",
		AgentName:      "riley",
		CrewName:       "ops",
	}

	// Without the floor the model's ALLOW stands.
	base, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if base.Decision != string(keeper.DecisionAllow) {
		t.Fatalf("baseline decision = %q, want ALLOW — the premise of this test", base.Decision)
	}

	// With it, the same ALLOW becomes an escalation.
	req.EscalateFrom = keeper.SecurityLevelL3
	got, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Decision != string(keeper.DecisionEscalate) {
		t.Errorf("decision = %q, want ESCALATE — the operator asked for a human on L3 and the model granted it alone", got.Decision)
	}
}

// The floor must not reach down. An operator putting a human on L3 has not asked
// for one on every npm read token, and L1's no-model fast path must survive.
func TestEvaluate_EscalateFromLeavesL1Alone(t *testing.T) {
	p := &mockProvider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	g := gatekeeper.New(p, "qwen3.5:9b", newTestLogger())

	got, err := g.Evaluate(context.Background(), gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agt_riley",
			Intent:            "publish the release tarball to npm",
		},
		SecurityLevel:  keeper.SecurityLevelL1,
		CredentialName: "npm-token",
		AgentName:      "riley",
		EscalateFrom:   keeper.SecurityLevelL3,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Decision != string(keeper.DecisionAllow) {
		t.Errorf("L1 decision = %q, want ALLOW — an L3 floor must not reach down", got.Decision)
	}
}
