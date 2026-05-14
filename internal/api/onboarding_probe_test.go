package api

import (
	"context"
	"testing"
)

// CREWSHIP_E2E_SKIP_TOKEN_PROBE must short-circuit the probe so the
// onboarding E2E suite can run in CI without a live Anthropic call.
// If someone refactors probeAnthropicOAuthToken and moves the env
// check below http.NewRequestWithContext, the suite quietly starts
// depending on a real token + outbound network again. This pins the
// contract.
func TestProbeAnthropicOAuthTokenSkipGate(t *testing.T) {
	for _, v := range []string{"1", "true"} {
		t.Run("gate="+v, func(t *testing.T) {
			t.Setenv("CREWSHIP_E2E_SKIP_TOKEN_PROBE", v)
			if err := probeAnthropicOAuthToken(context.Background(), "sk-ant-oat-not-a-real-token"); err != nil {
				t.Fatalf("expected nil with skip gate enabled, got %v", err)
			}
		})
	}
}
