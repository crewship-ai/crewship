// Package credprovider is the single source of truth for credential provider
// identifiers, their conventional agent-facing environment variable names, and
// the devcontainer feature each provider's CLI arrives in.
//
// Both the crewshipd HTTP handler (GET /api/v1/credentials/default-env-var)
// and the `crewship credential` CLI (the --provider flag help and the
// default-env-var command) reference this package so the provider enum and the
// provider→env-var map can't drift apart (#1083). The provider→feature map
// backs GET /api/v1/crews/{crewId}/credential-readiness.
package credprovider

import (
	"sort"
	"strings"
)

// Providers is the canonical, ordered list of recognized credential providers,
// used to render CLI flag help. It is documentation-grade: the server does not
// reject unknown providers, but keeping one list means the CLI help and the
// server's env-var map describe the same set.
//
// Keys match the frontend brand registry (lib/credential-providers/registry.ts)
// wherever a brand exists there, so a credential created from the CLI renders
// with the right icon in the dashboard instead of the generic key glyph.
var Providers = []string{
	// AI / inference
	"ANTHROPIC", "OPENAI", "GOOGLE", "HUGGINGFACE", "PERPLEXITY",
	"REPLICATE", "ELEVENLABS", "OLLAMA", "OPENROUTER", "OPENAI_COMPAT",
	// Cloud / hosting
	"AWS", "GCP", "AZURE", "CLOUDFLARE", "VERCEL", "NETLIFY",
	"RAILWAY", "DIGITALOCEAN", "HEROKU", "SUPABASE",
	// Infrastructure / orchestration
	"KUBERNETES", "DOCKER", "TERRAFORM", "PULUMI", "ANSIBLE",
	// Source control / packaging
	"GITHUB", "GITLAB", "NPM",
	// SaaS an agent scripts against
	"SLACK", "NOTION", "LINEAR", "STRIPE", "SENTRY", "DATADOG",
	"TWILIO", "SENDGRID", "RESEND", "NGROK",
	// Fallbacks
	"CUSTOM_CLI", "NONE",
}

// defaultEnvVars maps the providers that have a conventional environment
// variable an agent reads inside its container. Providers absent from this map
// have no default (the caller must supply --env-var-name).
//
// This is a SUGGESTION surface, never a gate: DefaultEnvVar returns "" for
// anything unmapped and the API accepts any provider string. Every entry is
// the variable the vendor's own CLI or SDK documents reading — a plausible-
// looking guess is worse than no entry, because the user trusts the suggestion
// and the failure mode is a silently-unauthenticated tool at run time, not a
// startup error.
//
// Deliberately absent (the tool reads no single documented variable, so any
// value here would be invented): DOCKER (the docker CLI reads ~/.docker/config.json,
// not an env var), TERRAFORM (HCP Terraform's token variable is host-suffixed,
// TF_TOKEN_<hostname>), ANSIBLE, CUSTOM_CLI, NONE, and OPENAI_COMPAT — that
// last one is not an oversight but the point: an OPENAI_COMPAT credential is a
// {baseURL, apiKey, headers} object dialled by the sidecar, not a token any
// agent-side tool reads from its environment, so there is no conventional
// variable to suggest and suggesting one would invite exactly the env-var
// delivery the provider exists to avoid.
var defaultEnvVars = map[string]string{
	// AI / inference — the SDK/CLI variable, not the proxy header. Crewship
	// injects API_KEY / AI_CLI_TOKEN credentials through the sidecar proxy,
	// but an agent shelling out to a vendor CLI still expects these names.
	"ANTHROPIC":   "ANTHROPIC_API_KEY",
	"OPENAI":      "OPENAI_API_KEY",
	"GOOGLE":      "GOOGLE_API_KEY", // Gemini / Google Gen AI SDK (not gcloud — see GCP)
	"HUGGINGFACE": "HF_TOKEN",
	"PERPLEXITY":  "PERPLEXITY_API_KEY",
	"REPLICATE":   "REPLICATE_API_TOKEN",
	"ELEVENLABS":  "ELEVENLABS_API_KEY",
	"OLLAMA":      "OLLAMA_HOST",        // an endpoint, not a secret — Ollama has no auth
	"OPENROUTER":  "OPENROUTER_API_KEY", // the variable OpenCode/the OpenAI SDK read for openrouter/<model>

	// Cloud / hosting
	"AWS":          "AWS_ACCESS_KEY_ID",
	"GCP":          "GOOGLE_APPLICATION_CREDENTIALS", // ADC: path to the service-account JSON
	"AZURE":        "AZURE_CLIENT_SECRET",            // Azure Identity EnvironmentCredential
	"CLOUDFLARE":   "CLOUDFLARE_API_TOKEN",
	"VERCEL":       "VERCEL_TOKEN",
	"NETLIFY":      "NETLIFY_AUTH_TOKEN",
	"RAILWAY":      "RAILWAY_TOKEN",
	"DIGITALOCEAN": "DIGITALOCEAN_ACCESS_TOKEN",
	"HEROKU":       "HEROKU_API_KEY",
	"SUPABASE":     "SUPABASE_ACCESS_TOKEN",
	"PULUMI":       "PULUMI_ACCESS_TOKEN",
	"KUBERNETES":   "KUBECONFIG", // a path, mounted as a file credential
	"NGROK":        "NGROK_AUTHTOKEN",

	// Source control / packaging
	"GITHUB": "GH_TOKEN", // gh reads GH_TOKEN first, GITHUB_TOKEN second
	"GITLAB": "GITLAB_TOKEN",
	"NPM":    "NPM_TOKEN", // referenced from .npmrc as ${NPM_TOKEN}

	// SaaS
	"SLACK":    "SLACK_BOT_TOKEN",
	"NOTION":   "NOTION_API_KEY",
	"LINEAR":   "LINEAR_API_KEY",
	"STRIPE":   "STRIPE_API_KEY",
	"SENTRY":   "SENTRY_AUTH_TOKEN",
	"DATADOG":  "DD_API_KEY",
	"TWILIO":   "TWILIO_AUTH_TOKEN",
	"SENDGRID": "SENDGRID_API_KEY",
	"RESEND":   "RESEND_API_KEY",
}

