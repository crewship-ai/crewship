package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// #961 Feature A / #974 S2 — an authenticated local endpoint. Which of the two
// shapes the generated config takes depends on whether there is a sidecar to
// route through, and the difference IS the isolation guarantee:
//
//   - sidecar run: the block points at 127.0.0.1:9119, carries a dummy key and
//     no headers, and the real token appears NOWHERE in the env — not even
//     inside OPENCODE_CONFIG_CONTENT. The sidecar holds it.
//   - no-sidecar run: there is nothing between the driver and the endpoint, so
//     the token and headers go in the config, as they always have.

func TestOpencodeLocalConfigEnv_SidecarRunIsolatesEndpointAuth(t *testing.T) {
	req := localModelReq()
	req.LocalModelAPIKey = "sk-tenant-secret"
	req.LocalModelHeaders = map[string]string{"X-Tenant": "acme"}
	// The run is routed only when the credential is on its way to the CredStore.
	// Carrying it here also sharpens the leak assertion below: the secret is now
	// present in req.Credentials, so "not in the env" is a real result rather
	// than a value that was never in scope.
	req = withDeliveredEndpointCred(req)

	env := BuildEnvVarsSidecar(req, false)
	raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if !ok {
		t.Fatalf("OPENCODE_CONFIG_CONTENT missing: %v", env)
	}
	opts := parseOpencodeOptions(t, raw, "ollama")
	if opts.BaseURL != "http://127.0.0.1:9119/llm/openai-compat" {
		t.Errorf("options.baseURL = %q, want the sidecar route", opts.BaseURL)
	}
	if opts.APIKey != "dummy-crewship-sidecar" {
		t.Errorf("options.apiKey = %q, want the dummy placeholder", opts.APIKey)
	}
	if len(opts.Headers) != 0 {
		t.Errorf("options.headers = %v, want omitted — a custom header on an authenticated endpoint is credential material too", opts.Headers)
	}
	// The whole point: no env entry anywhere carries the real secret, and this
	// time the config JSON gets no exemption.
	for _, e := range env {
		if strings.Contains(e, "sk-tenant-secret") || strings.Contains(e, "acme") {
			t.Errorf("endpoint auth leaked into the agent env: %q", e)
		}
	}
}

func TestOpencodeLocalConfigEnv_NoSidecarRunStillCarriesAuth(t *testing.T) {
	req := localModelReq()
	req.LocalModelAPIKey = "sk-tenant-secret"
	req.LocalModelHeaders = map[string]string{"X-Tenant": "acme"}

	env := BuildEnvVars(req, nil)
	raw, ok := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if !ok {
		t.Fatalf("OPENCODE_CONFIG_CONTENT missing: %v", env)
	}
	opts := parseOpencodeOptions(t, raw, "ollama")
	if opts.BaseURL != "http://host.docker.internal:11434/v1" {
		t.Errorf("options.baseURL = %q, want the endpoint itself", opts.BaseURL)
	}
	if opts.APIKey != "sk-tenant-secret" {
		t.Errorf("options.apiKey = %q, want the token", opts.APIKey)
	}
	if opts.Headers["X-Tenant"] != "acme" {
		t.Errorf("options.headers = %v", opts.Headers)
	}

	// Even here the token must not become a bare env var of its own.
	for _, e := range env {
		if strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT=") {
			continue // the config JSON legitimately contains it on this path
		}
		if strings.Contains(e, "sk-tenant-secret") {
			t.Errorf("auth token leaked into env var: %q", e)
		}
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

// AgentEnvCredentialExposures mirrors BuildEnvVarsSidecar, so once the endpoint
// auth is routed through the sidecar there is nothing left to report for it —
// #974 S2 is closed on that path. The branch that reports it survives because it
// mirrors localModelConfigEnv's unrouted arm, which is still what a no-sidecar
// run gets; the assertions below pin both halves so neither can drift into a
// claim the other contradicts.
func TestLocalModelToken_NotExposedOnceRouted(t *testing.T) {
	req := withDeliveredEndpointCred(func() AgentRunRequest {
		r := localModelReq()
		r.LocalModelAPIKey = "sk-should-not-be-exposed"
		return r
	}())
	for _, ex := range AgentEnvCredentialExposures(req, true) {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" {
			t.Errorf("routed endpoint auth must not be reported as an env exposure, got %+v", ex)
		}
	}

	// Headers-only auth is NOT routed, and must still be reported.
	//
	// llmroute.ApplyAuth is a no-op on an empty token, so the sidecar would
	// forward the request without the custom headers and the endpoint would
	// reject it. The API tier therefore withholds the credential and the run
	// stays direct, with the headers in the OpenCode config — which is an env
	// exposure and has to be named as one. Reporting it as isolated here would
	// tell an operator their header secret is in the sidecar's heap when it is
	// in the container's environment.
	headersOnly := localModelReq()
	headersOnly.LocalModelHeaders = map[string]string{"X-Api-Key": "v"}
	if _, routed := resolveRoutedProvider(headersOnly, true); routed {
		t.Error("a headers-only endpoint was routed; the proxy cannot write its headers")
	}
	reported := false
	for _, ex := range AgentEnvCredentialExposures(headersOnly, true) {
		if ex.EnvVarName == "OPENCODE_CONFIG_CONTENT" {
			reported = true
		}
	}
	if !reported {
		t.Error("headers-only endpoint auth rides in the agent env but was not reported as an exposure")
	}

	// With no auth material there was never anything to expose, and the block
	// still points straight at the endpoint.
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

	// The arm the surviving exposure branch mirrors: unrouted, the token really
	// is in the config env var. If this ever stops being true the branch is dead
	// and should go with it.
	raw, ok := localModelConfigEnv(req, false)
	if !ok || !strings.Contains(raw, "sk-should-not-be-exposed") {
		t.Fatalf("unrouted config must still carry the token, got ok=%v %s", ok, raw)
	}
}

// parseOpencodeOptions decodes one provider block's options out of an
// OPENCODE_CONFIG_CONTENT value.
func parseOpencodeOptions(t *testing.T, raw, providerID string) struct {
	BaseURL string            `json:"baseURL"`
	APIKey  string            `json:"apiKey"`
	Headers map[string]string `json:"headers"`
} {
	t.Helper()
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
	p, ok := cfg.Provider[providerID]
	if !ok {
		t.Fatalf("provider block missing %q: %s", providerID, raw)
	}
	return p.Options
}
