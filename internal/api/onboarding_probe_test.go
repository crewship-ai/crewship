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
func TestProbeAnthropicOAuthTokenSkipGate(t *testing.T) {
	orig := skipTokenProbe
	t.Cleanup(func() { skipTokenProbe = orig })

	skipTokenProbe = true
	if err := probeAnthropicOAuthToken(context.Background(), "sk-ant-oat-not-a-real-token"); err != nil {
		t.Fatalf("expected nil with skip gate enabled, got %v", err)
	}
}
