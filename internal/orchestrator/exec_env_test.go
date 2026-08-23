package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

// Phase 2 — a provider becomes a credential. When a run is proxy-routed, the
// provider key stops being written into the agent's environment and lives only
// in the sidecar's CredStore. When it is NOT routed, every byte of the env block
// is what it was before phase 2: the two tests below pin exactly that pair, and
// the second one is the reason the first is allowed to exist.

func opencodeReq(model string, creds ...Credential) AgentRunRequest {
	return AgentRunRequest{
		AgentID:     "agent-1",
		AgentSlug:   "coder",
		CrewID:      "crew-1",
		ChatID:      "chat-1",
		CLIAdapter:  "OPENCODE",
		LLMModel:    model,
		Credentials: creds,
	}
}

// Deliberately unmistakable as a fixture rather than realistic-looking: a
// plausible-shaped key here trips the gitleaks pre-commit gate, and the repo's
// allowlist is keyed by commit SHA, so every amend or rebase would need a fresh
// entry for a string that was never a secret. The test greps for the literal, so
// its shape is irrelevant to what is being proven.
const openRouterSecret = "sk-or-v1-EXAMPLE-NOT-A-REAL-KEY-FIXTURE"

// TestBuildEnvVarsSidecar_RoutedOpenRouterKeyIsNotInEnv is the FR-3 assertion in
// its bluntest form: search the finished env block for the plaintext, the way
// failover_test.go does for Anthropic. A routed OpenRouter credential must not
// appear anywhere in it — not as its own variable, and not inside the generated
// OpenCode config either.
func TestBuildEnvVarsSidecar_RoutedOpenRouterKeyIsNotInEnv(t *testing.T) {
	req := opencodeReq("openrouter/anthropic/claude-sonnet-4-6", Credential{
		ID:         "cred-1",
		EnvVarName: "OPENROUTER_API_KEY",
		PlainValue: openRouterSecret,
		Type:       "API_KEY",
		Provider:   "OPENROUTER",
	})

	env := BuildEnvVarsSidecar(req, false)
	for _, e := range env {
		if strings.Contains(e, openRouterSecret) {
			t.Errorf("OpenRouter key leaked into the agent env: %q", e)
		}
	}
	if _, ok := envValue(env, "OPENROUTER_API_KEY"); ok {
		t.Error("OPENROUTER_API_KEY must not be set at all on a routed run")
	}

	raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if !ok {
		t.Fatalf("routed run must emit a provider block pointing at the sidecar: %v", env)
	}
	opts := parseOpencodeOptions(t, raw, "openrouter")
	if opts.BaseURL != "http://127.0.0.1:9119/llm/openrouter" {
		t.Errorf("options.baseURL = %q, want the sidecar's OpenRouter route", opts.BaseURL)
	}
	if opts.APIKey != "dummy-crewship-sidecar" {
		t.Errorf("options.apiKey = %q, want the dummy placeholder", opts.APIKey)
	}
}

// The routing decision is per-run and conservative. Each case below leaves the
// credential on its pre-phase-2 path, because in each of them the sidecar's
// CredStore would NOT hold the key under OPENROUTER — and withholding it from
// the env then means a 503 where a working run used to be.
func TestBuildEnvVarsSidecar_UnroutedOpenRouterKeepsItsKey(t *testing.T) {
	tests := []struct {
		name  string
		model string
		cred  Credential
	}{
		{
			name:  "no provider column: the sidecar's env-var switch drops it, so it never reaches the CredStore",
			model: "openrouter/anthropic/claude-sonnet-4-6",
			cred:  Credential{ID: "c", EnvVarName: "OPENROUTER_API_KEY", PlainValue: openRouterSecret, Type: "API_KEY"},
		},
		{
			name:  "provider column names someone else",
			model: "openrouter/anthropic/claude-sonnet-4-6",
			cred:  Credential{ID: "c", EnvVarName: "OPENROUTER_API_KEY", PlainValue: openRouterSecret, Type: "API_KEY", Provider: "OPENAI"},
		},
		{
			name:  "env var belongs to another provider, which claims the credential first",
			model: "openrouter/anthropic/claude-sonnet-4-6",
			cred:  Credential{ID: "c", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: openRouterSecret, Type: "API_KEY", Provider: "OPENROUTER"},
		},
		{
			name:  "model is not routed through the sidecar at all",
			model: "deepseek/deepseek-v3.2",
			cred:  Credential{ID: "c", EnvVarName: "OPENROUTER_API_KEY", PlainValue: openRouterSecret, Type: "API_KEY", Provider: "OPENROUTER"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := BuildEnvVarsSidecar(opencodeReq(tc.model, tc.cred), false)
			got, ok := envValue(env, tc.cred.EnvVarName)
			if !ok || got != openRouterSecret {
				t.Fatalf("%s must still be delivered to env, got %q (present=%v)", tc.cred.EnvVarName, got, ok)
			}
			if _, ok := envValue(env, "OPENCODE_CONFIG_CONTENT"); ok {
				t.Error("an unrouted run must not gain a generated provider block")
			}
		})
	}
}

