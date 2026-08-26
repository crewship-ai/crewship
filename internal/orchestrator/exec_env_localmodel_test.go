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
	got := proxiedEndpointDomains(req)
	if len(got) != 1 || got[0] != "host.docker.internal" {
		t.Fatalf("proxiedEndpointDomains = %v, want [host.docker.internal]", got)
	}

	req.LLMModel = "anthropic/claude-sonnet-5"
	if got := proxiedEndpointDomains(req); len(got) != 0 {
		t.Errorf("cloud model: extra domains = %v, want none", got)
	}

	req = localModelReq()
	req.LocalModelBaseURL = ""
	if got := proxiedEndpointDomains(req); len(got) != 0 {
		t.Errorf("no base URL: extra domains = %v, want none", got)
	}

	req = localModelReq()
	req.LocalModelBaseURL = "://not-a-url"
	if got := proxiedEndpointDomains(req); len(got) != 0 {
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

// Phase 2 — the generated block only changes shape when the run is actually
// proxy-routed. An unauthenticated Ollama box has no secret to isolate, so it
// keeps pointing straight at the endpoint and the JSON stays byte-identical to
// what #944/#957 shipped, sidecar or not.
func TestLocalModelConfigEnv_UnauthenticatedOllamaUnchanged(t *testing.T) {
	const want = `OPENCODE_CONFIG_CONTENT={"provider":{"ollama":{"npm":"@ai-sdk/openai-compatible",` +
		`"name":"Ollama (local)","options":{"baseURL":"http://host.docker.internal:11434/v1"},` +
		`"models":{"qwen3-coder:30b":{"name":"qwen3-coder:30b"}}}}}`

	for _, viaSidecar := range []bool{false, true} {
		got, ok := localModelConfigEnv(localModelReq(), viaSidecar)
		if !ok {
			t.Fatalf("viaSidecar=%v: no config emitted", viaSidecar)
		}
		if got != want {
			t.Errorf("viaSidecar=%v:\n got: %s\nwant: %s", viaSidecar, got, want)
		}
	}
}

// The authenticated endpoint is the #961 exposure this phase closes. Table over
// the one input that decides it — whether there is a sidecar to route through —
// asserting both the endpoint the driver is sent to and that no credential
// material rides along.
func TestLocalModelConfigEnv_AuthenticatedEndpointPointsAtSidecar(t *testing.T) {
	tests := []struct {
		name        string
		viaSidecar  bool
		wantBaseURL string
		wantAPIKey  string
		wantHeaders bool
	}{
		{"sidecar run is routed and isolated", true, "http://127.0.0.1:9119/llm/openai-compat", "dummy-crewship-sidecar", false},
		{"no sidecar means nothing to route through", false, "http://host.docker.internal:11434/v1", "sk-tenant-secret", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := withDeliveredEndpointCred(func() AgentRunRequest {
				r := localModelReq()
				r.LocalModelAPIKey = "sk-tenant-secret"
				r.LocalModelHeaders = map[string]string{"X-Tenant": "acme"}
				return r
			}())

			raw, ok := localModelConfigEnv(req, tc.viaSidecar)
			if !ok {
				t.Fatal("no config emitted")
			}
			opts := parseOpencodeOptions(t, strings.TrimPrefix(raw, "OPENCODE_CONFIG_CONTENT="), "ollama")
			if opts.BaseURL != tc.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", opts.BaseURL, tc.wantBaseURL)
			}
			if opts.APIKey != tc.wantAPIKey {
				t.Errorf("apiKey = %q, want %q", opts.APIKey, tc.wantAPIKey)
			}
			if hasHeaders := len(opts.Headers) > 0; hasHeaders != tc.wantHeaders {
				t.Errorf("headers present = %v, want %v (%v)", hasHeaders, tc.wantHeaders, opts.Headers)
			}
		})
	}
}

// The endpoint's host has to stay on the crew allowlist once the run is routed.
// The agent no longer dials it — the SIDECAR does, and it checks the same crew
// allowlist first, so dropping the host here would make a routed restricted-mode
// crew egress-block its own model traffic.
func TestProxiedEndpointDomains_RoutedRunStillAllowlistsTheHost(t *testing.T) {
	req := withDeliveredEndpointCred(func() AgentRunRequest {
		r := localModelReq()
		r.LocalModelAPIKey = "sk-tenant-secret"
		return r
	}())

	if _, routed := resolveRoutedProvider(req, true); !routed {
		t.Fatal("precondition: an authenticated endpoint whose credential is delivered must be routed")
	}
	got := proxiedEndpointDomains(req)
	if len(got) != 1 || got[0] != "host.docker.internal" {
		t.Fatalf("proxiedEndpointDomains = %v, want [host.docker.internal]", got)
	}
}

