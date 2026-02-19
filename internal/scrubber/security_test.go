package scrubber

import (
	"strings"
	"testing"
	"time"
)

func TestSecurityGoogleAPIKey(t *testing.T) {
	s := New()
	tests := []struct {
		input string
		want  string
	}{
		{"key=AIzaSyA1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q", "key=[REDACTED:google_key]"},
		{"AIzaSyA1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q", "[REDACTED:google_key]"},
		{"not a key: AIzaXy", "not a key: AIzaXy"}, // too short / wrong prefix
	}
	for _, tt := range tests {
		got := s.Scrub(tt.input)
		if got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSecurityMultipleCredentialsSameLine(t *testing.T) {
	s := New()
	input := "keys: sk-ant-api03-secret123 and ghp_abc123def456ghi789jkl012mno345pqrst and xoxb-token-here"
	got := s.Scrub(input)
	if strings.Contains(got, "sk-ant-") {
		t.Error("anthropic key not scrubbed")
	}
	if strings.Contains(got, "ghp_") {
		t.Error("github token not scrubbed")
	}
	if strings.Contains(got, "xoxb-") {
		t.Error("slack token not scrubbed")
	}
}

func TestSecurityNestedJSON(t *testing.T) {
	s := New()
	input := `{"config":{"nested":{"api_key":"sk-ant-api03-nestedsecret123"}}}`
	got := s.Scrub(input)
	if strings.Contains(got, "sk-ant-api03-nestedsecret123") {
		t.Error("credential in nested JSON not scrubbed")
	}
}

func TestSecurityBase64EncodedCredential(t *testing.T) {
	// Base64-encoded credentials bypass the scrubber by design.
	// The scrubber operates on plaintext patterns; it cannot decode arbitrary
	// base64. This test documents this known limitation.
	s := New()
	// "sk-ant-api03-secret123" base64-encoded = "c2stYW50LWFwaTAzLXNlY3JldDEyMw=="
	input := "encoded: c2stYW50LWFwaTAzLXNlY3JldDEyMw=="
	got := s.Scrub(input)
	// Known limitation: base64-encoded secrets are NOT caught
	// This test documents the behavior, not a failure
	if got != input {
		t.Logf("NOTE: scrubber caught base64-encoded credential (unexpected but OK): %q", got)
	}
}

func TestSecurityURLEncodedCredential(t *testing.T) {
	// URL-encoded credentials: sk-ant-api03-secret = sk-ant-api03-secret (no special chars)
	// But if someone URL-encodes the dashes: sk%2Dant%2Dapi03%2Dsecret
	s := New()
	input := "key=sk%2Dant%2Dapi03%2Dsecret123456"
	got := s.Scrub(input)
	// Known limitation: URL-encoded secrets are NOT caught since the pattern
	// doesn't match percent-encoded dashes
	if got != input {
		t.Logf("NOTE: scrubber caught URL-encoded credential: %q", got)
	}
}

func TestSecurityVeryLongLine(t *testing.T) {
	s := New()
	// 1MB line with a secret embedded in the middle
	padding := strings.Repeat("A", 500000)
	secret := "sk-ant-api03-embeddedinlongline1234567"
	input := padding + secret + padding
	got := s.Scrub(input)
	if strings.Contains(got, "sk-ant-api03-embeddedinlongline") {
		t.Error("credential in 1MB line not scrubbed")
	}
	if !strings.Contains(got, "[REDACTED:anthropic_key]") {
		t.Error("expected redaction marker in output")
	}
}

func TestSecurityBinaryData(t *testing.T) {
	s := New()
	// Input with null bytes and non-UTF8
	input := "prefix\x00\x01\x02sk-ant-api03-binarysecret123\x00\x03suffix"
	got := s.Scrub(input)
	if strings.Contains(got, "sk-ant-api03-binarysecret") {
		t.Error("credential in binary data not scrubbed")
	}
}

func TestSecurityReDoSResistance(t *testing.T) {
	s := New()
	// Craft input designed to trigger catastrophic backtracking in naive regexes
	// Long repeated patterns near regex anchors
	input := strings.Repeat("sk-ant-", 10000) + "not-a-real-key"
	start := time.Now()
	_ = s.Scrub(input)
	duration := time.Since(start)
	if duration > 5*time.Second {
		t.Errorf("scrubber took %v on adversarial input (possible ReDoS)", duration)
	}
}

func TestSecurityUnicodeObfuscation(t *testing.T) {
	s := New()
	// Zero-width characters inserted into key
	// sk-ant-\u200Bapi03-secret123 (zero-width space between "ant-" and "api03")
	input := "sk-ant-\u200Bapi03-secret123456789"
	got := s.Scrub(input)
	// Known limitation: unicode obfuscation may bypass pattern matching.
	// The zero-width space breaks the regex match.
	// This test documents the behavior.
	if got == input {
		t.Log("NOTE: unicode-obfuscated credential was NOT caught (known limitation)")
	}
}

func TestSecurityContainsSecretFalsePositives(t *testing.T) {
	s := New()
	// Normal text that shouldn't trigger false positives
	normalTexts := []string{
		"The ship sailed across the atlantic ocean",
		"Writing code is fun, let me skip this test",
		"Error: file not found at /var/log/app.log",
		`{"status":"ok","message":"All systems operational"}`,
		"Build succeeded: 42 tests passed, 0 failed",
		"func main() { fmt.Println(\"hello world\") }",
	}
	for _, text := range normalTexts {
		if s.ContainsSecret(text) {
			t.Errorf("false positive: ContainsSecret(%q) = true", text)
		}
	}
}

func TestSecurityContainsSecretTruePositives(t *testing.T) {
	s := New()
	secretTexts := []string{
		"sk-ant-api03-realsecret1234567890",
		"ghp_1234567890abcdefghij1234567890abcdef",
		"AKIAIOSFODNN7EXAMPLE",
		"xoxb-123456789-abcdefghijklmnop",
		`"password": "mysecret123"`,
		"Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature",
		"AIzaSyA1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q",
	}
	for _, text := range secretTexts {
		if !s.ContainsSecret(text) {
			t.Errorf("missed secret: ContainsSecret(%q) = false", text)
		}
	}
}

func TestSecurityConcurrentScrub(t *testing.T) {
	s := New()
	errCh := make(chan string, 100)
	for i := 0; i < 100; i++ {
		go func() {
			input := "key: sk-ant-api03-concurrent-secret-test1234"
			got := s.Scrub(input)
			if strings.Contains(got, "sk-ant-") {
				errCh <- got
			} else {
				errCh <- ""
			}
		}()
	}
	for i := 0; i < 100; i++ {
		if msg := <-errCh; msg != "" {
			t.Errorf("concurrent scrub failed: %q", msg)
		}
	}
}
