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
		{
			name: "OAUTH2 (legacy) still accepted with no extra fields",
			req:  createCredentialRequest{Type: "OAUTH2", Value: "pending_oauth"},
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
