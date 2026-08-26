package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// Auth material for a credential-supplied endpoint comes in three shapes, and
// the CLI has to be able to produce all three: a bearer token, custom headers,
// or both. llmroute.ApplyAuth writes `extra` headers whether or not a token is
// set, so an endpoint that authenticates purely on `X-Api-Key: …` is routed by
// the sidecar exactly like a bearer one — but `credential create` rejected it
// before buildEndpointCredentialValue ever saw the headers, which left the one
// shape the rest of the stack had just been taught to handle as the one shape
// no operator could create.
//
// Driven through the built binary, like the rest of the credential acceptance
// coverage: cli_route_contract_test.go skips every command that reaches the API
// through api_helpers.go, so the wire body is only asserted here.
//
// The fourth shape — no auth material at all — must still be refused with exit
// 2; that row lives in TestAcceptance_CredentialCreate_EndpointFlagRejections
// alongside the other local validation failures.

// A placeholder, deliberately not key-shaped: the pre-commit gitleaks hook
// flags fixtures that look like real secrets.
const headerOnlyEndpointSecret = "endpoint-secret-EXAMPLE-NOT-A-REAL-KEY"

func TestAcceptance_CredentialCreate_EndpointAuthMaterialForms(t *testing.T) {
	const baseURL = "https://llm.internal.example/v1"

	tests := []struct {
		name string
		args []string
		// wantAPIKey "" means the apiKey key must be ABSENT, not empty: a
		// credential carrying `"apiKey":""` reads to the sidecar as a token it
		// should send, and an empty Authorization header is a worse failure
		// than none at all.
		wantAPIKey  string
		wantHeaders map[string]string // nil = the headers key must be absent
	}{
		{
			name: "headers only, no bearer token",
			args: []string{
				"--name", "header-auth-llm", "--type", "API_KEY", "--provider", "OPENAI_COMPAT",
				"--base-url", baseURL,
				"--header", "X-Api-Key=" + headerOnlyEndpointSecret,
			},
			wantHeaders: map[string]string{"X-Api-Key": headerOnlyEndpointSecret},
		},
		{
			name: "bearer token only",
			args: []string{
				"--name", "token-auth-llm", "--type", "API_KEY", "--provider", "OPENAI_COMPAT",
				"--base-url", baseURL,
				"--auth-token", headerOnlyEndpointSecret,
			},
			wantAPIKey: headerOnlyEndpointSecret,
		},
		{
			name: "token and headers together",
			args: []string{
				"--name", "both-auth-llm", "--type", "API_KEY", "--provider", "OPENAI_COMPAT",
				"--base-url", baseURL,
				"--auth-token", headerOnlyEndpointSecret,
				"--header", "X-Org=acme",
			},
			wantAPIKey:  headerOnlyEndpointSecret,
			wantHeaders: map[string]string{"X-Org": "acme"},
		},
		{
			// The #961 arm stores the same object from a different flag shape
			// (--value carries the URL, there is no --provider). It already
			// accepted headers alone; pinned here so the two paths cannot drift
			// into disagreeing about what a header-only endpoint looks like.
			name: "headers only on a plain ENDPOINT_URL credential",
			args: []string{
				"--name", "header-auth-endpoint", "--type", "ENDPOINT_URL",
				"--value", baseURL,
				"--header", "X-Api-Key=" + headerOnlyEndpointSecret,
			},
			wantHeaders: map[string]string{"X-Api-Key": headerOnlyEndpointSecret},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &credStubServer{}
			srv := stub.start(t)
			cfg := credStubConfig(t, srv.URL)

			out, err := runCredCLI(t, cfg, append([]string{"credential", "create"}, tc.args...)...)
			if err != nil {
				t.Fatalf("create: %v\noutput: %s", err, out)
			}

			value, _ := stub.createdBody(t)["value"].(string)
			var obj map[string]any
			if err := json.Unmarshal([]byte(value), &obj); err != nil {
				t.Fatalf("stored value is not the endpoint object: %v (%q)", err, value)
			}
			if obj["baseURL"] != baseURL {
				t.Errorf("baseURL = %v, want %q", obj["baseURL"], baseURL)
			}

			if tc.wantAPIKey == "" {
				if got, ok := obj["apiKey"]; ok {
					t.Errorf("apiKey = %v is present on a credential with no bearer token", got)
				}
			} else if obj["apiKey"] != tc.wantAPIKey {
				t.Errorf("apiKey = %v, want %q", obj["apiKey"], tc.wantAPIKey)
			}

			if tc.wantHeaders == nil {
				if got, ok := obj["headers"]; ok {
					t.Errorf("headers = %v is present on a credential with no --header", got)
				}
				return
			}
			headers, ok := obj["headers"].(map[string]any)
			if !ok {
				t.Fatalf("headers = %v, want an object", obj["headers"])
			}
			if len(headers) != len(tc.wantHeaders) {
				t.Errorf("headers = %v, want %v", headers, tc.wantHeaders)
			}
			for k, want := range tc.wantHeaders {
				if headers[k] != want {
					t.Errorf("headers[%q] = %v, want %q", k, headers[k], want)
				}
			}
		})
	}
}

