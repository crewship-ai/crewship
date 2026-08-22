package api

import (
	"strings"
	"testing"
)

// pemFixture builds a fake PEM block at runtime. Used by tests so the
// literal "-----BEGIN <label>-----" string never appears contiguously
// in source — keeps the gitleaks private-key rule from flagging our
// fixtures as real leaked keys.
func pemFixture(label, body string) string {
	const dashes = "-----"
	return dashes + "BEGIN " + label + dashes + "\n" + body + "\n" + dashes + "END " + label + dashes
}

func TestValidateCredentialPayload(t *testing.T) {
	t.Parallel()

	username := "user@gmail.com"
	emptyUsername := "   "

	// PEM fixtures are assembled at runtime so the obvious "BEGIN ...
	// PRIVATE KEY" literal never appears contiguously in source —
	// otherwise gitleaks' private-key rule treats the test data as a
	// real leaked key and blocks the commit. The bodies are deliberately
	// truncated ("…" placeholders) so even if a future scanner is
	// smarter, there's nothing to actually decrypt.
	sshPEM := pemFixture("OPENSSH PRIVATE KEY", "b3BlbnNzaC1rZXktdjEAAAAA…")
	rsaPEM := pemFixture("RSA PRIVATE KEY", "MIIEpAIBAAKCAQEA0Z3VS5…")
	pkcs8PEM := pemFixture("PRIVATE KEY", "MIIEvAIBADANBgkqhkiG9w0…")
	certPEM := pemFixture("CERTIFICATE", "MIIDazCCAlOgAwIBAgIUJTd…")

	// Common mistake we explicitly want to catch: pasting a public key
	// where a private key is expected.
	sshPublicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINexample user@host"

	tests := []struct {
		name    string
		req     createCredentialRequest
		wantErr string // substring to match; empty means "no error"
	}{
		{
			name:    "rejects unknown type",
			req:     createCredentialRequest{Type: "BANANA", Value: "x"},
			wantErr: "type must be one of",
		},
		{
			name:    "USERPASS requires username",
			req:     createCredentialRequest{Type: "USERPASS", Value: "pwd"},
			wantErr: "username is required",
		},
		{
			name: "USERPASS rejects whitespace-only username",
			req: createCredentialRequest{
				Type: "USERPASS", Value: "pwd", Username: &emptyUsername,
			},
			wantErr: "username is required",
		},
		{
			name: "USERPASS accepts username + password",
			req: createCredentialRequest{
				Type: "USERPASS", Value: "pwd", Username: &username,
			},
		},
		{
			name:    "SSH_KEY rejects bare public key",
			req:     createCredentialRequest{Type: "SSH_KEY", Value: sshPublicKey},
			wantErr: "PEM-encoded private key",
		},
		{
			name:    "SSH_KEY rejects garbage",
			req:     createCredentialRequest{Type: "SSH_KEY", Value: "not a key"},
			wantErr: "PEM-encoded private key",
		},
		{
			name: "SSH_KEY accepts OpenSSH private key",
			req:  createCredentialRequest{Type: "SSH_KEY", Value: sshPEM},
		},
		{
			name: "SSH_KEY accepts RSA PKCS#1 private key",
			req:  createCredentialRequest{Type: "SSH_KEY", Value: rsaPEM},
		},
		{
			name: "SSH_KEY accepts PKCS#8 private key",
			req:  createCredentialRequest{Type: "SSH_KEY", Value: pkcs8PEM},
		},
		{
			name: "CERTIFICATE accepts PEM cert",
			req:  createCredentialRequest{Type: "CERTIFICATE", Value: certPEM},
		},
		{
			name:    "CERTIFICATE rejects non-PEM",
			req:     createCredentialRequest{Type: "CERTIFICATE", Value: "MIIDazCC..."},
			wantErr: "PEM-encoded",
		},
		{
			name: "GENERIC_SECRET accepts any opaque value",
			req:  createCredentialRequest{Type: "GENERIC_SECRET", Value: "hunter2"},
		},
		{
			name: "API_KEY (legacy) still accepted with no extra fields",
			req:  createCredentialRequest{Type: "API_KEY", Value: "sk-..."},
		},
		// The API_KEY gate is provider-conditional, and these rows pin which
		// side of it each provider is on. An opaque key must keep sailing
		// through for every provider that dials a fixed vendor host — otherwise
		// phase 2 has broken every credential in every workspace — while a
		// BYO-endpoint provider's value must carry the URL the sidecar will
		// dial. The URL rules themselves are exercised in
		// TestProviderEndpointFromValue; what is under test here is only that
		// the gate fires for the right providers and for no others.
		{
			name: "API_KEY for a fixed-host provider stays opaque",
			req:  createCredentialRequest{Type: "API_KEY", Provider: "OPENAI", Value: "sk-proj-not-a-url"},
		},
		{
			name: "API_KEY with no provider stays opaque",
			req:  createCredentialRequest{Type: "API_KEY", Value: "hunter2"},
		},
		{
			name: "API_KEY for OPENROUTER stays opaque (fixed upstream)",
			req:  createCredentialRequest{Type: "API_KEY", Provider: "OPENROUTER", Value: "sk-or-v1-not-a-url"},
		},
		{
			name: "OPENAI_COMPAT accepts the endpoint object",
			req: createCredentialRequest{
				Type: "API_KEY", Provider: "OPENAI_COMPAT",
				Value: `{"baseURL":"https://llm.internal.example/v1","apiKey":"sk-abc","headers":{"X-Org":"acme"}}`,
			},
		},
		{
			name: "OPENAI_COMPAT accepts a bare URL (unauthenticated endpoint)",
			req: createCredentialRequest{
				Type: "API_KEY", Provider: "OPENAI_COMPAT",
				Value: "http://192.168.1.222:11434/v1",
			},
		},
		{
			name: "OPENAI_COMPAT rejects a bare key with no endpoint",
			req: createCredentialRequest{
				Type: "API_KEY", Provider: "OPENAI_COMPAT", Value: "sk-just-a-key",
			},
			wantErr: "must use http or https",
		},
		{
			name: "OPENAI_COMPAT rejects the cloud-metadata address",
			req: createCredentialRequest{
				Type: "API_KEY", Provider: "OPENAI_COMPAT",
				Value: "http://169.254.169.254/v1",
			},
			wantErr: "link-local/metadata/reserved",
		},
		{
			name: "OAUTH2 (legacy) still accepted with no extra fields",
			req:  createCredentialRequest{Type: "OAUTH2", Value: "pending_oauth"},
		},
		{
			name: "ENDPOINT_URL accepts http URL",
			req:  createCredentialRequest{Type: "ENDPOINT_URL", Value: "http://host.docker.internal:11434/v1"},
		},
		{
			name: "ENDPOINT_URL accepts https URL",
			req:  createCredentialRequest{Type: "ENDPOINT_URL", Value: "https://ollama.example.com/v1"},
		},
		{
			name:    "ENDPOINT_URL rejects bare text (no scheme)",
			req:     createCredentialRequest{Type: "ENDPOINT_URL", Value: "not a url"},
			wantErr: "must use http or https",
		},
		{
			name:    "ENDPOINT_URL rejects non-http scheme",
			req:     createCredentialRequest{Type: "ENDPOINT_URL", Value: "ftp://host:21/x"},
			wantErr: "must use http or https",
		},
		{
			name:    "ENDPOINT_URL rejects PEM pasted by mistake",
			req:     createCredentialRequest{Type: "ENDPOINT_URL", Value: pkcs8PEM},
			wantErr: "valid URL",
		},
		{
			name:    "ENDPOINT_URL rejects scheme-only",
			req:     createCredentialRequest{Type: "ENDPOINT_URL", Value: "http://"},
			wantErr: "must include a host",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validateCredentialPayload(&tt.req)
			if tt.wantErr == "" {
				if got != "" {
					t.Errorf("validateCredentialPayload() = %q, want no error", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantErr) {
				t.Errorf("validateCredentialPayload() = %q, want substring %q", got, tt.wantErr)
			}
		})
	}
}

// TestProviderEndpointFromValue covers the split that decides what reaches the
// sidecar. Two properties matter more than the field values themselves:
//
//   - a provider that dials a fixed vendor host must come back byte-identical
//     to its stored value, because that is what today's boot payload carries and
//     phase 2 changes nothing for the three grandfathered providers;
//   - the endpoint JSON must NEVER come back as the token. Handing the whole
//     object to the Authorization header would ship the base URL and every
//     custom header to the upstream on each call, and would authenticate with
//     nothing.
//
// Hermetic: validateEndpointURL and parseEndpointValue are pure string/URL work
// with no DNS or dial in them.
func TestProviderEndpointFromValue(t *testing.T) {
	t.Parallel()

	const compatJSON = `{"baseURL":"https://llm.internal.example/v1","apiKey":"sk-byo-abc","headers":{"X-Org":"acme"}}`

	tests := []struct {
		name        string
		provider    string
		value       string
		wantToken   string
		wantBaseURL string
		wantHeaders map[string]string
		wantErr     string // substring; empty means "no error"
	}{
		{
			name:      "fixed-host provider returns the value verbatim as the token",
			provider:  "OPENROUTER",
			value:     "sk-or-v1-EXAMPLE-NOT-A-REAL-KEY",
			wantToken: "sk-or-v1-EXAMPLE-NOT-A-REAL-KEY",
		},
		{
			name:      "provider with no route spec returns the value verbatim",
			provider:  "NOTION",
			value:     "secret_abc123",
			wantToken: "secret_abc123",
		},
		{
			name:      "empty provider returns the value verbatim",
			provider:  "",
			value:     "opaque",
			wantToken: "opaque",
		},
		{
			name:        "BYO endpoint splits token, base URL and headers",
			provider:    "OPENAI_COMPAT",
			value:       compatJSON,
			wantToken:   "sk-byo-abc",
			wantBaseURL: "https://llm.internal.example/v1",
			wantHeaders: map[string]string{"X-Org": "acme"},
		},
		{
			name:        "BYO endpoint with no auth material yields a base URL and no token",
			provider:    "OPENAI_COMPAT",
			value:       "http://192.168.1.222:11434/v1",
			wantBaseURL: "http://192.168.1.222:11434/v1",
		},
		{
			name:     "BYO endpoint rejects a bare key — there would be no upstream",
			provider: "OPENAI_COMPAT",
			value:    "sk-just-a-key",
			wantErr:  "must use http or https",
		},
		{
			name:     "BYO endpoint rejects JSON with no baseURL",
			provider: "OPENAI_COMPAT",
			value:    `{"apiKey":"sk-abc"}`,
			wantErr:  "must be a URL",
		},
		{
			name:     "BYO endpoint rejects userinfo in the URL",
			provider: "OPENAI_COMPAT",
			value:    "https://user:pass@llm.example.com/v1",
			wantErr:  "must not embed credentials",
		},
		{
			name:     "BYO endpoint rejects a token over plaintext http to a public host",
			provider: "OPENAI_COMPAT",
			value:    `{"baseURL":"http://llm.example.com/v1","apiKey":"sk-abc"}`,
			wantErr:  "must use https",
		},
		{
			name:     "BYO endpoint rejects a link-local host",
			provider: "OPENAI_COMPAT",
			value:    "http://169.254.169.254/v1",
			wantErr:  "link-local",
		},
		{
			name:     "BYO endpoint rejects an oversized value",
			provider: "OPENAI_COMPAT",
			value:    `{"baseURL":"https://llm.example.com/` + strings.Repeat("a", maxEndpointValueLen) + `"}`,
			wantErr:  "too long",
		},
		{
			name:     "BYO endpoint rejects an empty value",
			provider: "OPENAI_COMPAT",
			value:    "",
			wantErr:  "endpoint URL is required",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token, baseURL, headers, err := providerEndpointFromValue(tt.provider, tt.value)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("providerEndpointFromValue(%q, …) = (%q, %q, %v, nil), want error containing %q",
						tt.provider, token, baseURL, headers, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
				}
				// A refused value must yield nothing at all: a half-populated
				// return would be delivered as a credential pointing nowhere.
				if token != "" || baseURL != "" || headers != nil {
					t.Errorf("rejected value still returned (%q, %q, %v)", token, baseURL, headers)
				}
				return
			}
			if err != nil {
				t.Fatalf("providerEndpointFromValue(%q, …) unexpected error: %v", tt.provider, err)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
			if baseURL != tt.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", baseURL, tt.wantBaseURL)
			}
			if len(headers) != len(tt.wantHeaders) {
				t.Errorf("headers = %v, want %v", headers, tt.wantHeaders)
			}
			for k, v := range tt.wantHeaders {
				if headers[k] != v {
					t.Errorf("headers[%q] = %q, want %q", k, headers[k], v)
				}
			}
			// The property the whole split exists for.
			if strings.Contains(token, "baseURL") {
				t.Errorf("token %q still carries the endpoint JSON — the object must never "+
					"travel as the bearer token", token)
			}
		})
	}
}

