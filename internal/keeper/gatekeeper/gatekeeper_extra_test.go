package gatekeeper_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
)

// modelCapturingProvider records the request fields the Gatekeeper passes
// down to the LLM provider. Tests use it to assert that the prompt and the
// configured model name are wired through correctly without standing up a
// real Ollama. Implements llm.Provider; mirrors the mockProvider pattern
// already used in gatekeeper_test.go / security_test.go.
type modelCapturingProvider struct {
	content     string
	err         error
	capturedReq llm.Request
	captured    bool
}

func (m *modelCapturingProvider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	m.capturedReq = req
	m.captured = true
	if m.err != nil {
		return nil, m.err
	}
	return &llm.Response{Content: m.content}, nil
}

func (m *modelCapturingProvider) Stream(ctx context.Context, req llm.Request, handler func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	_ = handler(llm.StreamEvent{Type: "text", Content: resp.Content})
	_ = handler(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}

func (m *modelCapturingProvider) Name() string { return "modelCapturingMock" }

// TestGatekeeper_L1AutoAllow_RejectsLowDistinctCharFiller verifies the M3 audit
// guard: a 10-char intent of "aaaaaaaaaa" satisfies len>=10 but only has one
// distinct non-whitespace rune. Without the distinct-char check this would
// be a free-pass for any L1 credential. Invariant: meaningful intent text
// (>=3 distinct non-whitespace chars) is required for the L1 fast path.
func TestGatekeeper_L1AutoAllow_RejectsLowDistinctCharFiller(t *testing.T) {
	g := gatekeeper.New(nil, "", newTestLogger())

	cases := []struct {
		name   string
		intent string
	}{
		{"all_same_char_10", "aaaaaaaaaa"},         // 10 chars, 1 distinct
		{"two_distinct_padded", "ababababab"},      // 10 chars, 2 distinct — still <3
		{"whitespace_separated_same", "a a a a a"}, // distinct count ignores whitespace → 1 distinct
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := gatekeeper.EvalRequest{
				Request: keeper.Request{
					RequestingAgentID: "agent1",
					Intent:            tc.intent,
				},
				SecurityLevel:  keeper.SecurityLevelL1,
				CredentialName: "npm-token",
				AgentName:      "DevBot",
				CrewName:       "Dev Crew",
			}

			resp, err := g.Evaluate(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// With no LLM configured, the only safe path when the auto-allow
			// shortcut does NOT trigger is DENY. ALLOW would prove the
			// shortcut leaked.
			if resp.Decision == string(keeper.DecisionAllow) {
				t.Fatalf("M3 REGRESSION: filler intent %q auto-allowed at L1 (reason: %s)",
					tc.intent, resp.Reason)
			}
			if resp.Decision != string(keeper.DecisionDeny) {
				t.Errorf("expected DENY (no LLM + no shortcut), got %s", resp.Decision)
			}
		})
	}
}

// TestGatekeeper_L1AutoAllow_TrimsWhitespaceBeforeLengthCheck verifies that
// padding the intent with whitespace cannot satisfy the >=10 char threshold —
// the gatekeeper TrimSpace's the intent before measuring. Invariant: an
// adversary cannot pad a trivial intent with spaces to get the L1 fast path.
func TestGatekeeper_L1AutoAllow_TrimsWhitespaceBeforeLengthCheck(t *testing.T) {
	g := gatekeeper.New(nil, "", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			// 30 raw chars, but trimmed → "hi" (2 chars) → must NOT auto-allow.
			Intent: "              hi              ",
		},
		SecurityLevel:  keeper.SecurityLevelL1,
		CredentialName: "npm-token",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision == string(keeper.DecisionAllow) {
		t.Fatalf("whitespace-padded short intent auto-allowed (reason: %s)", resp.Reason)
	}
}

