package gatekeeper_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// watchSentinel is a distinctive string a stub WatchSpecResolver returns so the
// tests can locate the injected watch-policy block in the built prompt.
const watchSentinel = "SENTINEL-WATCH-RULE-flag-ssh-reads"

func stubResolver(spec string) gatekeeper.WatchSpecResolver {
	return func(_ context.Context, _ string) string { return spec }
}

const jsonInstrLine = "Respond with ONLY valid JSON"
const convFence = "[BACKGROUND — CONVERSATION HISTORY]"
const policyLabel = "WORKSPACE WATCH POLICY"

// The access evaluator must inject the resolved watch spec, and it must sit
// ABOVE the untrusted conversation fence and ABOVE the final strict-JSON line —
// so agent-injected history can neither spoof the policy nor displace the
// response contract.
func TestEvaluate_InjectsWatchSpec_AccessPrompt(t *testing.T) {
	mp := &mockProvider{content: `{"decision":"ESCALATE","reason":"x","risk":5}`}
	g := gatekeeper.New(mp, "m", newTestLogger(),
		gatekeeper.WithWatchSpecResolver(stubResolver(watchSentinel)))

	req := gatekeeper.EvalRequest{
		// L3 (not L1) so the auto-allow fast path doesn't short-circuit before
		// the prompt is built.
		Request:        keeper.Request{WorkspaceID: "ws1", Intent: "read prod db creds for the migration"},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "db",
		AgentName:      "a",
		CrewName:       "c",
		ConvHistory:    "agent discussed running the migration",
	}
	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	p := mp.capturedPrompt
	si := strings.Index(p, watchSentinel)
	if si < 0 {
		t.Fatalf("access prompt missing the watch spec:\n%s", p)
	}
	if !strings.Contains(p, policyLabel) {
		t.Fatal("watch block missing the authoritative policy label")
	}
	if fence := strings.Index(p, convFence); fence < 0 || si > fence {
		t.Fatalf("watch block must precede the conversation fence (si=%d fence=%d)", si, fence)
	}
	if jl := strings.Index(p, jsonInstrLine); jl < 0 || si > jl {
		t.Fatalf("watch block must precede the final JSON instruction (si=%d jl=%d)", si, jl)
	}
}

// The behavior evaluator must inject the watch spec too, additive to (not a
// replacement for) the built-in anti-pattern list.
func TestEvaluate_InjectsWatchSpec_BehaviorPrompt(t *testing.T) {
	mp := &mockProvider{content: `{"decision":"ALLOW","reason":"x","risk":1}`}
	g := gatekeeper.New(mp, "m", newTestLogger(),
		gatekeeper.WithWatchSpecResolver(stubResolver(watchSentinel)))

	req := gatekeeper.EvalRequest{
		Request:     keeper.Request{WorkspaceID: "ws1", RequestType: keeper.RequestTypeBehavior},
		RequestType: keeper.RequestTypeBehavior,
		AgentName:   "a",
		CrewName:    "c",
		Behavior:    &gatekeeper.BehaviorInput{ToolName: "bash", BehaviorMode: "warn"},
	}
	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	p := mp.capturedPrompt
	si := strings.Index(p, watchSentinel)
	if si < 0 {
		t.Fatalf("behavior prompt missing the watch spec:\n%s", p)
	}
	if jl := strings.Index(p, jsonInstrLine); jl < 0 || si > jl {
		t.Fatalf("watch block must precede the final JSON instruction (si=%d jl=%d)", si, jl)
	}
	// The built-in anti-pattern list must survive — the watch spec is additive.
	if !strings.Contains(p, "Tight loops") {
		t.Fatal("built-in anti-pattern list was dropped")
	}
}