// requiredFeatures maps a provider to the devcontainer feature ref that
// installs the CLI its credential is useless without.
//
// The failure mode this exists to surface: the sandbox runtime image
// (docker/crewship-sandbox/Dockerfile) ships git, curl and jq and nothing
// else. `gh`, `aws`, `az`, `gcloud`, `kubectl`, `docker`, `terraform`,
// `node`/`npm` and `ansible` arrive ONLY if the crew's devcontainer config
// declares the matching feature. Without this map, a user can add a perfectly
// valid GitHub credential, see it green in the vault, and watch the agent
// still fail with "gh: command not found" — with nothing anywhere connecting
// the two facts.
//
// Only refs we can point at a published feature are listed. A wrong ref is
// worse than a missing one: the readiness report would tell the user to add a
// feature that does not resolve, and the next devcontainer build fails on the
// OCI reference instead of on the missing tool.
//
// Note GOOGLE is deliberately NOT here. In this enum GOOGLE means the Gemini
// API key (see probeProvider in internal/api/credentials_test_endpoint.go),
// which needs no gcloud; GCP is the Google Cloud credential that does.
var requiredFeatures = map[string]string{
	"GITHUB":     "ghcr.io/devcontainers/features/github-cli:1",
	"AWS":        "ghcr.io/devcontainers/features/aws-cli:1",
	"AZURE":      "ghcr.io/devcontainers/features/azure-cli:1",
	"GCP":        "ghcr.io/dhoeric/features/google-cloud-cli:1",
	"KUBERNETES": "ghcr.io/devcontainers/features/kubectl-helm-minikube:1",
	"DOCKER":     "ghcr.io/devcontainers/features/docker-in-docker:2",
	"TERRAFORM":  "ghcr.io/devcontainers/features/terraform:1",
	"ANSIBLE":    "ghcr.io/devcontainers-extra/features/ansible:2",
	"NPM":        "ghcr.io/devcontainers/features/node:1", // npm ships with node
}

// DefaultEnvVar returns the conventional environment variable name for a
// provider, or "" when the provider has no conventional default.
func DefaultEnvVar(provider string) string {
	return defaultEnvVars[provider]
}

// canonicalByFold indexes Providers by upper-case name, so a provider the
// operator typed in any casing can be folded back onto its canonical spelling.
var canonicalByFold = func() map[string]string {
	m := make(map[string]string, len(Providers))
	for _, p := range Providers {
		m[strings.ToUpper(p)] = p
	}
	return m
}()

// Canonical folds a provider string onto its canonical spelling when it names a
// recognized provider, and returns it UNCHANGED otherwise.
//
// The asymmetry is the point. Every consumer that does something with a
// provider — the endpoint-value gate, the sidecar's CredStore routing, the
// frontend's brand registry — keys off the canonical uppercase name, so
// "openai_compat" typed into the dashboard or posted by an API client silently
// missed all of them: the credential stored fine, validated nothing, and routed
// nowhere. But the provider column has never been a closed enum, and an
// operator who stores "MyInternalVault" means that string, not its shouted
// form. Folding only what we recognize fixes the first case without inventing a
// restriction that was never there.
func Canonical(provider string) string {
	trimmed := strings.TrimSpace(provider)
	if c, ok := canonicalByFold[strings.ToUpper(trimmed)]; ok {
		return c
	}
	return trimmed
}

// RequiredFeature returns the devcontainer feature ref that installs the CLI
// this provider's credential is meant to be read by, or "" when we have no
// opinion. "" means "no requirement to report", never "unsupported provider".
func RequiredFeature(provider string) string {
	return requiredFeatures[provider]
}

// ProvidersWithRequiredFeature returns, sorted, every provider that maps to a
// devcontainer feature. Sorted so callers that render or test the set get
// stable output instead of Go's randomized map order.
func ProvidersWithRequiredFeature() []string {
	out := make([]string, 0, len(requiredFeatures))
	for p := range requiredFeatures {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ProvidersHelp renders the provider enum as a pipe-joined string for CLI flag
// help (e.g. "ANTHROPIC|OPENAI|...").
func ProvidersHelp() string {
	return strings.Join(Providers, "|")
}