// TestGatekeeper_L4_NeverAutoAllowsWithoutLLM verifies that L4 (the highest
// security tier — production admin, payment) never takes the L1 fast path
// no matter how descriptive the intent is. With no LLM configured the only
// safe outcome is DENY. Invariant: the auto-allow shortcut is L1-only.
func TestGatekeeper_L4_NeverAutoAllowsWithoutLLM(t *testing.T) {
	g := gatekeeper.New(nil, "", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			// Long, meaningful, many distinct chars — would auto-allow at L1.
			Intent: "I need to rotate the production payment gateway admin credential",
		},
		SecurityLevel:  keeper.SecurityLevelL4,
		CredentialName: "prod-payment-admin",
		AgentName:      "RotatorBot",
		CrewName:       "Payments",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision == string(keeper.DecisionAllow) {
		t.Fatalf("L4 credential auto-allowed without LLM (reason: %s)", resp.Reason)
	}
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Errorf("expected DENY (no LLM at L4), got %s", resp.Decision)
	}
	if resp.RiskScore < 5 {
		t.Errorf("expected high risk score on L4 deny, got %d", resp.RiskScore)
	}
}

// TestGatekeeper_L4_RoutesToLLM verifies that an L4 request with an LLM
// configured does call the LLM and respect its decision (here ESCALATE).
// Invariant: L4 must always be evaluated, never short-circuited.
func TestGatekeeper_L4_RoutesToLLM(t *testing.T) {
	p := &modelCapturingProvider{
		content: `{"decision":"ESCALATE","reason":"L4 needs human approval","risk":8}`,
	}
	g := gatekeeper.New(p, "phi3:mini", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "Rotate production payment gateway admin credential",
		},
		SecurityLevel:  keeper.SecurityLevelL4,
		CredentialName: "prod-payment-admin",
		AgentName:      "RotatorBot",
		CrewName:       "Payments",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.captured {
		t.Fatal("LLM was not called for L4 request — auto-allow leaked?")
	}
	if resp.Decision != string(keeper.DecisionEscalate) {
		t.Errorf("expected ESCALATE, got %s (reason: %s)", resp.Decision, resp.Reason)
	}
}

// TestGatekeeper_LLMDeadlineExceeded_DenyWithReason verifies that when the
// upstream provider returns context.DeadlineExceeded (matching the audit-M4
// 5s upstream timeout behaviour), the gatekeeper denies AND the reason
// text surfaces the unavailability so audit logs are actionable.
// Invariant: timeouts must fail closed, never fail open.
func TestGatekeeper_LLMDeadlineExceeded_DenyWithReason(t *testing.T) {
	p := &modelCapturingProvider{err: context.DeadlineExceeded}
	g := gatekeeper.New(p, "phi3:mini", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "Deploy to staging via SSH key",
		},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "prod-ssh-key",
		AgentName:      "DeployBot",
		CrewName:       "Infra",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		// Evaluate must not surface the error; deny-by-default is the contract.
		t.Fatalf("unexpected error (should fail closed in-band): %v", err)
	}
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Fatalf("expected DENY on deadline exceeded, got %s", resp.Decision)
	}
	if resp.RiskScore != 10 {
		t.Errorf("expected risk score 10 on hard deny, got %d", resp.RiskScore)
	}
	if !strings.Contains(strings.ToLower(resp.Reason), "unavailable") {
		t.Errorf("expected reason to cite unavailability, got %q", resp.Reason)
	}
	if !strings.Contains(resp.Reason, context.DeadlineExceeded.Error()) {
		t.Errorf("expected reason to include underlying error %q, got %q",
			context.DeadlineExceeded.Error(), resp.Reason)
	}
}

