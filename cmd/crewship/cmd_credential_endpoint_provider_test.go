package main

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

// The unit half of `credential create --base-url`: which providers carry their
// own endpoint, and what the stored credential object looks like when one does.
// The acceptance half (the built binary against a stub server) is in
// acceptance_credential_openrouter_test.go.

func TestRouteEndpointProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		wantID   string // "" = not an endpoint-carrying provider
	}{
		{name: "canonical id", provider: "OPENAI_COMPAT", wantID: "OPENAI_COMPAT"},
		{name: "lower case, as an operator types it", provider: "openai_compat", wantID: "OPENAI_COMPAT"},
		{name: "surrounding whitespace", provider: "  OpenAI_Compat  ", wantID: "OPENAI_COMPAT"},
		// A fixed-upstream provider must NOT be treated as endpoint-carrying:
		// the sidecar already knows where openrouter.ai is, and accepting a
		// --base-url for it would store a URL nothing reads.
		{name: "fixed upstream", provider: "OPENROUTER"},
		{name: "built-in", provider: "ANTHROPIC"},
		{name: "not an LLM provider at all", provider: "GITHUB"},
		{name: "unset", provider: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := routeEndpointProvider(tc.provider)
			if ok != (tc.wantID != "") {
				t.Fatalf("routeEndpointProvider(%q) ok = %v, want %v", tc.provider, ok, tc.wantID != "")
			}
			if ok && spec.ID != tc.wantID {
				t.Errorf("routeEndpointProvider(%q) id = %q, want %q", tc.provider, spec.ID, tc.wantID)
			}
			// The id the CLI stores is the registry's spelling, because the
			// sidecar looks the provider column up case-sensitively.
			if ok && !spec.UpstreamFromCredential {
				t.Errorf("routeEndpointProvider(%q) returned a spec whose upstream is not from the credential", tc.provider)
			}
		})
	}
}

// The error message for a rejected --base-url names the providers it WOULD
// have been valid for, so it is derived from the table rather than typed out.
// Derived, therefore it must not be allowed to go vacuous.
func TestRouteEndpointProviderIDs_DerivedFromTheTable(t *testing.T) {
	t.Parallel()

	want := []string{}
	for _, s := range llmroute.Specs() {
		if s.UpstreamFromCredential {
			want = append(want, s.ID)
		}
	}
	if len(want) == 0 {
		t.Fatal("no llmroute spec sets UpstreamFromCredential; --base-url has nothing to be valid for")
	}
	got := routeEndpointProviderIDs()
	if len(got) != len(want) {
		t.Fatalf("routeEndpointProviderIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("routeEndpointProviderIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The type gate exists because credpolicy resolves DELIVERY by type: an
// endpoint credential filed as SECRET reaches the agent's environment instead
// of the sidecar, which is the leak the provider exists to close.
func TestEndpointCredentialTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		credType string
		want     bool
	}{
		{"API_KEY", true},
		{"ENDPOINT_URL", true},
		{"SECRET", false},
		{"CLI_TOKEN", false},
		{"AI_CLI_TOKEN", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := endpointCredentialTypes[tc.credType]; got != tc.want {
			t.Errorf("endpointCredentialTypes[%q] = %v, want %v", tc.credType, got, tc.want)
		}
	}
}

// The stored object for a credential-supplied endpoint is the SAME shape #961
// defined for ENDPOINT_URL — that is why no new column, table or credential
// type was needed. This pins the three keys the server parses.
func TestBuildEndpointCredentialValue_CompatObjectShape(t *testing.T) {
	t.Parallel()

	raw, err := buildEndpointCredentialValue("https://llm.internal.example/v1", "sk-internal-key", []string{"X-Org=acme"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var obj struct {
		BaseURL string            `json:"baseURL"`
		APIKey  string            `json:"apiKey"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("stored value is not the endpoint object: %v (%s)", err, raw)
	}
	if obj.BaseURL != "https://llm.internal.example/v1" {
		t.Errorf("baseURL = %q", obj.BaseURL)
	}
	if obj.APIKey != "sk-internal-key" {
		t.Errorf("apiKey = %q", obj.APIKey)
	}
	if obj.Headers["X-Org"] != "acme" {
		t.Errorf("headers = %v", obj.Headers)
	}
}
