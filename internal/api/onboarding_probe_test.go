package api

import (
	"context"
	"testing"
)

// skipTokenProbe is read once at package init from
// CREWSHIP_E2E_SKIP_TOKEN_PROBE — flipping the package var directly
// mirrors what server startup would have cached for any truthy value
// and avoids leaking the dependence on init-time env state into the
// test. Pins the contract that probeAnthropicOAuthToken returns nil
// when the gate is set, so a refactor that moves the check below
// http.NewRequestWithContext breaks the test instead of silently
// starting to depend on a real Anthropic call.
//
// Single-row table on purpose — the disabled-gate path requires
// mocking the outbound HTTP call (the URL is a const inside the
// probe), which is out of scope for this guardrail. The table
// wrapper is here so adding that case later doesn't restructure
// the test.
func TestProbeAnthropicOAuthTokenSkipGate(t *testing.T) {
	tests := []struct {
		name string
		gate bool
	}{
		{name: "returns nil when skip gate enabled", gate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := skipTokenProbe
			t.Cleanup(func() { skipTokenProbe = orig })

			skipTokenProbe = tt.gate
			if err := probeAnthropicOAuthToken(context.Background(), "sk-ant-oat-not-a-real-token"); err != nil {
				t.Fatalf("expected nil with skip gate enabled, got %v", err)
			}
		})
	}
}