// The unit half: buildEndpointCredentialValue is what decides which keys the
// stored object carries, and "no token" must mean the key is missing rather
// than present-and-empty.
func TestBuildEndpointCredentialValue_HeadersWithoutToken(t *testing.T) {
	t.Parallel()

	raw, err := buildEndpointCredentialValue("https://llm.internal.example/v1", "",
		[]string{"X-Api-Key=" + headerOnlyEndpointSecret})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("not the endpoint object: %v (%s)", err, raw)
	}
	if _, ok := obj["apiKey"]; ok {
		t.Errorf("apiKey key is present with no token: %s", raw)
	}
	headers, _ := obj["headers"].(map[string]any)
	if headers["X-Api-Key"] != headerOnlyEndpointSecret {
		t.Errorf("headers = %v", obj["headers"])
	}
}

// Empty stdin must not clear a stored token.
//
// --value-stdin fails closed when it reads nothing; --auth-token-stdin did not,
// and the difference had a silent destructive outcome: a pipe that produced no
// bytes (a mistyped variable, a command that failed upstream) sent an empty
// token, the server merged it over the stored one, and the credential was left
// authenticating with nothing. The failure would surface later as a 401 from the
// endpoint, which blames the key rather than the rotate that emptied it.
func TestReadAuthToken_EmptyStdinIsRefused(t *testing.T) {
	t.Run("empty stdin is an error, not a clear", func(t *testing.T) {
		withStdin(t, "", func() {
			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			flags.String("auth-token", "", "")
			flags.Bool("auth-token-stdin", false, "")
			if err := flags.Set("auth-token-stdin", "true"); err != nil {
				t.Fatal(err)
			}

			_, changed, err := readAuthToken(flags)
			if err == nil {
				t.Fatalf("readAuthToken accepted empty stdin (changed=%v); an empty token would be merged over the stored one", changed)
			}
			if !strings.Contains(err.Error(), `--auth-token ""`) {
				t.Errorf("error %q must name the explicit way to clear a token", err)
			}
		})
	})

	t.Run("a real token from stdin still works", func(t *testing.T) {
		withStdin(t, "endpoint-secret-EXAMPLE-NOT-A-REAL-KEY\n", func() {
			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			flags.String("auth-token", "", "")
			flags.Bool("auth-token-stdin", false, "")
			if err := flags.Set("auth-token-stdin", "true"); err != nil {
				t.Fatal(err)
			}

			tok, changed, err := readAuthToken(flags)
			if err != nil {
				t.Fatalf("readAuthToken: %v", err)
			}
			if tok != "endpoint-secret-EXAMPLE-NOT-A-REAL-KEY" || !changed {
				t.Errorf("got (%q, %v), want the token and changed=true", tok, changed)
			}
		})
	})

	t.Run("clearing deliberately still works through the flag", func(t *testing.T) {
		flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
		flags.String("auth-token", "", "")
		flags.Bool("auth-token-stdin", false, "")
		if err := flags.Set("auth-token", ""); err != nil {
			t.Fatal(err)
		}

		tok, changed, err := readAuthToken(flags)
		if err != nil {
			t.Fatalf("readAuthToken: %v", err)
		}
		if tok != "" || !changed {
			t.Errorf("got (%q, %v), want (\"\", true) — an explicit empty flag is still a clear", tok, changed)
		}
	})
}
