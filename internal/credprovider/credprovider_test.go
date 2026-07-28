package credprovider

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestDefaultEnvVar pins the conventional env-var mappings the CLI and server
// both depend on. Spot-checks rather than the whole table: the shape
// invariants below cover every entry; this pins the ones a typo would
// silently break for the most users.
func TestDefaultEnvVar(t *testing.T) {
	cases := map[string]string{
		"GITHUB":      "GH_TOKEN",
		"GITLAB":      "GITLAB_TOKEN",
		"VERCEL":      "VERCEL_TOKEN",
		"AWS":         "AWS_ACCESS_KEY_ID",
		"KUBERNETES":  "KUBECONFIG",
		"ANTHROPIC":   "ANTHROPIC_API_KEY",
		"OPENAI":      "OPENAI_API_KEY",
		"GCP":         "GOOGLE_APPLICATION_CREDENTIALS",
		"HUGGINGFACE": "HF_TOKEN",
		"DATADOG":     "DD_API_KEY",
		// Providers whose CLI reads no single conventional variable MUST
		// stay unmapped — see TestDefaultEnvVarNeverGates.
		"DOCKER":     "",
		"TERRAFORM":  "",
		"CUSTOM_CLI": "",
		"NONE":       "",
		"":           "",
	}
	for provider, want := range cases {
		if got := DefaultEnvVar(provider); got != want {
			t.Errorf("DefaultEnvVar(%q) = %q, want %q", provider, got, want)
		}
	}
}

// envVarNamePattern is the POSIX-portable shape every entry must have:
// uppercase snake, starting with a letter. A lowercase or hyphenated
// suggestion would be copied verbatim into `credential assign
// --env-var-name` and produce a variable the container's shell cannot
// export.
var envVarNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// TestDefaultEnvVarShape is the table invariant over the whole map: a
// blank or malformed value is worse than no entry at all, because the
// user trusts the suggestion and pastes it into their agent config.
func TestDefaultEnvVarShape(t *testing.T) {
	if len(defaultEnvVars) < 25 {
		t.Errorf("defaultEnvVars shrank to %d entries — the suggestion list is meant to "+
			"cover the common agent-CLI providers", len(defaultEnvVars))
	}
	for provider, envVar := range defaultEnvVars {
		if strings.TrimSpace(envVar) == "" {
			t.Errorf("provider %q maps to an empty env var — omit the entry instead, "+
				"DefaultEnvVar already returns \"\" for unmapped providers", provider)
			continue
		}
		if !envVarNamePattern.MatchString(envVar) {
			t.Errorf("provider %q maps to %q, which is not an uppercase-snake env var name", provider, envVar)
		}
		if !envVarNamePattern.MatchString(provider) {
			t.Errorf("provider key %q is not uppercase-snake — the enum is compared verbatim "+
				"against credentials.provider, which the API stores as given", provider)
		}
	}
}

// TestDefaultEnvVarNeverGates is the load-bearing property: the map is a
// SUGGESTION surface, not an allowlist. An unknown provider must resolve
// to "" and never panic, because the server accepts any provider string
// and a user with a niche tool must still be able to store its token.
func TestDefaultEnvVarNeverGates(t *testing.T) {
	for _, unknown := range []string{
		"ACME_INTERNAL", "github", "GitHub", "  GITHUB  ", "🔑", "NOT_A_PROVIDER",
	} {
		if got := DefaultEnvVar(unknown); got != "" {
			t.Errorf("DefaultEnvVar(%q) = %q, want \"\" — unknown providers must fall through, not be rejected", unknown, got)
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

// TestProvidersAreUnique guards the other direction of the same drift: a
// duplicated entry renders twice in `--provider` help and silently
// doubles any future range-over-Providers loop.
func TestProvidersAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Providers {
		if seen[p] {
			t.Errorf("provider %q listed twice in Providers", p)
		}
		seen[p] = true
	}
}

// TestProvidersHelp keeps the CLI flag help derived from the enum rather
// than a hand-maintained string that drifts out of it.
func TestProvidersHelp(t *testing.T) {
	help := ProvidersHelp()
	for _, p := range Providers {
		if !strings.Contains(help, p) {
			t.Errorf("ProvidersHelp() omits %q", p)
		}
	}
	if strings.Count(help, "|") != len(Providers)-1 {
		t.Errorf("ProvidersHelp() = %q, want %d providers pipe-joined", help, len(Providers))
	}
}

// featureRefPattern matches an OCI devcontainer feature ref carrying an
// explicit version tag: registry/namespace/features/<id>:<tag>. A ref
// without a tag resolves to :latest at build time, which silently
// changes the installed CLI under a crew that never changed its config.
var featureRefPattern = regexp.MustCompile(`^[a-z0-9.-]+(/[a-z0-9._-]+)+:[0-9]+(\.[0-9]+)*$`)

// TestRequiredFeatureShape pins the feature refs. A wrong ref is worse
// than a missing one: the readiness report would tell the user to add a
// feature that does not exist, and the devcontainer build then fails on
// an unresolvable OCI reference.
func TestRequiredFeatureShape(t *testing.T) {
	if len(requiredFeatures) < 8 {
		t.Errorf("requiredFeatures has only %d entries — the point is to cover the "+
			"providers whose CLI is absent from the sandbox base image", len(requiredFeatures))
	}
	for provider, ref := range requiredFeatures {
		if !slices.Contains(Providers, provider) {
			t.Errorf("provider %q requires a feature but is missing from the Providers enum", provider)
		}
		if !featureRefPattern.MatchString(ref) {
			t.Errorf("provider %q maps to %q, which is not a tagged OCI feature ref", provider, ref)
		}
		if !strings.Contains(ref, "/features/") {
			t.Errorf("provider %q maps to %q — devcontainer feature refs live under a /features/ path", provider, ref)
		}
	}
}

// TestRequiredFeatureNeverGates mirrors TestDefaultEnvVarNeverGates: the
// readiness report is advisory, so a provider we have no opinion about
// must report no requirement rather than a spurious gap.
func TestRequiredFeatureNeverGates(t *testing.T) {
	for _, unknown := range []string{"ACME_INTERNAL", "github", "NONE", "CUSTOM_CLI", ""} {
		if got := RequiredFeature(unknown); got != "" {
			t.Errorf("RequiredFeature(%q) = %q, want \"\"", unknown, got)
		}
	}
}

// TestProvidersWithRequiredFeature is the iteration helper the API's
// coherence test walks; it must expose every mapped provider exactly
// once and in a stable order.
func TestProvidersWithRequiredFeature(t *testing.T) {
	got := ProvidersWithRequiredFeature()
	if len(got) != len(requiredFeatures) {
		t.Fatalf("ProvidersWithRequiredFeature() returned %d entries, want %d", len(got), len(requiredFeatures))
	}
	if !slices.IsSorted(got) {
		t.Errorf("ProvidersWithRequiredFeature() = %v, want sorted for stable output", got)
	}
	for _, p := range got {
		if requiredFeatures[p] == "" {
			t.Errorf("ProvidersWithRequiredFeature() returned %q, which has no feature", p)
		}
	}
}
