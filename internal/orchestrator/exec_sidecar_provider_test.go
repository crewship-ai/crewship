package orchestrator

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestCredTypeToProvider_EnvVarStillWins pins the byte-identity constraint: the
// pre-phase-2 env-var switch is consulted FIRST, so every credential that
// reached the sidecar CredStore before reaches it under exactly the same
// provider now — including when the provider column disagrees.
//
// Preferring the column would be a behaviour change for credentials that work
// today: a row carrying provider=OPENAI delivered under ANTHROPIC_API_KEY is
// legal and currently lands under ANTHROPIC.
func TestCredTypeToProvider_EnvVarStillWins(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		wantProv string
	}{
		{"anthropic by env var", Credential{EnvVarName: "ANTHROPIC_API_KEY"}, "ANTHROPIC"},
		{"openai by env var", Credential{EnvVarName: "OPENAI_API_KEY"}, "OPENAI"},
		{"google by env var", Credential{EnvVarName: "GOOGLE_API_KEY"}, "GOOGLE"},
		{"gemini alias by env var", Credential{EnvVarName: "GEMINI_API_KEY"}, "GOOGLE"},
		{"cursor keeps its arm", Credential{EnvVarName: "CURSOR_API_KEY"}, "CURSOR"},
		{"factory keeps its arm", Credential{EnvVarName: "FACTORY_API_KEY"}, "FACTORY"},

		// The disagreement cases. The env var wins in every one.
		{
			"column disagrees with env var",
			Credential{EnvVarName: "ANTHROPIC_API_KEY", Provider: "OPENAI"},
			"ANTHROPIC",
		},
		{
			"openrouter column under an anthropic env var",
			Credential{EnvVarName: "ANTHROPIC_API_KEY", Provider: "OPENROUTER"},
			"ANTHROPIC",
		},
		{
			"cursor env var with a routable column",
			Credential{EnvVarName: "CURSOR_API_KEY", Provider: "OPENROUTER"},
			"CURSOR",
		},

		// Still dropped: no env var arm, no routable column.
		{"unknown env var, no column", Credential{EnvVarName: "GH_TOKEN"}, ""},
		{"unknown env var, unroutable column", Credential{EnvVarName: "GH_TOKEN", Provider: "GITHUB"}, ""},
		{"default provider column", Credential{EnvVarName: "GH_TOKEN", Provider: "NONE"}, ""},
		{"empty everything", Credential{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credTypeToProvider(tt.cred); got != tt.wantProv {
				t.Errorf("credTypeToProvider() = %q, want %q", got, tt.wantProv)
			}
		})
	}
}

// TestCredTypeToProvider_ColumnResolvesWhatEnvVarDrops is the red-on-main test
// for E3. Before the provider column travelled, a perfectly-stored OpenRouter
// or OpenAI-compatible credential was dropped between the vault and the sidecar
// with no log — the only symptom was a 401 from the upstream, which blames the
// key rather than the delivery.
func TestCredTypeToProvider_ColumnResolvesWhatEnvVarDrops(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		wantProv string
	}{
		{
			"openrouter names no env-var arm",
			Credential{EnvVarName: "OPENROUTER_API_KEY", Provider: "OPENROUTER"},
			"OPENROUTER",
		},
		{
			"an endpoint credential carries no env var at all",
			Credential{EnvVarName: "", Provider: "OPENAI_COMPAT"},
			"OPENAI_COMPAT",
		},
		{
			"openrouter under a bound slot name",
			Credential{EnvVarName: "TEAM_LLM_KEY", Provider: "OPENROUTER"},
			"OPENROUTER",
		},

		// The provider COLUMN is free text a human types — the dashboard and
		// every REST client send it verbatim, and only the CLI ever folded
		// case. An earlier version of this file pinned "lowercase column does
		// not resolve" as though it were a designed property; it was the
		// defect. A credential stored as "openrouter" validated nothing on the
		// way in and was dropped from the CredStore on the way out, and the
		// only symptom was a 401 blaming the key.
		{
			"lower-case column resolves — the operator typed a provider, not a wire value",
			Credential{EnvVarName: "GH_TOKEN", Provider: "openrouter"},
			"OPENROUTER",
		},
		{
			"mixed case resolves",
			Credential{EnvVarName: "", Provider: "OpenAI_Compat"},
			"OPENAI_COMPAT",
		},
		{
			"surrounding whitespace resolves",
			Credential{EnvVarName: "", Provider: "  OPENAI_COMPAT  "},
			"OPENAI_COMPAT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := credTypeToProvider(tt.cred)
			if got != tt.wantProv {
				t.Errorf("credTypeToProvider() = %q, want %q — the provider column "+
					"did not reach the CredStore, so this credential is silently dropped",
					got, tt.wantProv)
			}
		})
	}
}