// The isolation above is only safe if it is surgical. This golden pins the whole
// env block for an ordinary, unrouted OpenCode run byte for byte — captured from
// main before phase 2 touched this file. It is the guard against the routed path
// quietly changing what every existing crew gets.
func TestBuildEnvVarsSidecar_UnroutedOpenCodeUnchanged(t *testing.T) {
	req := opencodeReq("anthropic/claude-sonnet-5",
		Credential{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-api03-real", Type: "API_KEY", Provider: "ANTHROPIC"},
		Credential{ID: "c2", EnvVarName: "OPENROUTER_API_KEY", PlainValue: openRouterSecret, Type: "API_KEY", Provider: "OPENROUTER"},
		Credential{ID: "c3", EnvVarName: "GH_TOKEN", PlainValue: "ghp_realtoken", Type: "CLI_TOKEN"},
	)

	want := []string{
		"HOME=/crew/agents/coder",
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=agent-1",
		"CREWSHIP_CREW_ID=crew-1",
		"CREWSHIP_CHAT_ID=chat-1",
		"CREWSHIP_CREW_SHARED=/crew/shared",
		"HTTP_PROXY=http://127.0.0.1:9119",
		"HTTPS_PROXY=http://127.0.0.1:9119",
		"http_proxy=http://127.0.0.1:9119",
		"https_proxy=http://127.0.0.1:9119",
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:9119",
		"ANTHROPIC_API_KEY=sk-ant-api03-real",
		"OPENAI_API_KEY=sk-dummy-crewship-sidecar",
		"GOOGLE_API_KEY=dummy-crewship-sidecar",
		"CREWSHIP_BILLING_MODE=metered",
		"OPENROUTER_API_KEY=" + openRouterSecret,
		"GH_TOKEN=ghp_realtoken",
	}

	got := BuildEnvVarsSidecar(req, true)
	if len(got) != len(want) {
		t.Fatalf("env block has %d entries, want %d\n got: %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// apiKeyEnvVarsForAdapter is deliberately NOT narrowed for phase 2: an OpenCode
// crew whose model is not routed through the sidecar still needs every key in
// the set. Narrowing it would break those runs invisibly, so the routing filter
// lives in the loop instead — and this pins the distinction.
func TestAPIKeyEnvVarsForAdapter_OpenCodeKeepsOpenRouter(t *testing.T) {
	allowed := apiKeyEnvVarsForAdapter("OPENCODE")
	if _, ok := allowed["OPENROUTER_API_KEY"]; !ok {
		t.Error("OPENROUTER_API_KEY must stay in the OPENCODE set — unrouted OpenCode runs read it from env")
	}
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI", "UNKNOWN"} {
		if got := apiKeyEnvVarsForAdapter(adapter); len(got) != 0 {
			t.Errorf("apiKeyEnvVarsForAdapter(%q) = %v, want empty", adapter, got)
		}
	}
}

const openAICompatSecret = "byo-endpoint-secret-9f2c4a1b6d8e"

// The headline capability of the credential phase, asserted end to end: an
// operator pastes a bring-your-own OpenAI-compatible endpoint as a credential,
// and an agent run actually goes through the sidecar to it with the secret
// withheld from the container.
//
// This test exists because that flow shipped INERT. resolveRoutedProvider's
// model-prefix branch refused every UpstreamFromCredential spec outright, so
// the credential was stored, validated, delivered to the CredStore — and
// nothing ever dialled /llm/openai-compat. The whole operator-facing surface
// (the --base-url flag, the endpoint credential type, sixty lines of docs
// describing "the first agent call through the sidecar is the test") described
// a call that could not happen. Nothing failed; it just did nothing.
func TestBuildEnvVarsSidecar_RoutedOpenAICompatEndpoint(t *testing.T) {
	const endpoint = "https://gateway.acme.example/v1"
	req := opencodeReq("openai_compat/llama-3.1-70b", Credential{
		ID:         "cred-byo",
		EnvVarName: "OPENAI_COMPAT_API_KEY",
		PlainValue: openAICompatSecret,
		Type:       "API_KEY",
		Provider:   "OPENAI_COMPAT",
		BaseURL:    endpoint,
	})

	env := BuildEnvVarsSidecar(req, false)

	for _, e := range env {
		if strings.Contains(e, openAICompatSecret) {
			t.Errorf("the endpoint secret leaked into the agent env: %q", e)
		}
	}

	raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if !ok {
		t.Fatalf("a routed bring-your-own endpoint must emit a provider block; got env %v", env)
	}
	if !strings.Contains(raw, "127.0.0.1:9119") {
		t.Errorf("the provider block must point at the sidecar, not at the endpoint directly: %s", raw)
	}
	if strings.Contains(raw, endpoint) {
		t.Errorf("the real upstream must not appear in the agent's config — the sidecar holds it: %s", raw)
	}
}

// The allowlist must name the host the SIDECAR will dial, which for an
// endpoint-carrying credential is the credential's own base URL — not
// req.LocalModelBaseURL. When an assigned credential won over a synthetic
// endpoint these disagreed, and restricted-mode crews got a 403 on every call
// with every credential valid. The two answers now come from one place.
func TestProxiedEndpointDomains_UsesTheHostTheSidecarWillDial(t *testing.T) {
	req := opencodeReq("openai_compat/llama-3.1-70b", Credential{
		ID:         "cred-byo",
		EnvVarName: "OPENAI_COMPAT_API_KEY",
		PlainValue: openAICompatSecret,
		Type:       "API_KEY",
		Provider:   "OPENAI_COMPAT",
		BaseURL:    "https://gateway.acme.example/v1",
	})
	// A different synthetic endpoint, the way a crew with a local Ollama box
	// resolved one before the credential was assigned.
	req.LocalModelBaseURL = "http://ollama.box.internal:11434/v1"

	got := proxiedEndpointDomains(req)
	want := "gateway.acme.example"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("allowlist = %v, want [%s] — the sidecar dials the credential's host, so allowlisting the synthetic one 403s every call", got, want)
	}
}

// The same question on the OTHER branch. resolveRoutedProvider reaches
// OPENAI_COMPAT two ways: by model prefix ("openai_compat/…", covered above) and
// by a resolved local endpoint ("ollama/…", covered here). Only the first
// branch was ever wired to UpstreamHost, so this exact collision — a crew with
// BOTH a resolved local endpoint and an assigned OPENAI_COMPAT credential —
// allowlisted the local box while the sidecar dialled the gateway.
//
// The precedence being relied on is appendProxiedEndpointCredential's: it logs
// the collision and delivers the ASSIGNED credential, never the resolved
// endpoint. So the credential's host is the only correct answer here.
func TestProxiedEndpointDomains_LocalEndpointBranchUsesTheCredentialsHost(t *testing.T) {
	req := opencodeReq("ollama/llama-3.1-70b", Credential{
		ID:         "cred-assigned",
		EnvVarName: "OPENAI_COMPAT_API_KEY",
		PlainValue: openAICompatSecret,
		Type:       "API_KEY",
		Provider:   "OPENAI_COMPAT",
		BaseURL:    "https://gateway.acme.example/v1",
	})
	// The workspace also resolved a local endpoint, with a token of its own —
	// which is what puts this run on the localEndpointModel branch at all.
	req.LocalModelBaseURL = "http://10.0.0.5:11434/v1"
	req.LocalModelAPIKey = "local-box-token"
	req.AllowPrivateEndpoints = true // or the RFC1918 host is dropped for other reasons

	rp, routed := resolveRoutedProvider(req, true)
	if !routed {
		t.Fatal("run must be routed: a local endpoint with a token and a matching credential is the routed case")
	}
	if rp.UpstreamHost != "https://gateway.acme.example/v1" {
		t.Errorf("UpstreamHost = %q, want the assigned credential's base URL", rp.UpstreamHost)
	}

	got := proxiedEndpointDomains(req)
	want := "gateway.acme.example"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("allowlist = %v, want [%s] — 10.0.0.5 is what the crew would have been allowed to reach while the sidecar dialled the gateway, which is a 403 on every model call with every credential valid", got, want)
	}
}

// A bring-your-own endpoint whose ONLY secret lives in a custom header must be
// isolated in the sidecar like any other. It was not: llmroute.ApplyAuth
// returned early on an empty token and dropped the custom headers with it, so
// routing such an endpoint would have sent an unauthenticated request. The
// endpoint was therefore left unrouted and its headers written into
// OPENCODE_CONFIG_CONTENT — which the agent process can simply read.
//
// The documentation said so plainly, which is not the same as it being all
// right. CodeRabbit's phrasing on the PR: "Documentation does not mitigate this
// exposure."
func TestBuildEnvVarsSidecar_HeaderOnlyEndpointIsIsolated(t *testing.T) {
	const headerSecret = "header-only-endpoint-secret-7b3d"

	// The credential the API tier synthesises for exactly this case
	// (appendProxiedEndpointCredential): no env var, no bearer token, the whole
	// secret in Headers.
	req := opencodeReq("ollama/llama-3.1-70b", Credential{
		ID:       "local-model-endpoint",
		Type:     "API_KEY",
		Provider: "OPENAI_COMPAT",
		BaseURL:  "https://gateway.acme.example/v1",
		Headers:  map[string]string{"X-Api-Key": headerSecret},
	})
	req.LocalModelBaseURL = "https://gateway.acme.example/v1"
	req.LocalModelHeaders = map[string]string{"X-Api-Key": headerSecret}
	req.AllowPrivateEndpoints = true

	rp, routed := resolveRoutedProvider(req, true)
	if !routed {
		t.Fatal("an endpoint carrying header auth must be routed; leaving it unrouted is what put the secret in the agent env")
	}
	if !rp.Spec.UpstreamFromCredential {
		t.Errorf("routed through %q, want the endpoint-backed spec", rp.Spec.ID)
	}

	env := BuildEnvVarsSidecar(req, false)
	for _, e := range env {
		if strings.Contains(e, headerSecret) {
			t.Errorf("the header secret reached the agent env: %q", e)
		}
	}

	raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if !ok {
		t.Fatalf("a routed run must emit a provider block; got %v", env)
	}
	if strings.Contains(raw, headerSecret) || strings.Contains(raw, "X-Api-Key") {
		t.Errorf("the provider block still carries the endpoint's auth headers: %s", raw)
	}
	if !strings.Contains(raw, "127.0.0.1:9119") {
		t.Errorf("the provider block must point at the sidecar: %s", raw)
	}

	// And the posture report must stop calling it an exposure, because it is
	// no longer one.
	for _, ex := range AgentEnvCredentialExposures(req, true) {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" {
			t.Errorf("a routed header-only endpoint is still reported as an env exposure: %+v", ex)
		}
	}
}