// TestGatekeeper_NoProvider_ReasonCitesMisconfiguration verifies the
// deny-by-default reason for the "no LLM provider configured" path so
// operators can diagnose silent denials. Invariant: misconfiguration
// must be visible in the audit reason, not just an opaque DENY.
func TestGatekeeper_NoProvider_ReasonCitesMisconfiguration(t *testing.T) {
	g := gatekeeper.New(nil, "", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "I need the DB credentials to run a migration",
		},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "db-admin",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Fatalf("expected DENY, got %s", resp.Decision)
	}
	if !strings.Contains(resp.Reason, "not configured") {
		t.Errorf("expected reason to cite 'not configured', got %q", resp.Reason)
	}
	if resp.RiskScore != 10 {
		t.Errorf("expected risk score 10 on misconfig deny, got %d", resp.RiskScore)
	}
}

// TestGatekeeper_ConvHistory_DelimiterMatchedAtBeginAndEnd verifies the
// random delimiter wrapping the conversation history block uses the SAME
// hex token on both the begin and end markers. A predictable or mismatched
// delimiter would let injected text break out of the BACKGROUND block and
// be interpreted as top-level Keeper instructions.
// Invariant: prompt-injection-via-history is mitigated by a single
// per-request random nonce framing the history.
func TestGatekeeper_ConvHistory_DelimiterMatchedAtBeginAndEnd(t *testing.T) {
	p := &modelCapturingProvider{
		content: `{"decision":"DENY","reason":"precautionary","risk":8}`,
	}
	g := gatekeeper.New(p, "phi3:mini", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "Deploy to staging using SSH key",
		},
		SecurityLevel:  keeper.SecurityLevelL2,
		CredentialName: "staging-ssh",
		ConvHistory:    "User: deploy please\nAgent: starting",
	}

	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := p.capturedReq.Messages[0].Content
	if prompt == "" {
		t.Fatal("LLM not called or prompt empty")
	}

	// Match the begin and end markers; the gatekeeper format is
	// "--- <hex> begin ---" and "--- <hex> end ---".
	beginRE := regexp.MustCompile(`--- ([0-9a-f]+) begin ---`)
	endRE := regexp.MustCompile(`--- ([0-9a-f]+) end ---`)
	beginMatch := beginRE.FindStringSubmatch(prompt)
	endMatch := endRE.FindStringSubmatch(prompt)
	if beginMatch == nil {
		t.Fatalf("begin delimiter not found in prompt; prompt:\n%s", prompt)
	}
	if endMatch == nil {
		t.Fatalf("end delimiter not found in prompt; prompt:\n%s", prompt)
	}
	if beginMatch[1] != endMatch[1] {
		t.Errorf("begin delimiter %q != end delimiter %q — history block not properly wrapped",
			beginMatch[1], endMatch[1])
	}
	if len(beginMatch[1]) < 8 {
		t.Errorf("delimiter %q is shorter than 8 hex chars — entropy too low to resist prediction",
			beginMatch[1])
	}
	// The history payload must lie BETWEEN the begin and end markers.
	beginIdx := strings.Index(prompt, beginMatch[0])
	endIdx := strings.Index(prompt, endMatch[0])
	histIdx := strings.Index(prompt, "User: deploy please")
	if !(beginIdx < histIdx && histIdx < endIdx) {
		t.Errorf("history payload not enclosed by delimiters (begin=%d, hist=%d, end=%d)",
			beginIdx, histIdx, endIdx)
	}
}

// TestGatekeeper_NoConvHistory_NoBackgroundBlockInPrompt verifies that when
// the request carries no conversation history, the gatekeeper does NOT emit
// an empty [BACKGROUND - CONVERSATION HISTORY] section or stray delimiters.
// Otherwise the LLM might think it received empty context and act oddly.
// Invariant: history framing is conditional on having content to frame.
func TestGatekeeper_NoConvHistory_NoBackgroundBlockInPrompt(t *testing.T) {
	p := &modelCapturingProvider{
		content: `{"decision":"DENY","reason":"no history","risk":5}`,
	}
	g := gatekeeper.New(p, "phi3:mini", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "Deploy to staging using SSH key",
		},
		SecurityLevel:  keeper.SecurityLevelL2,
		CredentialName: "staging-ssh",
		// ConvHistory deliberately empty.
	}

	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := p.capturedReq.Messages[0].Content
	if strings.Contains(prompt, "CONVERSATION HISTORY") {
		t.Errorf("expected no conversation-history block when ConvHistory is empty; prompt:\n%s",
			prompt)
	}
	if strings.Contains(prompt, " begin ---") || strings.Contains(prompt, " end ---") {
		t.Errorf("expected no history delimiters when ConvHistory is empty; prompt:\n%s",
			prompt)
	}
	// CURRENT REQUEST block must still be present.
	if !strings.Contains(prompt, "CURRENT REQUEST TO EVALUATE") {
		t.Errorf("expected CURRENT REQUEST block in prompt regardless of history")
	}
}

