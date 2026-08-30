package api

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

// TestSidecarOpenAICompatProvider_IsTheEndpointDescriptor pins the local const
// against the descriptor table. A rename in llmroute would otherwise leave this
// package delivering a credential under a provider the sidecar has no route
// for — it would compile, boot, and 503 on the first call.
func TestSidecarOpenAICompatProvider_IsTheEndpointDescriptor(t *testing.T) {
	spec, ok := llmroute.Lookup(sidecarOpenAICompatProvider)
	if !ok {
		t.Fatalf("llmroute has no spec %q — the provider a resolved endpoint is "+
			"delivered under does not exist", sidecarOpenAICompatProvider)
	}
	if !spec.UpstreamFromCredential {
		t.Errorf("spec %q does not take its upstream from the credential; it is the "+
			"wrong descriptor for a BYO endpoint", spec.ID)
	}
	if !spec.RequireCredential {
		t.Errorf("spec %q does not require a credential — a nil credential would be "+
			"forwarded to no upstream at all", spec.ID)
	}
	if !providerNeedsEndpointValue(sidecarOpenAICompatProvider) {
		t.Errorf("providerNeedsEndpointValue(%q) is false, so the stored "+
			"{baseURL,apiKey,headers} object would travel as the bearer token",
			sidecarOpenAICompatProvider)
	}
}

// TestAppendProxiedEndpointCredential covers the delivery gate WS5's routing
// decision depends on: the orchestrator routes the local endpoint through the
// sidecar whenever it carries auth material, and 503s if no credential is
// waiting there.
func TestAppendProxiedEndpointCredential(t *testing.T) {
	h := &InternalHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	tests := []struct {
		name      string
		existing  []mcpCredEntry
		endpoint  localModelEndpoint
		wantAdded bool
		wantValue string
		wantBase  string
	}{
		{
			name:      "no endpoint at all",
			endpoint:  localModelEndpoint{},
			wantAdded: false,
		},
		{
			name: "plain ollama carries no secret and stays on its current path",
			// OPENAI_COMPAT is RequireCredential; routing an unauthenticated
			// endpoint would put a 503 in front of a path that works today.
			endpoint:  localModelEndpoint{BaseURL: "http://127.0.0.1:11434/v1"},
			wantAdded: false,
		},
		{
			name:      "an authenticated endpoint is delivered",
			endpoint:  localModelEndpoint{BaseURL: "https://llm.internal.example/v1", APIKey: "sk-tenant-secret"},
			wantAdded: true,
			wantValue: "sk-tenant-secret",
			wantBase:  "https://llm.internal.example/v1",
		},
		{
			// Headers-only IS delivered now. It used not to be, because
			// llmroute.ApplyAuth dropped custom headers on an empty token — so
			// the credential stayed in the agent's OpenCode config, readable by
			// the agent, which is the exposure this delivery exists to close.
			// ApplyAuth writes them independently of the token now.
			name:      "an endpoint authenticated only by custom headers is delivered",
			endpoint:  localModelEndpoint{BaseURL: "https://llm.internal.example/v1", Headers: map[string]string{"X-Api-Key": "header-only-secret"}},
			wantAdded: true,
			wantValue: "",
			wantBase:  "https://llm.internal.example/v1",
		},
		{
			// Still not delivered: no auth material at all. There is nothing to
			// isolate, and OPENAI_COMPAT is RequireCredential, so routing a bare
			// endpoint would put a 503 in front of a path that works today.
			name:      "a bare endpoint with no auth of any kind stays unrouted",
			endpoint:  localModelEndpoint{BaseURL: "http://127.0.0.1:11434/v1"},
			wantAdded: false,
		},
		{
			name:      "a token plus headers is delivered whole",
			endpoint:  localModelEndpoint{BaseURL: "https://llm.internal.example/v1", APIKey: "sk-tenant-secret", Headers: map[string]string{"X-Org": "acme"}},
			wantAdded: true,
			wantValue: "sk-tenant-secret",
			wantBase:  "https://llm.internal.example/v1",
		},
		{
			name: "an assigned compat credential wins; only one is ever delivered",
			existing: []mcpCredEntry{
				{ID: "c1", Provider: "OPENAI_COMPAT", BaseURL: "https://other.example/v1", Value: "sk-other"},
			},
			endpoint:  localModelEndpoint{BaseURL: "https://llm.internal.example/v1", APIKey: "sk-tenant-secret"},
			wantAdded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, added := h.appendProxiedEndpointCredential(tt.existing, tt.endpoint)
			if added != tt.wantAdded {
				t.Fatalf("added = %v, want %v", added, tt.wantAdded)
			}
			if !added {
				if len(got) != len(tt.existing) {
					t.Errorf("credential list changed despite added=false")
				}
				return
			}
			last := got[len(got)-1]
			if last.Provider != sidecarOpenAICompatProvider {
				t.Errorf("provider = %q, want %q", last.Provider, sidecarOpenAICompatProvider)
			}
			if last.Value != tt.wantValue {
				t.Errorf("value = %q, want %q", last.Value, tt.wantValue)
			}
			if last.BaseURL != tt.wantBase {
				t.Errorf("base_url = %q, want %q", last.BaseURL, tt.wantBase)
			}
			// The whole point of the entry: it must reach the CredStore and
			// nothing else. Every path that writes a credential into the agent
			// environment or its /secrets files first requires a non-empty
			// env-var name.
			if last.EnvVar != "" {
				t.Errorf("env_var = %q, want empty — a named entry would put the "+
					"endpoint key back into the agent container, which is the "+
					"leak this delivery exists to close", last.EnvVar)
			}
		})
	}
}