// The other half: with no auth material at all there is nothing to isolate, and
// OPENAI_COMPAT is RequireCredential — routing a bare endpoint would put a 503
// in front of a path that works today.
func TestResolveRoutedProvider_BareEndpointStaysUnrouted(t *testing.T) {
	req := opencodeReq("ollama/llama-3.1-70b")
	req.LocalModelBaseURL = "http://127.0.0.1:11434/v1"

	if _, routed := resolveRoutedProvider(req, true); routed {
		t.Error("an endpoint with no auth material must stay unrouted")
	}
}

// Headers must not stand in for a token on a FIXED-upstream provider. Nothing
// populates Credential.Headers for those specs today — the field is written by
// the endpoint split alone — but "nothing does it today" is how the env-var
// mapping came to deliver an endpoint credential to api.openai.com. A
// token-less credential routed to a real vendor host arrives unauthenticated,
// and the run's key is withheld from the env at the same time, so the failure
// is a 503 where a working run used to be.
func TestCredentialRoutesTo_HeadersAreNotATokenForFixedUpstreamProviders(t *testing.T) {
	tests := []struct {
		name      string
		specID    string
		cred      Credential
		wantRoute bool
	}{
		{
			name:      "endpoint-backed spec accepts headers alone",
			specID:    "OPENAI_COMPAT",
			cred:      Credential{Provider: "OPENAI_COMPAT", Headers: map[string]string{"X-Api-Key": "s"}},
			wantRoute: true,
		},
		{
			name:      "fixed-upstream spec does not",
			specID:    "OPENROUTER",
			cred:      Credential{Provider: "OPENROUTER", Headers: map[string]string{"X-Api-Key": "s"}},
			wantRoute: false,
		},
		{
			name:      "fixed-upstream spec still accepts a real token",
			specID:    "OPENROUTER",
			cred:      Credential{Provider: "OPENROUTER", PlainValue: openRouterSecret},
			wantRoute: true,
		},
		{
			name:      "nothing at all routes nowhere",
			specID:    "OPENAI_COMPAT",
			cred:      Credential{Provider: "OPENAI_COMPAT"},
			wantRoute: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := llmroute.Lookup(tc.specID)
			if !ok {
				t.Fatalf("spec %q is not registered", tc.specID)
			}
			if got := credentialRoutesTo(tc.cred, s); got != tc.wantRoute {
				t.Errorf("credentialRoutesTo = %v, want %v", got, tc.wantRoute)
			}
		})
	}
}

