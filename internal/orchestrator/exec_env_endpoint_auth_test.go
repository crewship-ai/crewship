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
func TestLocalModelToken_NotInEnvExposures(t *testing.T) {
	req := localModelReq()
	req.LocalModelAPIKey = "sk-should-not-be-exposed"
	exposures := AgentEnvCredentialExposures(req, true)
	for _, ex := range exposures {
		if strings.Contains(ex.EnvVarName, "LOCAL_MODEL") || strings.Contains(ex.Reason, "local-model") {
			t.Errorf("local-model token must not be an env exposure, got %+v", ex)
		}
	}
}