// TestAgentConfig_EndpointJSONNeverTravelsAsToken is the load-bearing property
// of the split: Value is what the sidecar injects as a bearer token, so the
// stored {baseURL,apiKey,headers} object must never survive into it.
func TestAgentConfig_EndpointJSONNeverTravelsAsToken(t *testing.T) {
	const stored = `{"baseURL":"https://llm.internal.example/v1","apiKey":"sk-real-key","headers":{"X-Org":"acme"}}`

	token, baseURL, headers, err := providerEndpointFromValue(sidecarOpenAICompatProvider, stored)
	if err != nil {
		t.Fatalf("providerEndpointFromValue: %v", err)
	}
	if token != "sk-real-key" {
		t.Errorf("token = %q, want the bare api key", token)
	}
	if baseURL != "https://llm.internal.example/v1" {
		t.Errorf("baseURL = %q", baseURL)
	}
	if headers["X-Org"] != "acme" {
		t.Errorf("headers = %v", headers)
	}

	// A fixed-upstream provider's value is opaque and must pass through
	// verbatim, or every existing credential's boot payload changes.
	tok, base, hdrs, err := providerEndpointFromValue("OPENROUTER", "sk-or-v1-plain")
	if err != nil {
		t.Fatalf("providerEndpointFromValue(OPENROUTER): %v", err)
	}
	if tok != "sk-or-v1-plain" || base != "" || hdrs != nil {
		t.Errorf("a fixed-upstream provider's value was rewritten: token=%q base=%q headers=%v", tok, base, hdrs)
	}
}

