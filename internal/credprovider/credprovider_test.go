package credprovider

import (
	"slices"
	"testing"
)

// TestDefaultEnvVar pins the conventional env-var mappings the CLI and server
// both depend on.
func TestDefaultEnvVar(t *testing.T) {
	cases := map[string]string{
		"GITHUB":     "GH_TOKEN",
		"GITLAB":     "GITLAB_TOKEN",
		"VERCEL":     "VERCEL_TOKEN",
		"AWS":        "AWS_ACCESS_KEY_ID",
		"KUBERNETES": "KUBECONFIG",
		"ANTHROPIC":  "", // has no conventional default
		"":           "",
	}
	for provider, want := range cases {
		if got := DefaultEnvVar(provider); got != want {
			t.Errorf("DefaultEnvVar(%q) = %q, want %q", provider, got, want)
		}
	}
}

// TestEnvVarProvidersAreListed is the anti-drift invariant: every provider that
// has a default env var must also appear in the canonical Providers enum, so
// the CLI --provider help never omits a provider the server maps (#1083).
func TestEnvVarProvidersAreListed(t *testing.T) {
	for provider := range defaultEnvVars {
		if !slices.Contains(Providers, provider) {
			t.Errorf("provider %q has a default env var but is missing from Providers enum", provider)
		}
	}
}
