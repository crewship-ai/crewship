// Package credprovider is the single source of truth for credential provider
// identifiers and their conventional agent-facing environment variable names.
//
// Both the crewshipd HTTP handler (GET /api/v1/credentials/default-env-var)
// and the `crewship credential` CLI (the --provider flag help and the
// default-env-var command) reference this package so the provider enum and the
// provider→env-var map can't drift apart (#1083).
package credprovider

import "strings"

// Providers is the canonical, ordered list of recognized credential providers,
// used to render CLI flag help. It is documentation-grade: the server does not
// reject unknown providers, but keeping one list means the CLI help and the
// server's env-var map describe the same set.
var Providers = []string{
	"ANTHROPIC", "OPENAI", "GOOGLE",
	"GITHUB", "GITLAB", "VERCEL", "AWS", "KUBERNETES",
	"OLLAMA", "CUSTOM_CLI", "NONE",
}

// defaultEnvVars maps the providers that have a conventional environment
// variable an agent reads inside its container. Providers absent from this map
// have no default (the caller must supply --env-var-name).
var defaultEnvVars = map[string]string{
	"GITHUB":     "GH_TOKEN",
	"GITLAB":     "GITLAB_TOKEN",
	"VERCEL":     "VERCEL_TOKEN",
	"AWS":        "AWS_ACCESS_KEY_ID",
	"KUBERNETES": "KUBECONFIG",
}

// DefaultEnvVar returns the conventional environment variable name for a
// provider, or "" when the provider has no conventional default.
func DefaultEnvVar(provider string) string {
	return defaultEnvVars[provider]
}

// ProvidersHelp renders the provider enum as a pipe-joined string for CLI flag
// help (e.g. "ANTHROPIC|OPENAI|...").
func ProvidersHelp() string {
	return strings.Join(Providers, "|")
}
