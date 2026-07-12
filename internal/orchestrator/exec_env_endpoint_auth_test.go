package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// #961 Feature A — an authenticated local endpoint: the resolved auth token and
// custom headers must land in OPENCODE_CONFIG_CONTENT (options.apiKey /
// options.headers) and NEVER in the agent environment.

func TestOpencodeLocalConfigEnv_InjectsAuth(t *testing.T) {
	req := localModelReq()
	req.LocalModelAPIKey = "sk-tenant-secret"
	req.LocalModelHeaders = map[string]string{"X-Tenant": "acme"}

	for name, build := range map[string]func(AgentRunRequest) []string{
		"sidecar": func(r AgentRunRequest) []string { return BuildEnvVarsSidecar(r, false) },
		"direct":  func(r AgentRunRequest) []string { return BuildEnvVars(r, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			env := build(req)
			raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
			if !ok {
				t.Fatalf("OPENCODE_CONFIG_CONTENT missing: %v", env)
			}
			var cfg struct {
				Provider map[string]struct {
					Options struct {
						BaseURL string            `json:"baseURL"`
						APIKey  string            `json:"apiKey"`
						Headers map[string]string `json:"headers"`
					} `json:"options"`
				} `json:"provider"`
			}
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatalf("not valid JSON: %v\n%s", err, raw)
			}
			opts := cfg.Provider["ollama"].Options
			if opts.APIKey != "sk-tenant-secret" {
				t.Errorf("options.apiKey = %q, want the token", opts.APIKey)
			}
			if opts.Headers["X-Tenant"] != "acme" {
				t.Errorf("options.headers = %v", opts.Headers)
			}

			// The token must NOT appear as a bare env var anywhere.
			for _, e := range env {
				if strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT=") {
					continue // the config JSON legitimately contains it
				}
				if strings.Contains(e, "sk-tenant-secret") {
					t.Errorf("auth token leaked into env var: %q", e)
				}
			}
		})
	}
}

// No auth material → the config is byte-identical to the #944/#957 shape
// (omitempty keeps apiKey/headers out entirely).
func TestOpencodeLocalConfigEnv_NoAuthOmitsFields(t *testing.T) {
	env := BuildEnvVarsSidecar(localModelReq(), false)
	raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if !ok {
		t.Fatal("OPENCODE_CONFIG_CONTENT missing")
	}
	if strings.Contains(raw, "apiKey") || strings.Contains(raw, "headers") {
		t.Errorf("no-auth config must not contain apiKey/headers keys: %s", raw)
	}
}

// The auth token must never be added to the credential-exposure surface —
// it lives only in the generated config JSON, not the container env.
// #974 S2: the local-model endpoint token IS an env exposure — it is embedded
// in OPENCODE_CONFIG_CONTENT, which is an agent env var, and the openai-
// compatible driver dials the endpoint directly so the sidecar proxy cannot
// isolate it. It must therefore be reported by AgentEnvCredentialExposures so
// the isolation gap is observable rather than silently mislabeled as isolated.
func TestLocalModelToken_ReportedAsEnvExposure(t *testing.T) {
	req := localModelReq()
	req.LocalModelAPIKey = "sk-should-not-be-exposed"
	exposures := AgentEnvCredentialExposures(req, true)
	found := false
	for _, ex := range exposures {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" && ex.Type == "ENDPOINT_URL" {
			found = true
		}
	}
	if !found {
		t.Errorf("local-model endpoint token must be reported as an OPENCODE_CONFIG_CONTENT exposure, got %+v", exposures)
	}

	// With no auth material, there is nothing to expose.
	noAuth := localModelReq()
	for _, ex := range AgentEnvCredentialExposures(noAuth, true) {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" {
			t.Errorf("no auth token → no OPENCODE_CONFIG_CONTENT exposure, got %+v", ex)
		}
	}

	// #974 review: the auth is resolved for every agent in a workspace with an
	// authed ENDPOINT_URL, but OPENCODE_CONFIG_CONTENT is only actually emitted
	// for the OpenCode/ollama path. A mismatched-adapter run must NOT report a
	// phantom exposure.
	mismatch := localModelReq()
	mismatch.CLIAdapter = "CLAUDE" // config env is not emitted for this adapter
	mismatch.LocalModelAPIKey = "sk-resolved-but-unused"
	for _, ex := range AgentEnvCredentialExposures(mismatch, true) {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" {
			t.Errorf("non-OpenCode adapter → no OPENCODE_CONFIG_CONTENT exposure (config env isn't emitted), got %+v", ex)
		}
	}

	// Headers-only auth (no apiKey) on the active path still exposes.
	headersOnly := localModelReq()
	headersOnly.LocalModelHeaders = map[string]string{"X-Api-Key": "v"}
	foundHeaders := false
	for _, ex := range AgentEnvCredentialExposures(headersOnly, true) {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" {
			foundHeaders = true
		}
	}
	if !foundHeaders {
		t.Error("headers-only endpoint auth on the OpenCode path must be reported as an exposure")
	}
}
