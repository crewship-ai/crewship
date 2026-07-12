package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// #944 — local-model (Ollama) path for OpenCode. When an OPENCODE agent
// selects an "ollama/…" model and the operator configured a local-model
// base URL, the env builders must inject OPENCODE_CONFIG_CONTENT with a
// generated provider block pointing at that endpoint. No user-controlled
// JSON ever reaches the env — the block is marshalled from a struct.

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

func localModelReq() AgentRunRequest {
	return AgentRunRequest{
		AgentID:           "agent-1",
		AgentSlug:         "coder",
		CrewID:            "crew-1",
		ChatID:            "chat-1",
		CLIAdapter:        "OPENCODE",
		LLMModel:          "ollama/qwen3-coder:30b",
		LocalModelBaseURL: "http://host.docker.internal:11434/v1",
	}
}

func TestOpencodeLocalConfigEnv_InjectedForOllamaModel(t *testing.T) {
	for name, build := range map[string]func(AgentRunRequest) []string{
		"sidecar": func(r AgentRunRequest) []string { return BuildEnvVarsSidecar(r, false) },
		"direct":  func(r AgentRunRequest) []string { return BuildEnvVars(r, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			env := build(localModelReq())
			raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
			if !ok {
				t.Fatalf("OPENCODE_CONFIG_CONTENT missing from env: %v", env)
			}
			var cfg struct {
				Provider map[string]struct {
					NPM     string `json:"npm"`
					Options struct {
						BaseURL string `json:"baseURL"`
					} `json:"options"`
					Models map[string]struct {
						Name string `json:"name"`
					} `json:"models"`
				} `json:"provider"`
			}
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatalf("OPENCODE_CONFIG_CONTENT is not valid JSON: %v\n%s", err, raw)
			}
			p, ok := cfg.Provider["ollama"]
			if !ok {
				t.Fatalf("provider block missing 'ollama': %s", raw)
			}
			if p.NPM != "@ai-sdk/openai-compatible" {
				t.Errorf("npm = %q, want @ai-sdk/openai-compatible", p.NPM)
			}
			if p.Options.BaseURL != "http://host.docker.internal:11434/v1" {
				t.Errorf("baseURL = %q", p.Options.BaseURL)
			}
			if _, ok := p.Models["qwen3-coder:30b"]; !ok {
				t.Errorf("models missing requested 'qwen3-coder:30b': %s", raw)
			}
		})
	}
}

func TestOpencodeLocalConfigEnv_AbsentWhenNotApplicable(t *testing.T) {
	cases := map[string]func(AgentRunRequest) AgentRunRequest{
		"no base URL configured": func(r AgentRunRequest) AgentRunRequest {
			r.LocalModelBaseURL = ""
			return r
		},
		"cloud model": func(r AgentRunRequest) AgentRunRequest {
			r.LLMModel = "anthropic/claude-sonnet-5"
			return r
		},
		"different adapter": func(r AgentRunRequest) AgentRunRequest {
			r.CLIAdapter = "CLAUDE_CODE"
			r.LLMModel = "claude-sonnet-5"
			return r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := mutate(localModelReq())
			for buildName, env := range map[string][]string{
				"sidecar": BuildEnvVarsSidecar(req, false),
				"direct":  BuildEnvVars(req, nil),
			} {
				if v, ok := envValue(env, "OPENCODE_CONFIG_CONTENT"); ok {
					t.Errorf("%s: OPENCODE_CONFIG_CONTENT unexpectedly injected: %s", buildName, v)
				}
			}
		})
	}
}

// Restricted network mode must auto-allow the local endpoint's host so the
// sidecar proxy doesn't block the model traffic the operator explicitly
// enabled. Off (empty) unless the local-model path is active.
func TestLocalModelExtraDomains(t *testing.T) {
	req := localModelReq()
	got := localModelExtraDomains(req)
	if len(got) != 1 || got[0] != "host.docker.internal" {
		t.Fatalf("localModelExtraDomains = %v, want [host.docker.internal]", got)
	}

	req.LLMModel = "anthropic/claude-sonnet-5"
	if got := localModelExtraDomains(req); len(got) != 0 {
		t.Errorf("cloud model: extra domains = %v, want none", got)
	}

	req = localModelReq()
	req.LocalModelBaseURL = ""
	if got := localModelExtraDomains(req); len(got) != 0 {
		t.Errorf("no base URL: extra domains = %v, want none", got)
	}

	req = localModelReq()
	req.LocalModelBaseURL = "://not-a-url"
	if got := localModelExtraDomains(req); len(got) != 0 {
		t.Errorf("unparseable base URL: extra domains = %v, want none", got)
	}
}

// #955 — credential-sourced endpoint wins; the deprecated env is only a
// fallback. effectiveLocalModelBaseURL is the single precedence gate.
func TestEffectiveLocalModelBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		fromCred     string
		fromEnv      string
		wantURL      string
		wantFallback bool
	}{
		{"credential wins over env", "http://cred:11434/v1", "http://env:11434/v1", "http://cred:11434/v1", false},
		{"credential wins when env empty", "http://cred:11434/v1", "", "http://cred:11434/v1", false},
		{"env fallback when no credential", "", "http://env:11434/v1", "http://env:11434/v1", true},
		{"none configured", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, gotFallback := effectiveLocalModelBaseURL(tc.fromCred, tc.fromEnv)
			if gotURL != tc.wantURL || gotFallback != tc.wantFallback {
				t.Errorf("effectiveLocalModelBaseURL(%q,%q) = (%q,%v), want (%q,%v)",
					tc.fromCred, tc.fromEnv, gotURL, gotFallback, tc.wantURL, tc.wantFallback)
			}
		})
	}
}