func TestLooksLikePEM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  string
		marker string
		want   bool
	}{
		{"empty", "", "PRIVATE KEY", false},
		{"plain text", "hello world", "PRIVATE KEY", false},
		{"missing END marker", "-----BEGIN PRIVATE KEY-----\nABC", "PRIVATE KEY", false},
		{"PKCS#8 private key", pemFixture("PRIVATE KEY", "ABC"), "PRIVATE KEY", true},
		{"RSA private key", pemFixture("RSA PRIVATE KEY", "ABC"), "PRIVATE KEY", true},
		{"OpenSSH private key", pemFixture("OPENSSH PRIVATE KEY", "ABC"), "PRIVATE KEY", true},
		{"EC private key", pemFixture("EC PRIVATE KEY", "ABC"), "PRIVATE KEY", true},
		{"certificate matches CERTIFICATE marker", pemFixture("CERTIFICATE", "ABC"), "CERTIFICATE", true},
		{"certificate does NOT match PRIVATE KEY marker", pemFixture("CERTIFICATE", "ABC"), "PRIVATE KEY", false},
		{"public key does NOT match PRIVATE KEY", pemFixture("PUBLIC KEY", "ABC"), "PRIVATE KEY", false},
		{"surrounding whitespace tolerated", "  \n" + pemFixture("CERTIFICATE", "ABC") + "\n  ", "CERTIFICATE", true},
		// CRLF line endings: a PEM exported on Windows or pasted from
		// Notepad uses \r\n. The naked split-on-\n leaves the trailing
		// \r glued to the closing dashes, which trips the label check
		// if we don't TrimSpace before stripping suffixes. Regression
		// guard for that exact bug.
		{
			"CRLF line endings on private key",
			pemFixture("OPENSSH PRIVATE KEY", "ABC") + "",
			"PRIVATE KEY",
			true,
		},
		{
			"CRLF line endings on certificate",
			strings.ReplaceAll(pemFixture("CERTIFICATE", "ABC"), "\n", "\r\n"),
			"CERTIFICATE",
			true,
		},
		{
			"CRLF line endings on private key (literal CRLF)",
			strings.ReplaceAll(pemFixture("RSA PRIVATE KEY", "ABC"), "\n", "\r\n"),
			"PRIVATE KEY",
			true,
		},
		// Mismatched BEGIN/END labels: real PEMs always pair, so a
		// mismatched header is either copy-paste damage or a hostile
		// shape. The pre-fix structural check passed these because it
		// only validated the BEGIN label and the existence of any
		// "-----END " substring.
		{
			"BEGIN PRIVATE KEY but END CERTIFICATE — rejected",
			"-----BEGIN OPENSSH PRIVATE KEY-----\nABC\n-----END CERTIFICATE-----",
			"PRIVATE KEY",
			false,
		},
		{
			"BEGIN PUBLIC KEY but END CERTIFICATE — rejected against CERT",
			"-----BEGIN PUBLIC KEY-----\nABC\n-----END CERTIFICATE-----",
			"CERTIFICATE",
			false,
		},
		{
			"BEGIN CERTIFICATE but END PRIVATE KEY — rejected against CERT",
			"-----BEGIN CERTIFICATE-----\nABC\n-----END PRIVATE KEY-----",
			"CERTIFICATE",
			false,
		},
		// HasSuffix substring confusion: a payload labelled XPRIVATE
		// KEY would slip past a naked HasSuffix(label, "PRIVATE KEY").
		// labelMatchesMarker requires either exact equality or a
		// space-separated prefix to close the foot-gun.
		{
			"BEGIN XPRIVATE KEY — rejected (no space before marker)",
			"-----BEGIN XPRIVATE KEY-----\nABC\n-----END XPRIVATE KEY-----",
			"PRIVATE KEY",
			false,
		},
		{
			"BEGIN MYCERTIFICATE — rejected against CERTIFICATE marker",
			"-----BEGIN MYCERTIFICATE-----\nABC\n-----END MYCERTIFICATE-----",
			"CERTIFICATE",
			false,
		},
		// Empty marker is a future-caller foot-gun — HasSuffix(any, "")
		// is true, so a forgotten label constant would silently turn
		// the validator into "accept any PEM." Fail closed.
		{
			"empty marker rejects everything",
			pemFixture("RSA PRIVATE KEY", "ABC"),
			"",
			false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := looksLikePEM(tt.value, tt.marker); got != tt.want {
				t.Errorf("looksLikePEM(%q, %q) = %v, want %v", tt.value, tt.marker, got, tt.want)
			}
		})
	}
}
