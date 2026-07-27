package seeddata

// #1485 — the init/runtime boundary the parity test cannot see.
//
// AnthropicCredentialType() is evaluated when the Routines / EvalScenarios
// package-level vars are built, i.e. at PACKAGE INIT. At that moment
// SEED_ANTHROPIC_API_KEY is not yet set: `crewship seed` only loads .env.local
// inside runSeed, long after init. So the baked type is always the placeholder
// path's API_KEY — even on a slot whose real key is an OAuth token, where the
// credential the seed creates is AI_CLI_TOKEN. The resolver matches type
// EXACTLY, so those routines 422 / doctor reports a mismatch.
//
// TestSeedDataIsSelfSatisfiable cannot catch this: it reads both sides in ONE
// process where both were baked from the same (init-time) environment, so they
// agree by construction. The divergence only exists ACROSS the init→runtime
// boundary, which is what these tests reconstruct.

import (
	"testing"
)

// reqWith builds a one-entry credentials_required definition, mimicking what
// package init produces: a type resolved before .env.local was read, plus the
// provider tag ReresolveAnthropicRequirements keys on.
func reqWith(bakedType, provider string) []RoutineDef {
	entry := map[string]interface{}{"type": bakedType, "scope": "any"}
	if provider != "" {
		entry["provider"] = provider
	}
	return []RoutineDef{{
		Slug:       "x",
		Definition: map[string]interface{}{"credentials_required": []map[string]interface{}{entry}},
	}}
}

func firstReqType(t *testing.T, defs []RoutineDef) string {
	t.Helper()
	reqs, ok := defs[0].Definition["credentials_required"].([]map[string]interface{})
	if !ok || len(reqs) == 0 {
		t.Fatalf("credentials_required missing or wrong shape: %#v", defs[0].Definition)
	}
	typ, _ := reqs[0]["type"].(string)
	return typ
}

// TestReresolveAnthropicRequirements covers the whole rewrite contract:
//   - the #1485 regression: an Anthropic entry baked to the placeholder API_KEY
//     is corrected to the seed's real type once the key is in the environment;
//   - the rewrite is scoped by the provider marker, NOT by the bare type
//     string, so a non-Anthropic API_KEY / AI_CLI_TOKEN requirement — the exact
//     provider-collision a type-only match would corrupt — is left untouched.
func TestReresolveAnthropicRequirements(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seedKey   string // SEED_ANTHROPIC_API_KEY at seed time (post .env.local)
		bakedType string // type frozen at package init
		provider  string // provider tag on the requirement ("" = none)
		want      string // expected type after re-resolve
	}{
		{
			name:      "anthropic placeholder rewritten to OAuth token type",
			seedKey:   "sk-ant-oat01-not-a-real-token",
			bakedType: "API_KEY", // what init froze with SEED_ANTHROPIC_API_KEY unset
			provider:  anthropicProvider,
			want:      "AI_CLI_TOKEN",
		},
		{
			name:      "anthropic api-key mode is a no-op",
			seedKey:   "sk-ant-api03-not-a-real-key",
			bakedType: "API_KEY",
			provider:  anthropicProvider,
			want:      "API_KEY",
		},
		{
			name:      "non-anthropic API_KEY left untouched under OAuth env",
			seedKey:   "sk-ant-oat01-not-a-real-token",
			bakedType: "API_KEY",
			provider:  "OPENAI",
			want:      "API_KEY",
		},
		{
			name:      "non-anthropic AI_CLI_TOKEN left untouched",
			seedKey:   "sk-ant-oat01-not-a-real-token",
			bakedType: "AI_CLI_TOKEN",
			provider:  "GITHUB",
			want:      "AI_CLI_TOKEN",
		},
		{
			name:      "provider-less entry left untouched",
			seedKey:   "sk-ant-oat01-not-a-real-token",
			bakedType: "API_KEY",
			provider:  "",
			want:      "API_KEY",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEED_ANTHROPIC_API_KEY", tc.seedKey)
			defs := reqWith(tc.bakedType, tc.provider)
			ReresolveAnthropicRequirements(defs)
			if got := firstReqType(t, defs); got != tc.want {
				t.Errorf("type after re-resolve = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAnthropicCredentialRequirementIsSelfTagged pins the invariant the seed
// data relies on: the helper every seeded Anthropic requirement is built from
// carries the provider marker, so ReresolveAnthropicRequirements will actually
// find it. Without the tag the re-resolve silently does nothing and #1485
// regresses.
func TestAnthropicCredentialRequirementIsSelfTagged(t *testing.T) {
	req := AnthropicCredentialRequirement()
	if p, _ := req["provider"].(string); p != anthropicProvider {
		t.Errorf("AnthropicCredentialRequirement provider = %q, want %q", p, anthropicProvider)
	}
	if req["type"] != AnthropicCredentialType() {
		t.Errorf("AnthropicCredentialRequirement type = %v, want %q", req["type"], AnthropicCredentialType())
	}
}