// When an operator has an active watch policy, an L1 credential request must
// NOT take the auto-allow fast path — it must reach the LLM so the policy is
// applied. Otherwise the most common credential tier silently bypasses the spec.
func TestEvaluate_L1FastPath_SkippedWhenWatchSpecActive(t *testing.T) {
	mp := &mockProvider{content: `{"decision":"ESCALATE","reason":"off-hours","risk":6}`}
	g := gatekeeper.New(mp, "m", newTestLogger(),
		gatekeeper.WithWatchSpecResolver(stubResolver(watchSentinel)))

	req := gatekeeper.EvalRequest{
		Request:        keeper.Request{WorkspaceID: "ws1", Intent: "publish the nightly build"},
		SecurityLevel:  keeper.SecurityLevelL1, // would normally auto-allow
		CredentialName: "npm-token",
		AgentName:      "a",
		CrewName:       "c",
	}
	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// The LLM ran (fast path skipped) and its decision + the injected spec are visible.
	if resp.Decision != string(keeper.DecisionEscalate) {
		t.Fatalf("expected the LLM decision (fast path skipped), got %q", resp.Decision)
	}
	if !strings.Contains(mp.capturedPrompt, watchSentinel) {
		t.Fatal("L1 request with an active watch policy must inject the spec")
	}
}

// With no active watch policy the L1 fast path is preserved — the LLM is never
// called (backward-compatible, no perf regression for the common case).
func TestEvaluate_L1FastPath_PreservedWhenNoWatchSpec(t *testing.T) {
	mp := &mockProvider{content: `{"decision":"DENY","reason":"should not run","risk":9}`}
	g := gatekeeper.New(mp, "m", newTestLogger(),
		gatekeeper.WithWatchSpecResolver(stubResolver(""))) // empty spec

	req := gatekeeper.EvalRequest{
		Request:        keeper.Request{WorkspaceID: "ws1", Intent: "publish the nightly build"},
		SecurityLevel:  keeper.SecurityLevelL1,
		CredentialName: "npm-token",
		AgentName:      "a",
		CrewName:       "c",
	}
	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Fatalf("expected L1 auto-allow, got %q", resp.Decision)
	}
	if mp.capturedPrompt != "" {
		t.Fatal("L1 fast path must not call the LLM when no watch spec is active")
	}
}

// A nil resolver (the default for the ~8 bare New(...) call sites) injects no
// watch block and does not panic — backward compatible.
func TestEvaluate_NilResolver_NoWatchBlock(t *testing.T) {
	mp := &mockProvider{content: `{"decision":"ALLOW","reason":"x","risk":1}`}
	g := gatekeeper.New(mp, "m", newTestLogger()) // no WithWatchSpecResolver

	req := gatekeeper.EvalRequest{
		Request:     keeper.Request{WorkspaceID: "ws1", RequestType: keeper.RequestTypeBehavior},
		RequestType: keeper.RequestTypeBehavior,
		AgentName:   "a",
		CrewName:    "c",
		Behavior:    &gatekeeper.BehaviorInput{ToolName: "bash", BehaviorMode: "warn"},
	}
	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if strings.Contains(mp.capturedPrompt, policyLabel) {
		t.Fatal("nil resolver must not inject a watch policy block")
	}
}

// An empty resolved spec ("" — the unconfigured-workspace case) injects no block.
func TestEvaluate_EmptySpec_NoWatchBlock(t *testing.T) {
	mp := &mockProvider{content: `{"decision":"ALLOW","reason":"x","risk":1}`}
	g := gatekeeper.New(mp, "m", newTestLogger(),
		gatekeeper.WithWatchSpecResolver(stubResolver("")))

	req := gatekeeper.EvalRequest{
		Request:     keeper.Request{WorkspaceID: "ws1", RequestType: keeper.RequestTypeBehavior},
		RequestType: keeper.RequestTypeBehavior,
		AgentName:   "a",
		CrewName:    "c",
		Behavior:    &gatekeeper.BehaviorInput{ToolName: "bash", BehaviorMode: "warn"},
	}
	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if strings.Contains(mp.capturedPrompt, policyLabel) {
		t.Fatal("empty spec must not inject a watch policy block")
	}
}
