package seeddata

// #1460 — the shipped demo data must be able to satisfy its own requirements.
//
// A routine's `credentials_required` names a credential TYPE, and the resolver
// matches it exactly (`AND UPPER(type) = UPPER(?)`, credential_resolver.go).
// 35 declarations named "anthropic" — a PROVIDER, not a type — so the match
// could never be true and 17 demo routines plus 18 eval scenarios failed every
// run with a 422. It read as a missing feature: the HITL approval-gate test
// looked like a broken gate when in fact the routine's first step 422'd before
// the gate was ever reached.
//
// The subtlety that makes a hardcoded literal wrong: the Anthropic type is NOT
// fixed. ResolveAnthropicCredential returns AI_CLI_TOKEN for an OAuth token
// (sk-ant-oat…) and API_KEY for everything else — including the demo
// placeholder used when SEED_ANTHROPIC_API_KEY is unset. "Just write
// AI_CLI_TOKEN" is therefore correct in one seeding mode and 422s in the
// other, which is the DEFAULT one.

import (
	"strings"
	"testing"
)

// declaredTypes pulls every credentials_required type out of the built seed
// data — the real values, so the interpolation is exercised rather than the
// source text pattern-matched.
func declaredTypes(defs []RoutineDef) []string {
	var out []string
	for _, d := range defs {
		reqs, ok := d.Definition["credentials_required"].([]map[string]interface{})
		if !ok {
			continue
		}
		for _, r := range reqs {
			if typ, ok := r["type"].(string); ok {
				out = append(out, typ)
			}
		}
	}
	return out
}

// TestSeedDataIsSelfSatisfiable is the core invariant. Routines and
// EvalScenarios are package-level vars built at init, and so is the type they
// declare — both from the same ResolveAnthropicCredential call against the same
// environment. Whatever mode the package initialised in, the requirement and
// the credential must agree.
//
// That "same mode" property is exactly what a hardcoded literal cannot give.
func TestSeedDataIsSelfSatisfiable(t *testing.T) {
	want := strings.ToUpper(AnthropicCredentialType())
	if want == "" {
		t.Fatal("AnthropicCredentialType() is empty — the seed would declare an unsatisfiable requirement")
	}

	for _, tc := range []struct {
		label string
		defs  []RoutineDef
	}{
		{"routines", Routines},
		{"eval scenarios", EvalScenarios},
	} {
		got := declaredTypes(tc.defs)
		if len(got) == 0 {
			t.Errorf("%s: no credentials_required declarations found — did the shape change? "+
				"This test would silently pass forever if so.", tc.label)
			continue
		}
		for _, typ := range got {
			if strings.ToUpper(typ) != want {
				t.Errorf("%s declares credentials_required type %q but the seed creates %q. "+
					"The resolver matches type EXACTLY, so every run of that routine 422s.",
					tc.label, typ, want)
			}
		}
	}
}

// TestAnthropicCredentialTypeCoversBothModes pins the branch that made a
// hardcoded literal wrong. Called as a function (not read off a var), so
// t.Setenv genuinely switches the mode here.
func TestAnthropicCredentialTypeCoversBothModes(t *testing.T) {
	for _, tc := range []struct {
		name, key, want string
	}{
		{"oauth token", "sk-ant-oat01-not-a-real-token", "AI_CLI_TOKEN"},
		{"api key", "sk-ant-api03-not-a-real-key", "API_KEY"},
		{"unset — demo placeholder", "", "API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEED_ANTHROPIC_API_KEY", tc.key)
			if got := AnthropicCredentialType(); got != tc.want {
				t.Errorf("AnthropicCredentialType() = %q, want %q — a routine requiring the "+
					"other value would 422 in this seeding mode", got, tc.want)
			}
		})
	}
}

// TestRequiredTypeIsNeverAProviderName guards the specific confusion behind
// #1460: a provider ("anthropic", "openai") used where a structural type
// (API_KEY, AI_CLI_TOKEN, SECRET…) belongs. They are different fields and the
// resolver only ever reads one of them.
func TestRequiredTypeIsNeverAProviderName(t *testing.T) {
	providerish := map[string]bool{
		"ANTHROPIC": true, "OPENAI": true, "GOOGLE": true,
		"GITHUB": true, "OLLAMA": true, "CURSOR": true, "FACTORY": true,
	}
	for _, set := range [][]string{declaredTypes(Routines), declaredTypes(EvalScenarios)} {
		for _, typ := range set {
			if providerish[strings.ToUpper(typ)] {
				t.Errorf("credentials_required type is %q — that is a PROVIDER name. "+
					"Type must be structural (API_KEY, AI_CLI_TOKEN, SECRET, …); the resolver "+
					"never looks at provider, so this can never match.", typ)
			}
		}
	}
}