// TestGatekeeper_DefaultsModelWhenEmpty verifies New("") falls back to
// "phi3:mini" so the upstream call doesn't ship an empty model name.
// Invariant: a misconfigured caller must not produce a request the
// provider will reject for an empty Model field.
// TestGatekeeper_PassesExplicitModelToProvider replaces the deleted
// TestGatekeeper_DefaultsModelWhenEmpty. PR-Z Z.2 removed the silent
// phi3:mini fallback in gatekeeper.New; the model string is now passed
// through verbatim to the provider, and validation that it's non-empty
// is enforced at config load (see internal/config TestValidationKeeper*).
func TestGatekeeper_PassesExplicitModelToProvider(t *testing.T) {
	p := &modelCapturingProvider{
		content: `{"decision":"DENY","reason":"ok","risk":5}`,
	}
	const explicitModel = "claude-haiku-4-5"
	g := gatekeeper.New(p, explicitModel, newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "deploy staging please",
		},
		SecurityLevel:  keeper.SecurityLevelL2,
		CredentialName: "staging-key",
	}

	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.capturedReq.Model != explicitModel {
		t.Errorf("expected model %q passed through to provider, got %q", explicitModel, p.capturedReq.Model)
	}
}

// TestGatekeeper_NilLogger_DoesNotPanic verifies that New tolerates a nil
// logger and falls back to slog.Default rather than panicking on the
// first Info call. Invariant: defensive init must not crash callers.
func TestGatekeeper_NilLogger_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("New(nil-logger) panicked on Evaluate: %v", r)
		}
	}()

	g := gatekeeper.New(nil, "", nil)
	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "I need to deploy the staging build",
		},
		SecurityLevel:  keeper.SecurityLevelL1,
		CredentialName: "staging-deploy",
	}
	if _, err := g.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGatekeeper_LLMEscalateDecision_Preserved verifies the ESCALATE decision
