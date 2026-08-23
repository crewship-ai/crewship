package api

import (
	"database/sql"
	"testing"
)

// The endpoint gate on the two mutation paths that bypassed it.
//
// Create has always run an endpoint value through validateEndpointURL. Rotate
// and Update did not — Rotate keyed on type == ENDPOINT_URL, and Update had no
// endpoint case at all. Phase 2 introduces a second credential shape that stores
// an endpoint (API_KEY + a provider whose upstream comes from the credential),
// so both holes become reachable through a supported provider.

// TestValidateCredentialUpdate_EndpointValueIsValidated pins the PATCH gate. The
// link-local case is the one that matters: 169.254.169.254 is the cloud metadata
// service, and the sidecar dials whatever this value holds.
func TestValidateCredentialUpdate_EndpointValueIsValidated(t *testing.T) {
	tests := []struct {
		name            string
		body            map[string]interface{}
		currentType     string
		currentProvider string
		wantErr         bool
	}{
		{
			name:        "ENDPOINT_URL patched to the metadata service is refused",
			body:        map[string]interface{}{"value": "http://169.254.169.254/v1"},
			currentType: CredTypeEndpointURL,
			wantErr:     true,
		},
		{
			name:        "ENDPOINT_URL patched to a valid endpoint is accepted",
			body:        map[string]interface{}{"value": "https://llm.example.com/v1"},
			currentType: CredTypeEndpointURL,
			wantErr:     false,
		},
		{
			name:        "ENDPOINT_URL patched to a non-URL is refused",
			body:        map[string]interface{}{"value": "not-a-url"},
			currentType: CredTypeEndpointURL,
			wantErr:     true,
		},
		{
			name:            "an endpoint-backed API_KEY is gated by its provider",
			body:            map[string]interface{}{"value": "http://169.254.169.254/v1"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI_COMPAT",
			wantErr:         true,
		},
		{
			name:            "an endpoint-backed API_KEY accepts the object shape",
			body:            map[string]interface{}{"value": `{"baseURL":"https://llm.example.com/v1","apiKey":"sk-x"}`},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI_COMPAT",
			wantErr:         false,
		},
		{
			// The merged-provider case: one PATCH both switches the provider and
			// writes the value. Judged against the stored provider, neither half
			// would ever meet the gate.
			name: "switching provider and value in one patch is gated by the new provider",
			body: map[string]interface{}{
				"provider": "OPENAI_COMPAT",
				"value":    "http://169.254.169.254/v1",
			},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI",
			wantErr:         true,
		},
		{
			// A fixed-upstream provider's value is an opaque key and must stay
			// opaque — gating it would reject every real OpenRouter key.
			name:            "a fixed-upstream provider's key is not an endpoint",
			body:            map[string]interface{}{"value": "sk-or-v1-abcdef"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENROUTER",
			wantErr:         false,
		},
		{
			name:            "a plain API_KEY is untouched by the gate",
			body:            map[string]interface{}{"value": "ghp_sometoken"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "GITHUB",
			wantErr:         false,
		},
		{
			// A PATCH that does not write a value must not be failed by a stored
			// value it is not changing.
			name:            "renaming an endpoint credential sends no value and is allowed",
			body:            map[string]interface{}{"name": "renamed"},
			currentType:     CredTypeEndpointURL,
			currentProvider: "",
			wantErr:         false,
		},
		{
			name:            "a non-string provider is rejected outright",
			body:            map[string]interface{}{"provider": 123},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI",
			wantErr:         true,
		},
		{
			// Crossing the boundary with no value re-interprets bytes that are
			// already stored. This direction leaves a bare key under a provider
			// whose loader expects an object: every delivery path `continue`s
			// past it, so the credential is saved and reaches no agent, with
			// only a server-side log to say so.
			name:            "switching TO an endpoint provider without a value is refused",
			body:            map[string]interface{}{"provider": "OPENAI_COMPAT"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI",
			wantErr:         true,
		},
		{
			// And this direction is the leak: the stored value IS the object,
			// nothing splits it any more, and the whole blob — base URL, custom
			// headers and key — goes upstream in an Authorization header.
			name:            "switching AWAY from an endpoint provider without a value is refused",
			body:            map[string]interface{}{"provider": "OPENAI"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI_COMPAT",
			wantErr:         true,
		},
		{
			// Not every provider patch crosses the boundary. One that does not
			// must stay a cheap metadata edit.
			name:            "switching between two fixed-upstream providers needs no value",
			body:            map[string]interface{}{"provider": "OPENROUTER"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI",
			wantErr:         false,
		},
		{
			name:            "renaming an endpoint-backed credential still needs no value",
			body:            map[string]interface{}{"name": "renamed"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI_COMPAT",
			wantErr:         false,
		},
		{
			// TYPE crosses the boundary too, and the first version of this
			// guard asked only about the provider. ENDPOINT_URL -> API_KEY with
			// the provider untouched turns the stored object into "an opaque
			// API key", and downstream consumers then send the whole thing —
			// base URL, custom headers and secret — in an auth header.
			name:            "ENDPOINT_URL to API_KEY without a value is refused",
			body:            map[string]interface{}{"type": string(CredTypeAPIKey)},
			currentType:     CredTypeEndpointURL,
			currentProvider: "OPENAI",
			wantErr:         true,
		},
		{
			name:            "API_KEY to ENDPOINT_URL without a value is refused",
			body:            map[string]interface{}{"type": CredTypeEndpointURL},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "OPENAI",
			wantErr:         true,
		},
		{
			// Both coordinates already store the object, so this crosses
			// nothing and must stay a cheap metadata edit.
			name:            "ENDPOINT_URL to an endpoint-backed API_KEY needs no value",
			body:            map[string]interface{}{"type": string(CredTypeAPIKey), "provider": "OPENAI_COMPAT"},
			currentType:     CredTypeEndpointURL,
			currentProvider: "OPENAI",
			wantErr:         false,
		},
		{
			name:            "a type change that crosses nothing needs no value",
			body:            map[string]interface{}{"type": "SECRET"},
			currentType:     string(CredTypeAPIKey),
			currentProvider: "GITHUB",
			wantErr:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validateCredentialUpdate(tt.body, tt.currentType, sql.NullString{}, tt.currentProvider)
			if tt.wantErr && msg == "" {
				t.Errorf("validateCredentialUpdate accepted the patch; want a 400 message")
			}
			if !tt.wantErr && msg != "" {
				t.Errorf("validateCredentialUpdate rejected a valid patch: %q", msg)
			}
		})
	}
}

// TestRotateEndpointGate_CoversEndpointBackedProviders pins the predicate the
// rotate handler now uses. Keying on type alone sent an API_KEY + OPENAI_COMPAT
// credential down the opaque path, where a full-value rotate replaced the whole
// {baseURL,apiKey,headers} object with a bare key and left it routing nowhere.
func TestRotateEndpointGate_CoversEndpointBackedProviders(t *testing.T) {
	tests := []struct {
		name         string
		credType     string
		credProvider string
		wantEndpoint bool
	}{
		{"ENDPOINT_URL by type", CredTypeEndpointURL, "", true},
		{"ENDPOINT_URL keeps its behaviour whatever the provider", CredTypeEndpointURL, "OPENAI", true},
		{"API_KEY + OPENAI_COMPAT stores an endpoint object", string(CredTypeAPIKey), "OPENAI_COMPAT", true},
		// The provider column is free text a human types, not a wire value. The
		// dashboard and any REST client send it verbatim; only the CLI folded
		// case. A mis-cased provider that missed this predicate stored fine,
		// skipped the endpoint gate, skipped the delivery-time split and was
		// then dropped from the CredStore — three silent no-ops and no error.
		{"API_KEY + lower-case openai_compat is still an endpoint", string(CredTypeAPIKey), "openai_compat", true},
		{"API_KEY + mixed-case OpenAI_Compat is still an endpoint", string(CredTypeAPIKey), "OpenAI_Compat", true},
		{"API_KEY + surrounding whitespace is still an endpoint", string(CredTypeAPIKey), "  OPENAI_COMPAT  ", true},
		{"API_KEY + OPENROUTER is an opaque key", string(CredTypeAPIKey), "OPENROUTER", false},
		{"API_KEY + GITHUB is an opaque key", string(CredTypeAPIKey), "GITHUB", false},
		{"SECRET with no provider is opaque", "SECRET", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.credType == CredTypeEndpointURL || providerNeedsEndpointValue(tt.credProvider)
			if got != tt.wantEndpoint {
				t.Errorf("endpoint-shaped = %v, want %v", got, tt.wantEndpoint)
			}
		})
	}
}