// The literal-IP pre-fence is unchanged by the widening: a hard-blocked range is
// refused before it can reach the crew allowlist, opt-in or not.
func TestProxiedEndpointDomains_HardBlockedLiteralIPRefused(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		req := localModelReq()
		req.LocalModelBaseURL = "http://169.254.169.254/v1"
		req.LocalModelAPIKey = "sk-tenant-secret"
		req.AllowPrivateEndpoints = optIn
		if got := proxiedEndpointDomains(req); len(got) != 0 {
			t.Errorf("allowPrivate=%v: cloud-metadata endpoint must never be allowlisted, got %v", optIn, got)
		}
	}
}

// resolveRoutedProvider is the single gate every phase-2 behaviour hangs off.
// Table-driven over the whole decision so a future provider inherits the same
// refusals rather than discovering them in production.
func TestResolveRoutedProvider(t *testing.T) {
	orCred := Credential{ID: "c", EnvVarName: "OPENROUTER_API_KEY", PlainValue: "sk-or-v1-x", Type: "API_KEY", Provider: "OPENROUTER"}

	tests := []struct {
		name       string
		req        AgentRunRequest
		viaSidecar bool
		wantSpecID string // "" = not routed
	}{
		{"no sidecar, nothing to route through", authedLocalReq(), false, ""},
		{"authenticated endpoint routes to the compat descriptor", authedLocalReq(), true, "OPENAI_COMPAT"},
		{"unauthenticated endpoint is left alone", localModelReq(), true, ""},
		{"authenticated endpoint whose credential was withheld does not route", authedLocalReqNoCred(), true, ""},
		{"openrouter model with a matching credential", opencodeReq("openrouter/x/y", orCred), true, "OPENROUTER"},
		{"provider and bare model route without a duplicated prefix", func() AgentRunRequest {
			r := opencodeReq("gpt-5", orCred)
			r.LLMProvider = "OPENROUTER"
			return r
		}(), true, "OPENROUTER"},
		{"codex has a native custom-provider route", func() AgentRunRequest {
			r := opencodeReq("openrouter/x/y", orCred)
			r.CLIAdapter = "CODEX_CLI"
			return r
		}(), true, "OPENROUTER"},
		{"openrouter model without one", opencodeReq("openrouter/x/y"), true, ""},
		{"a grandfathered provider is not /llm/-routable", opencodeReq("anthropic/claude-sonnet-5", orCred), true, ""},
		{"unknown prefix", opencodeReq("deepseek/deepseek-v3.2", orCred), true, ""},
		{"bare model name has no prefix to route on", opencodeReq("claude-sonnet-5", orCred), true, ""},
		{"a different adapter has no config surface to point", func() AgentRunRequest {
			r := opencodeReq("openrouter/x/y", orCred)
			r.CLIAdapter = "CLAUDE_CODE"
			return r
		}(), true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveRoutedProvider(tc.req, tc.viaSidecar)
			if tc.wantSpecID == "" {
				if ok {
					t.Fatalf("routed to %q, want not routed", got.Spec.ID)
				}
				return
			}
			if !ok {
				t.Fatalf("not routed, want %s", tc.wantSpecID)
			}
			if got.Spec.ID != tc.wantSpecID {
				t.Errorf("routed to %q, want %q", got.Spec.ID, tc.wantSpecID)
			}
		})
	}
}

// withDeliveredEndpointCred attaches the OPENAI_COMPAT credential the API tier
// delivers alongside an authenticated local endpoint. Routing requires it —
// see resolveRoutedProvider — so any test asserting the ROUTED shape has to
// carry it, exactly as a real run does.
func withDeliveredEndpointCred(r AgentRunRequest) AgentRunRequest {
	r.Credentials = append(r.Credentials, Credential{
		ID:         "local-model-endpoint",
		PlainValue: r.LocalModelAPIKey,
		Type:       "API_KEY",
		Provider:   "OPENAI_COMPAT",
		BaseURL:    r.LocalModelBaseURL,
		Headers:    r.LocalModelHeaders,
	})
	return r
}

// authedLocalReq is a run whose local endpoint carries auth material AND whose
// credential list carries the matching OPENAI_COMPAT entry the API tier
// delivers alongside it (internal/api/agent_config.go).
//
// Both halves are required to route. The key alone is not enough: the API tier
// withholds the CredStore delivery when the crew is privileged and has not
// opted in (#1032), and routing on the key's presence alone would point
// OpenCode at a RequireCredential provider the proxy has nothing for.
func authedLocalReq() AgentRunRequest {
	r := localModelReq()
	r.LocalModelAPIKey = "sk-tenant-secret"
	r.Credentials = []Credential{{
		ID:         "local-model-endpoint",
		PlainValue: "sk-tenant-secret",
		Type:       "API_KEY",
		Provider:   "OPENAI_COMPAT",
		BaseURL:    r.LocalModelBaseURL,
	}}
	return r
}

// authedLocalReqNoCred is the same run with the credential withheld — the
// privileged-crew shape. It must NOT route.
func authedLocalReqNoCred() AgentRunRequest {
	r := localModelReq()
	r.LocalModelAPIKey = "sk-tenant-secret"
	return r
}
