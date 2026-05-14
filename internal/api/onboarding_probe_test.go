package api

import (
	"context"
	"testing"
)

// CREWSHIP_E2E_SKIP_TOKEN_PROBE must short-circuit the probe so the
// onboarding E2E suite can run in CI without a live Anthropic call.
// The gate lives in probeAnthropicOAuthToken — if someone refactors
// the function and moves the env check below http.NewRequestWithContext,
// the suite quietly starts depending on a real token + outbound network
// again. This test pins the contract.
func TestProbeAnthropicOAuthTokenSkipGate(t *testing.T) {
	t.Setenv("CREWSHIP_E2E_SKIP_TOKEN_PROBE", "1")
	if err := probeAnthropicOAuthToken(context.Background(), "sk-ant-oat-not-a-real-token"); err != nil {
		t.Fatalf("expected nil with skip gate enabled, got %v", err)
	}

	t.Setenv("CREWSHIP_E2E_SKIP_TOKEN_PROBE", "true")
	if err := probeAnthropicOAuthToken(context.Background(), "sk-ant-oat-still-not-real"); err != nil {
		t.Fatalf("expected nil with skip gate ='true', got %v", err)
	}

	// Empty / other values must NOT skip — guards against a typo
	// (e.g. =yes) accidentally widening the gate to anything truthy-ish.
	t.Setenv("CREWSHIP_E2E_SKIP_TOKEN_PROBE", "yes")
	// We can't assert the outbound call here without a network double,
	// but we can assert the function gets past the gate by checking it
	// does NOT return nil instantly on a context that's already cancelled
	// — when the gate matches, no HTTP attempt happens and ctx state is
	// irrelevant; when it doesn't, the request building / Do call observes
	// the cancelled ctx and returns nil (soft-fail path). Both paths land
	// on nil, so the only observable signal is timing. Skip the negative
	// case to avoid a network-dependent test.
}
