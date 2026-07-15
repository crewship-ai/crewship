package chatbridge

import "testing"

// TestResultUsageForLedger_HappyPath pins #1205: a completed run whose
// adapter reported real token usage in its "result" event must produce a
// RunCostUsage the caller can forward to paymaster's cost-ledger endpoint.
// This is the CLI-stdout fallback path — see cost.go for why it exists
// (the sidecar's HTTP-level cost observation can't see OAuth CONNECT-tunneled
// traffic at all, and never parses usage out of streaming/SSE response
// bodies, which is what every CLI coding adapter uses in practice).
func TestResultUsageForLedger_HappyPath(t *testing.T) {
	meta := map[string]any{
		"total_cost_usd": 0.0042,
		"usage": map[string]any{
			"input_tokens":  float64(1000),
			"output_tokens": float64(200),
		},
	}
	usage, ok := ResultUsageForLedger("ws-1", "crew-1", "agent-1", "claude-haiku-4-5", meta)
	if !ok {
		t.Fatalf("ResultUsageForLedger() ok = false, want true")
	}
	if usage.WorkspaceID != "ws-1" || usage.CrewID != "crew-1" || usage.AgentID != "agent-1" {
		t.Errorf("scope wrong: %+v", usage)
	}
	if usage.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", usage.Provider)
	}
	if usage.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", usage.Model)
	}
	if usage.InputTokens != 1000 || usage.OutputTokens != 200 {
		t.Errorf("tokens = (%d,%d), want (1000,200)", usage.InputTokens, usage.OutputTokens)
	}
}

// TestResultUsageForLedger_NoUsage covers a run whose result event carried no
// usage block (e.g. adapter never surfaced one) — must not fabricate a row.
func TestResultUsageForLedger_NoUsage(t *testing.T) {
	if _, ok := ResultUsageForLedger("ws-1", "crew-1", "agent-1", "claude-haiku-4-5", nil); ok {
		t.Errorf("ResultUsageForLedger() ok = true with nil meta, want false")
	}
	meta := map[string]any{"total_cost_usd": 0.0}
	if _, ok := ResultUsageForLedger("ws-1", "crew-1", "agent-1", "claude-haiku-4-5", meta); ok {
		t.Errorf("ResultUsageForLedger() ok = true with zero-token meta, want false")
	}
}

// TestResultUsageForLedger_UnknownProvider covers a model name the provider
// table doesn't recognize — must not write a row with an empty/garbage
// provider (paymaster.Record requires provider+model).
func TestResultUsageForLedger_UnknownProvider(t *testing.T) {
	meta := map[string]any{
		"usage": map[string]any{
			"input_tokens":  float64(10),
			"output_tokens": float64(5),
		},
	}
	if _, ok := ResultUsageForLedger("ws-1", "crew-1", "agent-1", "some-mystery-model", meta); ok {
		t.Errorf("ResultUsageForLedger() ok = true for unrecognized model, want false")
	}
}