// from the LLM is preserved verbatim (only the case-fold and unknown-value
// branches normalise). Invariant: legitimate escalations must not be
// silently downgraded to DENY (which would block work) or ALLOW (worse).
func TestGatekeeper_LLMEscalateDecision_Preserved(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"upper", `{"decision":"ESCALATE","reason":"L3 unclear","risk":6}`},
		{"mixed", `{"decision":"Escalate","reason":"L3 unclear","risk":6}`},
		{"lower", `{"decision":"escalate","reason":"L3 unclear","risk":6}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &modelCapturingProvider{content: tc.body}
			g := gatekeeper.New(p, "phi3:mini", newTestLogger())

			req := gatekeeper.EvalRequest{
				Request: keeper.Request{
					RequestingAgentID: "agent1",
					Intent:            "need staging key for the pipeline",
				},
				SecurityLevel:  keeper.SecurityLevelL3,
				CredentialName: "staging-key",
			}
			resp, err := g.Evaluate(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Decision != string(keeper.DecisionEscalate) {
				t.Errorf("expected ESCALATE preserved (case %q), got %s",
					tc.name, resp.Decision)
			}
		})
	}
}

// TestGatekeeper_LLMUnavailable_ReasonEmbedsError verifies that when the
// provider returns a non-deadline error (e.g. connection refused), the
// gatekeeper denies AND the underlying error message is embedded in the
// reason. Invariant: audit log must retain enough context to diagnose
// LLM outages without re-running the request.
func TestGatekeeper_LLMUnavailable_ReasonEmbedsError(t *testing.T) {
	upstreamErr := errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
	p := &modelCapturingProvider{err: upstreamErr}
	g := gatekeeper.New(p, "phi3:mini", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "need the AWS prod key for cluster diagnostics",
		},
		SecurityLevel:  keeper.SecurityLevelL3,
		CredentialName: "aws-prod-key",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Fatalf("expected DENY on upstream error, got %s", resp.Decision)
	}
	if !strings.Contains(resp.Reason, "connection refused") {
		t.Errorf("expected reason to embed underlying error 'connection refused', got %q",
			resp.Reason)
	}
}

// TestGatekeeper_RiskScoreExactlyAtBounds_NotClamped verifies the clamp
// is an inclusive [1,10] window — values exactly at 1 or 10 pass through
// untouched. Guards against an off-by-one that would clamp 10→9 or 1→2
// and produce confusing audit records.
func TestGatekeeper_RiskScoreExactlyAtBounds_NotClamped(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantScore int
	}{
		{"min_boundary", `{"decision":"ALLOW","reason":"ok","risk":1}`, 1},
		{"max_boundary", `{"decision":"DENY","reason":"high","risk":10}`, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &modelCapturingProvider{content: tc.body}
			g := gatekeeper.New(p, "phi3:mini", newTestLogger())
			req := gatekeeper.EvalRequest{
				Request: keeper.Request{
					RequestingAgentID: "agent1",
					Intent:            "need staging key for the pipeline",
				},
				SecurityLevel:  keeper.SecurityLevelL2,
				CredentialName: "staging-key",
			}
			resp, err := g.Evaluate(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.RiskScore != tc.wantScore {
				t.Errorf("expected boundary risk %d preserved, got %d",
					tc.wantScore, resp.RiskScore)
			}
		})
	}
}

// TestGatekeeper_AuditFieldsTruncated_OnExtremelyLongResponse verifies that
// the Prompt and RawLLMResponse audit fields are truncated when stored on
// the response, preventing a malicious LLM from bloating the audit log via
// a multi-MB reply. Invariant: audit storage is bounded.
func TestGatekeeper_AuditFieldsTruncated_OnExtremelyLongResponse(t *testing.T) {
	// Build a JSON response with a giant 'reason' payload around it.
	// The wrapper JSON is valid; parseResponse will extract it, then
	// truncateForAudit caps the stored copy.
	huge := strings.Repeat("X", 10_000)
	body := `prefix garbage ` + huge + ` {"decision":"DENY","reason":"too risky","risk":7} suffix ` + huge
	p := &modelCapturingProvider{content: body}
	g := gatekeeper.New(p, "phi3:mini", newTestLogger())

	req := gatekeeper.EvalRequest{
		Request: keeper.Request{
			RequestingAgentID: "agent1",
			Intent:            "need staging key for the pipeline",
		},
		SecurityLevel:  keeper.SecurityLevelL2,
		CredentialName: "staging-key",
	}

	resp, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Decision still parsed correctly out of the surrounding noise.
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Errorf("expected DENY parsed out of noisy response, got %s", resp.Decision)
	}
	// RawLLMResponse must be capped.
	if len(resp.RawLLMResponse) > 2100 {
		t.Errorf("RawLLMResponse not truncated: len=%d (want <= ~2000+marker)",
			len(resp.RawLLMResponse))
	}
	if len(resp.RawLLMResponse) > 0 && !strings.HasSuffix(resp.RawLLMResponse, "(truncated)") {
		// Only assert marker if truncation actually happened (it should).
		t.Errorf("expected '(truncated)' marker on capped RawLLMResponse, got tail %q",
			resp.RawLLMResponse[max(0, len(resp.RawLLMResponse)-20):])
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
