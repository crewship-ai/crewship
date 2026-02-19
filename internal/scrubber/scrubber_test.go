package scrubber

import (
	"strings"
	"testing"
)

// buildKey constructs a test key at runtime to avoid Gitleaks flagging literal secrets.
func buildKey(prefix string, bodyLen int) string {
	body := strings.Repeat("abcdef1234567890", (bodyLen/16)+1)
	return prefix + body[:bodyLen]
}

func TestScrubAnthropicAPIKey(t *testing.T) {
	s := New()
	key := buildKey("sk-ant-api03-", 20)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with_context", "my key is " + key, "my key is [REDACTED:anthropic_key]"},
		{"bare_key", buildKey("sk-ant-test-", 12), "[REDACTED:anthropic_key]"},
		{"no_secret", "no key here", "no key here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScrubOpenAIKey(t *testing.T) {
	s := New()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"sk_proj", "key: " + buildKey("sk-proj-", 20), "key: [REDACTED:openai_key]"},
		{"long_sk", buildKey("sk-", 40), "[REDACTED:openai_key]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScrubGitHubToken(t *testing.T) {
	s := New()
	ghpKey := buildKey("ghp_", 36)
	ghoKey := buildKey("gho_", 36)
	ghsKey := buildKey("ghs_", 36)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ghp_envvar", "GITHUB_TOKEN=" + ghpKey, "GITHUB_TOKEN=[REDACTED:github_token]"},
		{"gho", "token: " + ghoKey, "token: [REDACTED:github_token]"},
		{"ghs", ghsKey, "[REDACTED:github_token]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScrubSSHPrivateKey(t *testing.T) {
	s := New()
	// Construct PEM block at runtime
	pemBody := strings.Repeat("AAAA", 10)
	tests := []struct {
		name       string
		input      string
		notContain string
		contain    string
	}{
		{
			"openssh",
			"some text\n-----BEGIN OPENSSH PRIVATE KEY-----\n" + pemBody + "\n-----END OPENSSH PRIVATE KEY-----\nmore text",
			"BEGIN OPENSSH PRIVATE KEY",
			"[REDACTED:ssh_private_key]",
		},
		{
			"rsa",
			"-----BEGIN RSA PRIVATE KEY-----\n" + pemBody + "\n-----END RSA PRIVATE KEY-----",
			"BEGIN RSA PRIVATE KEY",
			"[REDACTED:private_key]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if strings.Contains(got, tt.notContain) {
				t.Errorf("expected %q to be scrubbed from output", tt.notContain)
			}
			if !strings.Contains(got, tt.contain) {
				t.Errorf("expected %q in output, got %q", tt.contain, got)
			}
		})
	}

	// Verify surrounding text preserved
	t.Run("preserves_context", func(t *testing.T) {
		input := "some text\n-----BEGIN OPENSSH PRIVATE KEY-----\n" + pemBody + "\n-----END OPENSSH PRIVATE KEY-----\nmore text"
		got := s.Scrub(input)
		if !strings.Contains(got, "some text") || !strings.Contains(got, "more text") {
			t.Error("non-secret text should be preserved")
		}
	})
}

func TestScrubSlackToken(t *testing.T) {
	s := New()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"xoxb", "xoxb-123456789-" + strings.Repeat("a", 16), "[REDACTED:slack_token]"},
		{"xoxp", "xoxp-1234-5678-abcd", "[REDACTED:slack_token]"},
		{"xoxa", "xoxa-1234-abcd", "[REDACTED:slack_token]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScrubAWSKey(t *testing.T) {
	s := New()
	// AWS keys: AKIA + 16 uppercase alphanumeric chars
	awsKey := "AKIA" + strings.Repeat("A", 16)
	t.Run("bare", func(t *testing.T) {
		got := s.Scrub(awsKey)
		if got != "[REDACTED:aws_key]" {
			t.Errorf("Scrub(%q) = %q, want [REDACTED:aws_key]", awsKey, got)
		}
	})
}

func TestScrubGenericPassword(t *testing.T) {
	s := New()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"json_password", `"password": "mysecretpass123"`, `"password": "[REDACTED]"`},
		{"json_no_space", `"password":"short"`, `"password":"[REDACTED]"`},
		{"env_password", `PASSWORD=mysecretvalue123`, `PASSWORD=[REDACTED]`},
		{"env_secret_key", `SECRET_KEY=abcdef123456`, `SECRET_KEY=[REDACTED]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestScrubMultipleSecrets(t *testing.T) {
	s := New()
	anthKey := buildKey("sk-ant-api03-", 20)
	ghpKey := buildKey("ghp_", 36)
	tests := []struct {
		name       string
		input      string
		notContain string
	}{
		{"anthropic", "key: " + anthKey + " and more", "sk-ant-"},
		{"github", "token: " + ghpKey, "ghp_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scrub(tt.input)
			if strings.Contains(got, tt.notContain) {
				t.Errorf("%s key not scrubbed in %q", tt.name, got)
			}
		})
	}
}

func TestScrubPreservesNonSecretContent(t *testing.T) {
	s := New()
	t.Run("normal_text", func(t *testing.T) {
		input := "This is a normal log message with no secrets at line 42"
		got := s.Scrub(input)
		if got != input {
			t.Errorf("non-secret content modified: %q -> %q", input, got)
		}
	})
}

func TestScrubEmptyString(t *testing.T) {
	s := New()
	t.Run("empty", func(t *testing.T) {
		if got := s.Scrub(""); got != "" {
			t.Errorf("empty string should return empty, got %q", got)
		}
	})
}

func TestScrubWithCustomPatterns(t *testing.T) {
	s := New()
	if err := s.AddPattern("custom_token", `ctk_[a-zA-Z0-9]{20,}`); err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}
	t.Run("custom_match", func(t *testing.T) {
		input := "my token: " + buildKey("ctk_", 20)
		got := s.Scrub(input)
		if !strings.Contains(got, "[REDACTED:custom_token]") {
			t.Errorf("custom pattern not applied: %q", got)
		}
	})
}

func TestAddPatternInvalidRegex(t *testing.T) {
	s := New()
	t.Run("invalid", func(t *testing.T) {
		err := s.AddPattern("bad", `[invalid`)
		if err == nil {
			t.Error("expected error for invalid regex")
		}
	})
}

func TestScrubFactoryAIToken(t *testing.T) {
	s := New()
	// Build a JWT-like token at runtime: header.payload.signature
	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0." + strings.Repeat("a", 20)
	t.Run("bearer_jwt", func(t *testing.T) {
		input := "Authorization: Bearer " + jwt
		got := s.Scrub(input)
		if strings.Contains(got, "eyJhbGci") {
			t.Error("JWT bearer token should be scrubbed")
		}
	})
}

func BenchmarkScrub(b *testing.B) {
	s := New()
	anthKey := buildKey("sk-ant-api03-", 20)
	ghpKey := buildKey("ghp_", 36)
	input := "Some log output with " + anthKey + " and " + ghpKey + " and normal text"
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
