package scrubber

import (
	"strings"
	"testing"
)

func TestScrubAnthropicAPIKey(t *testing.T) {
	s := New()
	tests := []struct {
		input string
		want  string
	}{
		{"my key is sk-ant-api03-abc123def456", "my key is [REDACTED:anthropic_key]"},
		{"sk-ant-test-key-abc123", "[REDACTED:anthropic_key]"},
		{"no key here", "no key here"},
	}
	for _, tt := range tests {
		got := s.Scrub(tt.input)
		if got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScrubOpenAIKey(t *testing.T) {
	s := New()
	tests := []struct {
		input string
		want  string
	}{
		{"key: sk-proj-abcdefghijklmnopqrst", "key: [REDACTED:openai_key]"},
		{"sk-abcdefghijklmnopqrstuvwxyz1234567890abcdefghijkl", "[REDACTED:openai_key]"},
	}
	for _, tt := range tests {
		got := s.Scrub(tt.input)
		if got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScrubGitHubToken(t *testing.T) {
	s := New()
	tests := []struct {
		input string
		want  string
	}{
		{"GITHUB_TOKEN=ghp_abc123def456ghi789jkl012mno345pqrst", "GITHUB_TOKEN=[REDACTED:github_token]"},
		{"token: gho_abc123def456ghi789jkl012mno345pqrst", "token: [REDACTED:github_token]"},
		{"ghs_abc123def456ghi789jkl012mno345pqrst", "[REDACTED:github_token]"},
	}
	for _, tt := range tests {
		got := s.Scrub(tt.input)
		if got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScrubSSHPrivateKey(t *testing.T) {
	s := New()
	input := `some text
-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAA
AAAA
-----END OPENSSH PRIVATE KEY-----
more text`
	got := s.Scrub(input)
	if strings.Contains(got, "BEGIN OPENSSH PRIVATE KEY") {
		t.Error("SSH private key was not scrubbed")
	}
	if !strings.Contains(got, "[REDACTED:ssh_private_key]") {
		t.Error("expected [REDACTED:ssh_private_key] marker")
	}
	if !strings.Contains(got, "some text") {
		t.Error("non-secret text should be preserved")
	}
	if !strings.Contains(got, "more text") {
		t.Error("non-secret text after key should be preserved")
	}
}

func TestScrubRSAPrivateKey(t *testing.T) {
	s := New()
	input := `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEA2a2rwplBQLOCNGzyYCMqU
-----END RSA PRIVATE KEY-----`
	got := s.Scrub(input)
	if strings.Contains(got, "BEGIN RSA PRIVATE KEY") {
		t.Error("RSA private key was not scrubbed")
	}
	if !strings.Contains(got, "[REDACTED:private_key]") {
		t.Error("expected [REDACTED:private_key] marker")
	}
}

func TestScrubSlackToken(t *testing.T) {
	s := New()
	tests := []struct {
		input string
		want  string
	}{
		{"xoxb-123456789-abcdefghijklmnop", "[REDACTED:slack_token]"},
		{"xoxp-1234-5678-abcd", "[REDACTED:slack_token]"},
		{"xoxa-1234-abcd", "[REDACTED:slack_token]"},
	}
	for _, tt := range tests {
		got := s.Scrub(tt.input)
		if got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScrubAWSKey(t *testing.T) {
	s := New()
	input := "AKIAIOSFODNN7EXAMPLE"
	got := s.Scrub(input)
	if got != "[REDACTED:aws_key]" {
		t.Errorf("Scrub(%q) = %q, want [REDACTED:aws_key]", input, got)
	}
}

func TestScrubGenericPassword(t *testing.T) {
	s := New()
	tests := []struct {
		input string
		want  string
	}{
		{`"password": "mysecretpass123"`, `"password": "[REDACTED]"`},
		{`"password":"short"`, `"password":"[REDACTED]"`},
		{`PASSWORD=mysecretvalue123`, `PASSWORD=[REDACTED]`},
		{`SECRET_KEY=abcdef123456`, `SECRET_KEY=[REDACTED]`},
	}
	for _, tt := range tests {
		got := s.Scrub(tt.input)
		if got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScrubMultipleSecrets(t *testing.T) {
	s := New()
	input := "anthropic key: sk-ant-api03-secret123 and github: ghp_abc123def456ghi789jkl012mno345pqrst"
	got := s.Scrub(input)
	if strings.Contains(got, "sk-ant-") {
		t.Error("anthropic key not scrubbed")
	}
	if strings.Contains(got, "ghp_") {
		t.Error("github token not scrubbed")
	}
}

func TestScrubPreservesNonSecretContent(t *testing.T) {
	s := New()
	input := "This is a normal log message with no secrets at line 42"
	got := s.Scrub(input)
	if got != input {
		t.Errorf("non-secret content modified: %q → %q", input, got)
	}
}

func TestScrubEmptyString(t *testing.T) {
	s := New()
	if got := s.Scrub(""); got != "" {
		t.Errorf("empty string should return empty, got %q", got)
	}
}

func TestScrubWithCustomPatterns(t *testing.T) {
	s := New()
	if err := s.AddPattern("custom_token", `ctk_[a-zA-Z0-9]{20,}`); err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	input := "my token: ctk_abc123def456ghi789jkl0"
	got := s.Scrub(input)
	if !strings.Contains(got, "[REDACTED:custom_token]") {
		t.Errorf("custom pattern not applied: %q", got)
	}
}

func TestAddPatternInvalidRegex(t *testing.T) {
	s := New()
	err := s.AddPattern("bad", `[invalid`)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestScrubFactoryAIToken(t *testing.T) {
	s := New()
	// Factory AI uses bearer tokens -- test generic bearer pattern
	input := `Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature`
	got := s.Scrub(input)
	if strings.Contains(got, "eyJhbGci") {
		t.Error("JWT bearer token should be scrubbed")
	}
}

func BenchmarkScrub(b *testing.B) {
	s := New()
	input := "Some log output with sk-ant-api03-abc123 and ghp_abc123def456ghi789jkl012mno345pqrst and normal text"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scrub(input)
	}
}

func BenchmarkScrubNoSecrets(b *testing.B) {
	s := New()
	input := "This is a normal log message with no secrets, just regular output from the agent at line 42 of main.go"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scrub(input)
	}
}