// TestEveryCredentialLoader_SplitsTheEndpointObject is a source-level guard over
// the three loaders that build a credential for a run.
//
// All three now carry deliveredCredential.Provider through to the orchestrator,
// and the orchestrator hands PlainValue to the sidecar as a bearer token. So any
// loader that sets Provider MUST also split an endpoint-backed value — otherwise
// the stored {baseURL,apiKey,headers} object is sent upstream as the secret,
// leaking the endpoint and every custom header into an Authorization header.
//
// Asserted against the source rather than by driving three handlers because the
// property is "no loader forgot", and a fourth loader added later is exactly the
// case a hand-written list of three would miss.
func TestEveryCredentialLoader_SplitsTheEndpointObject(t *testing.T) {
	// DERIVED, not listed. The previous version of this test walked a
	// hand-written list of three files while its own comment said "a fourth
	// loader added later is exactly the case a hand-written list of three would
	// miss". Two such loaders existed at the time — gov_model_credential_lookup.go
	// and keeper_aux_credential.go — and both shipped the leak this guard was
	// written to prevent, sending {baseURL,apiKey,headers} to api.anthropic.com
	// as an x-api-key value.
	//
	// So: find every file in the REPOSITORY that decrypts a credential, and
	// require each one to either split the value or be classified below with a
	// reason. A new decrypting file fails this test until someone decides which
	// it is, which is the only version of this guard that can catch the next one.
	//
	// Repository-wide, not package-wide, for the same reason it is derived rather
	// than listed. The previous version walked os.ReadDir(".") — internal/api
	// alone — while claiming to find "every file that decrypts a credential". It
	// missed internal/pipeline/credential_resolver.go, which selects an http
	// step's credential by TYPE and hands the decrypted value straight to an
	// Authorization header: an OPENAI_COMPAT row is stored as API_KEY, so
	// creating one made it the newest match and the whole {baseURL,apiKey,
	// headers} object would have been posted to whatever third-party endpoint
	// the routine dials. Same leak, different package, invisible to a guard
	// scoped to one directory.
	root := repoRoot(t)
	files, err := repoGoFiles(root)
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	// notUpstreamDelivery names files that decrypt a credential but never hand
	// the value to a third party as an auth credential. Each needs a reason: the
	// point is that adding a name here is a decision someone made, not a way to
	// silence the test.
	notUpstreamDelivery := map[string]string{
		"internal/api/admin_reencrypt.go":          "re-encrypts at rest; the plaintext never leaves the process",
		"internal/api/credentials.go":              "CRUD over the row itself",
		"internal/api/credentials_reveal.go":       "shows the value to an authorised human, which is the whole point of the endpoint",
		"internal/api/credential_rotation.go":      "writes a new value; splits where it must",
		"internal/api/credential_delivery_crew.go": "resolves which credential a crew gets, not what is sent",
		"internal/api/composio_handler.go":         "third-party connector with its own token shape",
		"internal/api/crew_ai.go":                  "reads a key for a first-party LLM call, no endpoint-backed provider reaches it",
		"internal/api/internal_credentials.go":     "sidecar boot payload; the sidecar splits on its own side",
		"internal/api/internal_mcp.go":             "MCP server credential, not an LLM provider",
		"internal/api/internal_handler.go":         "dispatch only",
		"internal/api/escalation_waiter.go":        "reads a notification credential",
		"internal/api/issue_code_links.go":         "forge token, not an LLM provider",
		"internal/api/local_model_endpoint.go":     "reads ENDPOINT_URL rows, which are already the split shape",
		"internal/api/models.go":                   "lists models with a provider key",
		"internal/api/oauth_creds.go":              "OAuth token storage",
		"internal/api/oauth_flow.go":               "OAuth exchange",
		"internal/api/oauth_token.go":              "OAuth refresh",
		"internal/api/skills_generate.go":          "first-party LLM call",
		// Verified by reading probeProviderInner: it switches on PROVIDER and
		// has no OPENAI_COMPAT arm, so an endpoint-backed credential falls to
		// default:, fails the ENDPOINT_URL check and returns
		// probeNoValidationMsg WITHOUT dialling. Nothing is sent, so nothing
		// leaks — but see #2043: that also means the one provider where a
		// connectivity test matters most cannot be tested from this endpoint.
		"internal/api/credentials_test_endpoint.go": "probes by provider arm; endpoint-backed providers hit default: and are never dialled",

		// Outside internal/api — the half this guard could not see before.
		"internal/backup/keyring.go":                                       "backup keyring material, not a vault credential",
		"internal/database/migrate_consts_v140_encrypt_webhook_secrets.go": "one-shot migration re-encrypting webhook secrets at rest",
		"internal/keeper/secrets/store.go":                                 "the secret store itself; splitting here would be splitting twice",
		"internal/notify/channels.go":                                      "notification channel token (Slack, email), never an LLM provider",
		// Pins provider = 'ANTHROPIC' in SQL, so an endpoint-backed row can
		// never be selected. Verified in anthropicLLMCredentialFilter.
		"internal/pipeline/runner_llm.go": "selects on provider = 'ANTHROPIC' explicitly; no endpoint-backed row can match",
	}

	decryptMarkers := []string{"decryptCredential(", "encryption.Decrypt("}
	// Any of these means the file has reckoned with the endpoint shape. The list
	// spans packages deliberately: internal/api splits with
	// providerEndpointFromValue, while packages that cannot reach an unexported
	// helper ask the route table directly or exclude endpoint-backed rows from
	// their query.
	awareMarkers := []string{
		"providerEndpointFromValue",
		"providerNeedsEndpointValue",
		"llmroute.ProviderCarriesUpstream",
		"excludeEndpointProviders",
	}

	var checked int
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(src)
		if !containsAny(text, decryptMarkers) {
			continue
		}
		checked++
		if _, waived := notUpstreamDelivery[rel]; waived {
			continue
		}
		if !containsAny(text, awareMarkers) {
			t.Errorf("%s decrypts a credential but never splits an endpoint-backed value.\n"+
				"An API_KEY row can hold {baseURL,apiKey,headers} for a provider that carries its own "+
				"endpoint, so handing the decrypted value straight to a provider constructor sends the "+
				"operator's secret, base URL and custom headers upstream as the credential.\n"+
				"Either split it (providerEndpointFromValue in this package, llmroute.ProviderCarriesUpstream "+
				"elsewhere), or add %q to notUpstreamDelivery with the reason it never delivers a value to a "+
				"third party.", rel, rel)
		}
	}
	if checked < 25 {
		t.Fatalf("only %d decrypting files found across the repo — the marker or the walk changed and this guard has gone vacuous", checked)
	}
	// A waiver for a file that no longer decrypts anything is dead weight that
	// makes the map look more considered than it is — and the name is then free
	// to be re-used by a file that DOES need checking. ("webhook.go" was one:
	// it sat in this map naming a file that had not carried the marker for some
	// time.)
	for rel := range notUpstreamDelivery {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("waived file %q does not exist; remove it from notUpstreamDelivery", rel)
			continue
		}
		if !containsAny(string(src), decryptMarkers) {
			t.Errorf("waived file %q no longer decrypts a credential; remove it from notUpstreamDelivery", rel)
		}
	}
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// The collision warning used to write both base URLs verbatim. An endpoint URL
// is not a secret, but the gate it passes through does not make it free of one
// either: validateEndpointURL rejects user:pass@host and says nothing about the
// query string, so https://gw.example/v1?api-key=SECRET stores cleanly and the
// log line carried it to an audience that was never granted the credential.
//
// CodeQL flagged the dataflow (go/clear-text-logging, high) on the PR that added
// the line. This asserts the fix at the level that matters — what actually
// reaches the log writer — rather than that a helper exists.
func TestAppendProxiedEndpointCredential_CollisionLogLeaksNoSecret(t *testing.T) {
	var buf strings.Builder
	h := &InternalHandler{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	const assignedSecret = "assigned-key-in-the-query-9f2c"
	const resolvedSecret = "resolved-key-in-the-query-4a1b"

	_, added := h.appendProxiedEndpointCredential(
		[]mcpCredEntry{{
			ID:       "cred-assigned",
			Provider: sidecarOpenAICompatProvider,
			BaseURL:  "https://gw.acme.example/v1?api-key=" + assignedSecret,
		}},
		localModelEndpoint{
			BaseURL: "https://ollama.box.internal:11434/v1?token=" + resolvedSecret,
			APIKey:  "sk-local-box",
		},
	)
	if added {
		t.Fatal("an assigned OPENAI_COMPAT credential must win; nothing should be appended")
	}

	logged := buf.String()
	if logged == "" {
		t.Fatal("the collision produced no log line at all — the warning is the only signal an operator gets")
	}
	for _, secret := range []string{assignedSecret, resolvedSecret, "sk-local-box"} {
		if strings.Contains(logged, secret) {
			t.Errorf("secret %q reached the log:\n%s", secret, logged)
		}
	}
	// Still actionable: both hosts must be there, or the line says a collision
	// happened without saying between what.
	for _, host := range []string{"gw.acme.example", "ollama.box.internal:11434"} {
		if !strings.Contains(logged, host) {
			t.Errorf("host %q missing from the log; the warning is no longer diagnosable:\n%s", host, logged)
		}
	}
}

func TestEndpointHostForLog(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://gw.acme.example/v1", "gw.acme.example"},
		{"http://ollama.box.internal:11434/v1", "ollama.box.internal:11434"},
		{"https://gw.acme.example/v1?api-key=SECRET", "gw.acme.example"},
		// Rejected at the storage gate, but a row predating it must not leak here.
		{"https://user:hunter2@gw.acme.example/v1", "gw.acme.example"},
		{"  https://gw.acme.example/v1  ", "gw.acme.example"},
		// The fallback must never be "return the input" — that is how redaction
		// helpers leak the one value they were written to hide.
		{"://not a url?api-key=SECRET", "<unparseable>"},
		{"not-a-url", "<unparseable>"},
		{"", "<unparseable>"},
	}
	for _, tt := range tests {
		if got := endpointHostForLog(tt.in); got != tt.want {
			t.Errorf("endpointHostForLog(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
