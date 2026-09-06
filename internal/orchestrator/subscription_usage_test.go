package orchestrator

import "testing"

func TestSubscriptionUsageComesFromActualParserAndGrant(t *testing.T) {
	cases := []struct {
		adapter, line string
		req           AgentRunRequest
	}{
		{"CODEX_CLI", `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20,"cached_input_tokens":30}}`, codexLoginReq(t)},
		{"GEMINI_CLI", `{"type":"result","status":"success","stats":{"input_tokens":100,"output_tokens":20}}`, geminiLoginReq(t, "")},
		{"CLAUDE_CODE", `{"type":"result","subtype":"success","usage":{"input_tokens":100,"output_tokens":20}}`, AgentRunRequest{Credentials: []Credential{{ID: "claude-login", Provider: "ANTHROPIC", Type: "AI_CLI_TOKEN", PlainValue: "sk-ant-oat01-fixture"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			req := tc.req
			req.CLIAdapter = tc.adapter
			req.WorkspaceID = "workspace"
			var captured []SubscriptionUsage
			getAdapter(tc.adapter).ParseStreamLine([]byte(tc.line), func(event AgentEvent) {
				if meta, ok := event.Metadata.(map[string]any); ok {
					meta["credential_id"] = "forged-by-cli"
				}
				if usage, ok := subscriptionUsageForEvent(req, "run", "observed-model", event); ok {
					captured = append(captured, usage)
				}
			})
			if len(captured) != 1 {
				t.Fatalf("recorded %d usage envelopes", len(captured))
			}
			got := captured[0]
			if got.CredentialID != req.Credentials[0].ID || got.InputTokens != 100 || got.OutputTokens != 20 || got.WorkspaceID != "workspace" || got.RunID != "run" {
				t.Fatalf("incorrect attribution: %+v", got)
			}
			if tc.adapter == "CODEX_CLI" && got.CachedInputTokens != 30 {
				t.Fatal("Codex cached tokens lost")
			}
			if tc.adapter == "CLAUDE_CODE" && got.Plan != "Claude (plan unknown)" {
				t.Fatal("opaque setup-token must not invent a Max subscription")
			}
		})
	}
}

func TestSubscriptionUsageNeverInventsAPIKeyUsage(t *testing.T) {
	req := AgentRunRequest{CLIAdapter: "CODEX_CLI", Credentials: []Credential{{ID: "api", Type: "API_KEY", Provider: "OPENAI", PlainValue: "api-key"}}}
	event := AgentEvent{Type: "result", Metadata: map[string]any{"usage": map[string]any{"input_tokens": float64(10)}}}
	if _, ok := subscriptionUsageForEvent(req, "run", "model", event); ok {
		t.Fatal("API key was recorded as a subscription")
	}
	req = codexLoginReq(t)
	event.Metadata = map[string]any{"is_error": true}
	if _, ok := subscriptionUsageForEvent(req, "run", "model", event); ok {
		t.Fatal("missing usage was fabricated as a zero-token call")
	}
}