// TestStartSidecar_OpenRouterCredentialReachesBootPayload drives the real boot
// payload builder and decodes the base64 stdin blob the sidecar actually
// receives, rather than asserting against an intermediate struct.
func TestStartSidecar_OpenRouterCredentialReachesBootPayload(t *testing.T) {
	creds := []Credential{
		{ID: "c-anthropic", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-real", Priority: 0},
		{ID: "c-openrouter", EnvVarName: "OPENROUTER_API_KEY", PlainValue: "sk-or-v1-real", Provider: "OPENROUTER", Priority: 1},
		{ID: "c-compat", Provider: "OPENAI_COMPAT", PlainValue: "sk-litellm-real", BaseURL: "https://llm.internal.example/v1", Headers: map[string]string{"X-Org": "acme"}, Priority: 2},
		{ID: "c-github", EnvVarName: "GH_TOKEN", PlainValue: "ghp_real", Provider: "GITHUB", Priority: 3},
	}

	payload := covBuildSidecarCredsPayload(t, creds)

	byID := map[string]map[string]any{}
	for _, c := range payload {
		byID[c["id"].(string)] = c
	}

	if _, ok := byID["c-github"]; ok {
		t.Error("a GITHUB credential must not reach the sidecar CredStore")
	}

	or, ok := byID["c-openrouter"]
	if !ok {
		t.Fatal("the OpenRouter credential never reached the boot payload — " +
			"credTypeToProvider dropped it because its env var names no arm")
	}
	if or["provider"] != "OPENROUTER" {
		t.Errorf("openrouter provider = %v, want OPENROUTER", or["provider"])
	}
	if or["token"] != "sk-or-v1-real" {
		t.Errorf("openrouter token = %v, want the real key", or["token"])
	}
	if _, has := or["base_url"]; has {
		t.Error("a fixed-upstream provider must not carry base_url — omitempty is " +
			"what keeps the payload byte-identical for every credential without one")
	}

	compat, ok := byID["c-compat"]
	if !ok {
		t.Fatal("the OPENAI_COMPAT credential never reached the boot payload")
	}
	if compat["base_url"] != "https://llm.internal.example/v1" {
		t.Errorf("compat base_url = %v, want the credential's endpoint", compat["base_url"])
	}
	hdrs, _ := compat["headers"].(map[string]any)
	if hdrs["X-Org"] != "acme" {
		t.Errorf("compat headers = %v, want X-Org: acme", compat["headers"])
	}
	if compat["token"] != "sk-litellm-real" {
		t.Errorf("compat token = %v — the stored JSON object must never travel "+
			"as the token; the API tier splits it", compat["token"])
	}

	if byID["c-anthropic"]["provider"] != "ANTHROPIC" {
		t.Errorf("anthropic provider = %v, want ANTHROPIC", byID["c-anthropic"]["provider"])
	}
}

// TestSidecarCredPayload_ByteIdenticalWithoutNewFields pins the omitempty
// contract: a credential carrying none of the phase-2 fields must serialise
// exactly as it did before they existed, so an older sidecar sees no change.
func TestSidecarCredPayload_ByteIdenticalWithoutNewFields(t *testing.T) {
	raw := covMarshalSidecarCred(t, Credential{
		ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-x", Priority: 7,
	})
	const want = `{"id":"c1","provider":"ANTHROPIC","token":"sk-ant-x","priority":7}`
	if raw != want {
		t.Errorf("boot payload changed shape for an unchanged credential:\n got %s\nwant %s", raw, want)
	}
}

// covBuildSidecarCredsPayload runs creds through the real boot-payload mapping
// and returns the decoded credential objects — asserting against the JSON the
// sidecar unmarshals, not against the Go struct, so a wrong tag is caught.
func covBuildSidecarCredsPayload(t *testing.T, creds []Credential) []map[string]any {
	t.Helper()
	blob, err := json.Marshal(buildSidecarCreds(creds, slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("marshal sidecar creds: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("unmarshal sidecar creds: %v", err)
	}
	return out
}

// covMarshalSidecarCred returns the exact JSON one credential becomes.
func covMarshalSidecarCred(t *testing.T, c Credential) string {
	t.Helper()
	sc := buildSidecarCreds([]Credential{c}, nil)
	if len(sc) != 1 {
		t.Fatalf("credential was dropped by buildSidecarCreds: %+v", c)
	}
	b, err := json.Marshal(sc[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestBuildSidecarCreds_EmptyIsAnEmptyArray pins that a run with no routable
// credential still serialises "credentials":[] rather than null — the sidecar
// unmarshals into a slice and null would change the payload for every agent
// that has no provider key.
func TestBuildSidecarCreds_EmptyIsAnEmptyArray(t *testing.T) {
	b, err := json.Marshal(buildSidecarCreds([]Credential{{EnvVarName: "GH_TOKEN", Provider: "GITHUB"}}, nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("empty credential set marshalled to %s, want []", b)
	}
}

// TestBuildEnvVarsSidecar_SyntheticEndpointCredentialStaysOutOfEnv is the
// end-to-end form of the #961 claim, from the delivery side.
//
// The API tier delivers the resolved authenticated endpoint as an OPENAI_COMPAT
// credential with an EMPTY env-var name, precisely so it reaches the sidecar's
// CredStore and nothing else. Every env and /secrets path first requires a
// non-empty name — this asserts the whole assembled block, so a future path that
// forgets that check fails here rather than in production.
func TestBuildEnvVarsSidecar_SyntheticEndpointCredentialStaysOutOfEnv(t *testing.T) {
	const secret = "sk-tenant-secret-value"

	req := localModelReq()
	req.LocalModelAPIKey = secret
	req.LocalModelHeaders = map[string]string{"X-Tenant": "acme"}
	// Exactly what api.appendProxiedEndpointCredential builds.
	req.Credentials = []Credential{{
		ID:         "local-model-endpoint",
		EnvVarName: "",
		PlainValue: secret,
		Type:       "API_KEY",
		Provider:   "OPENAI_COMPAT",
		BaseURL:    "http://host.docker.internal:11434/v1",
		Headers:    map[string]string{"X-Tenant": "acme"},
	}}

	env := BuildEnvVarsSidecar(req, false)
	for _, e := range env {
		if strings.Contains(e, secret) {
			t.Errorf("the endpoint key reached the agent environment: %s", e)
		}
	}

	// …and it DOES reach the sidecar's CredStore, or the routed run 503s.
	sc := buildSidecarCreds(req.Credentials, nil)
	if len(sc) != 1 {
		t.Fatalf("buildSidecarCreds returned %d credentials, want 1 — the routed "+
			"run would find no credential at the proxy and 503", len(sc))
	}
	if sc[0].Provider != "OPENAI_COMPAT" || sc[0].Token != secret {
		t.Errorf("credstore entry = %+v, want OPENAI_COMPAT holding the real key", sc[0])
	}
	if sc[0].BaseURL != "http://host.docker.internal:11434/v1" {
		t.Errorf("credstore entry lost the upstream: %q", sc[0].BaseURL)
	}
}

// TestResolveRoutedProvider_RefusesWithoutTheCredential is the fail-safe that
// keeps routing from ever being speculative.
//
// The API tier withholds the CredStore delivery in cases where the endpoint key
// still travels on the env path — a privileged crew without
// allow_privileged_credentials is the live one (#1032). Routing on the key's
// presence alone would point OpenCode at a RequireCredential provider the proxy
// has nothing for: a 503 where the run works today.
func TestResolveRoutedProvider_RefusesWithoutTheCredential(t *testing.T) {
	endpointCred := Credential{
		ID: "local-model-endpoint", PlainValue: "sk-tenant-secret",
		Type: "API_KEY", Provider: "OPENAI_COMPAT",
		BaseURL: "http://host.docker.internal:11434/v1",
	}

	tests := []struct {
		name      string
		creds     []Credential
		wantRoute bool
	}{
		{
			name:      "the credential is on its way to the credstore, so route",
			creds:     []Credential{endpointCred},
			wantRoute: true,
		},
		{
			name:      "auth material but no credential delivered: do not route",
			creds:     nil,
			wantRoute: false,
		},
		{
			name:      "somebody else's credential does not answer for this spec",
			creds:     []Credential{{ID: "x", PlainValue: "sk-ant", EnvVarName: "ANTHROPIC_API_KEY", Provider: "ANTHROPIC"}},
			wantRoute: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := localModelReq()
			req.LocalModelAPIKey = "sk-tenant-secret"
			req.Credentials = tt.creds

			routed, ok := resolveRoutedProvider(req, true)
			if ok != tt.wantRoute {
				t.Fatalf("routed = %v, want %v", ok, tt.wantRoute)
			}
			if !ok {
				return
			}
			if routed.Spec.ID != "OPENAI_COMPAT" {
				t.Errorf("routed to %q, want OPENAI_COMPAT", routed.Spec.ID)
			}
		})
	}
}

// TestResolveRoutedProvider_UnroutedRunStillGetsItsKey states the other half of
// the trade: when the run is not routed, the key must stay on the env path or
// the agent has no way to reach its endpoint at all.
func TestResolveRoutedProvider_UnroutedRunStillGetsItsKey(t *testing.T) {
	req := localModelReq()
	req.LocalModelAPIKey = "sk-tenant-secret"
	req.Credentials = nil // the privileged-crew case: withheld from the CredStore

	if _, ok := resolveRoutedProvider(req, true); ok {
		t.Fatal("routed without a credential at the proxy")
	}
	raw, ok := localModelConfigEnv(req, true)
	if !ok {
		t.Fatal("no OpenCode config emitted for an unrouted local-endpoint run")
	}
	if !strings.Contains(raw, "sk-tenant-secret") {
		t.Error("the unrouted run lost its key: it is neither in the config block " +
			"nor at the proxy, so the endpoint is unreachable")
	}
}

// TestSidecarCredWireTags pins the boot payload's JSON key set.
//
// sidecarCred and sidecar.Credential are two structs in two packages joined only
// by these strings: the orchestrator marshals one and the sidecar unmarshals the
// other. A renamed tag does not fail to compile and does not fail to unmarshal —
// it silently delivers a credential with that field zeroed, which for base_url
// means an OPENAI_COMPAT credential that routes nowhere.
func TestSidecarCredWireTags(t *testing.T) {
	full := sidecarCred{
		ID: "c1", Provider: "OPENAI_COMPAT", Token: "tok", Priority: 3,
		LeaseExpiresAt: "2026-01-01T00:00:00Z",
		BaseURL:        "https://llm.example/v1",
		Headers:        map[string]string{"X-Org": "acme"},
	}
	blob, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]bool{
		"id": true, "provider": true, "token": true, "priority": true,
		"lease_expires_at": true, "base_url": true, "headers": true,
	}
	for k := range got {
		if !want[k] {
			t.Errorf("boot payload carries an unexpected key %q — the sidecar will ignore it", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("boot payload is missing key %q; the sidecar reads it and will see a zero value", k)
	}
}