// The sidecar's CredStore refuses a credential whose lease has lapsed. The API
// tier applies that filter at delivery-query time, so a grant expiring between
// the query and the exec is still sitting in req.Credentials — and a host-side
// selector that ignores it routes the run to a provider the proxy then has
// nothing for. For an endpoint-backed spec it also names an UpstreamHost the
// sidecar will never dial, so the egress allowlist describes a call that cannot
// happen.
func TestCredentialFor_SkipsLapsedLeases(t *testing.T) {
	s, ok := llmroute.Lookup("OPENAI_COMPAT")
	if !ok {
		t.Fatal("OPENAI_COMPAT is not registered")
	}
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	base := func(id, lease string, prio int) Credential {
		return Credential{
			ID: id, Provider: "OPENAI_COMPAT", PlainValue: openAICompatSecret,
			BaseURL: "https://" + id + ".example/v1", LeaseExpiresAt: lease, Priority: prio,
		}
	}

	t.Run("a lapsed credential is not selected even at better priority", func(t *testing.T) {
		req := opencodeReq("openai_compat/m", base("expired", past, 0), base("live", future, 5))
		got, ok := credentialFor(req, s)
		if !ok || got.ID != "live" {
			t.Errorf("credentialFor = %q (ok=%v), want the unexpired one", got.ID, ok)
		}
	})

	t.Run("a standing credential has no deadline and always qualifies", func(t *testing.T) {
		req := opencodeReq("openai_compat/m", base("standing", "", 0))
		if got, ok := credentialFor(req, s); !ok || got.ID != "standing" {
			t.Errorf("credentialFor = %q (ok=%v), want standing", got.ID, ok)
		}
	})

	t.Run("an unparseable deadline reads as lapsed, matching leaseEpochSentinel", func(t *testing.T) {
		req := opencodeReq("openai_compat/m", base("corrupt", "not-a-timestamp", 0))
		if got, ok := credentialFor(req, s); ok {
			t.Errorf("credentialFor selected %q; a deadline we cannot read must fail closed", got.ID)
		}
	})

	t.Run("the allowlist drops a lapsed credential's host too", func(t *testing.T) {
		req := opencodeReq("openai_compat/m", base("expired", past, 0), base("live", future, 5))
		got := proxiedEndpointDomains(req)
		for _, h := range got {
			if h == "expired.example" {
				t.Errorf("allowlist = %v; it names a host the sidecar will never dial", got)
			}
		}
		if len(got) != 1 || got[0] != "live.example" {
			t.Errorf("allowlist = %v, want [live.example]", got)
		}
	})
}
