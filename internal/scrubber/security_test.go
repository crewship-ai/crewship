package scrubber

import (
	"strings"
	"testing"
	"time"
)

// buildTestKey constructs a test key at runtime to avoid Gitleaks flagging literal secrets.
func buildTestKey(prefix string, bodyLen int) string {
	body := strings.Repeat("abcdef1234567890", (bodyLen/16)+1)
	return prefix + body[:bodyLen]
}

func TestSecurityGoogleAPIKey(t *testing.T) {
	s := New()
	// Google keys: AIzaSy + 33 chars (alphanumeric + dash/underscore)
	googleKey := "AIzaSy" + strings.Repeat("A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q", 1)[:33]
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with_prefix", "key=" + googleKey, "key=[REDACTED:google_key]"},
		{"bare", googleKey, "[REDACTED:google_key]"},
		{"too_short", "not a key: AIzaXy", "not a key: AIzaXy"},
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

func TestSecurityMultipleCredentialsSameLine(t *testing.T) {
	s := New()
	anthKey := buildTestKey("sk-ant-api03-", 15)
	ghpKey := buildTestKey("ghp_", 36)
	input := "keys: " + anthKey + " and " + ghpKey + " and xoxb-token-here"
	tests := []struct {
		name       string
		notContain string
	}{
		{"anthropic", "sk-ant-"},
		{"github", "ghp_"},
		{"slack", "xoxb-"},
	}
	got := s.Scrub(input)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(got, tt.notContain) {
				t.Errorf("%s key not scrubbed in %q", tt.name, got)
			}
		})
	}
}

func TestSecurityNestedJSON(t *testing.T) {
	s := New()
	key := buildTestKey("sk-ant-api03-", 20)
	t.Run("nested", func(t *testing.T) {
		input := `{"config":{"nested":{"api_key":"` + key + `"}}}`
		got := s.Scrub(input)
		if strings.Contains(got, key) {
			t.Error("credential in nested JSON not scrubbed")
		}
	})
}

func TestSecurityBase64EncodedCredential(t *testing.T) {
	s := New()
	t.Run("known_limitation", func(t *testing.T) {
		// "sk-ant-api03-secret123" base64-encoded = "c2stYW50LWFwaTAzLXNlY3JldDEyMw=="
		input := "encoded: c2stYW50LWFwaTAzLXNlY3JldDEyMw=="
		got := s.Scrub(input)
		if got != input {
			t.Logf("NOTE: scrubber caught base64-encoded credential (unexpected but OK): %q", got)
		}
	})
}

func TestSecurityURLEncodedCredential(t *testing.T) {
	s := New()
	t.Run("known_limitation", func(t *testing.T) {
		input := "key=sk%2Dant%2Dapi03%2Dsecret123456"
		got := s.Scrub(input)
		if got != input {
			t.Logf("NOTE: scrubber caught URL-encoded credential: %q", got)
		}
	})
}

func TestSecurityVeryLongLine(t *testing.T) {
	s := New()
	t.Run("1mb_line", func(t *testing.T) {
		padding := strings.Repeat("A", 500000)
		secret := buildTestKey("sk-ant-api03-", 25)
		input := padding + secret + padding
		got := s.Scrub(input)
		if strings.Contains(got, "sk-ant-api03-") {
			t.Error("credential in 1MB line not scrubbed")
		}
		if !strings.Contains(got, "[REDACTED:anthropic_key]") {
			t.Error("expected redaction marker in output")
		}
	})
}

func TestSecurityBinaryData(t *testing.T) {
	s := New()
	t.Run("null_bytes", func(t *testing.T) {
		key := buildTestKey("sk-ant-api03-", 20)
		input := "prefix\x00\x01\x02" + key + "\x00\x03suffix"
		got := s.Scrub(input)
		if strings.Contains(got, key) {
			t.Error("credential in binary data not scrubbed")
		}
	})
}

func TestSecurityReDoSResistance(t *testing.T) {
	s := New()
	t.Run("adversarial", func(t *testing.T) {
		input := strings.Repeat("sk-ant-", 10000) + "not-a-real-key"
		start := time.Now()
		_ = s.Scrub(input)
		duration := time.Since(start)
		if duration > 5*time.Second {
			t.Errorf("scrubber took %v on adversarial input (possible ReDoS)", duration)
		}
	})
}

func TestSecurityUnicodeObfuscation(t *testing.T) {
	s := New()
	t.Run("zero_width_space", func(t *testing.T) {
		// Zero-width space between "ant-" and "api03" breaks regex match
		input := "sk-ant-\u200Bapi03-secret123456789"
		got := s.Scrub(input)
		if got == input {
			t.Log("NOTE: unicode-obfuscated credential was NOT caught (known limitation)")
		}
	})
}

func TestSecurityContainsSecretFalsePositives(t *testing.T) {
	s := New()
	normalTexts := []struct {
		name string
		text string
	}{
		{"prose", "The ship sailed across the atlantic ocean"},
		{"code", "Writing code is fun, let me skip this test"},
		{"error_log", "Error: file not found at /var/log/app.log"},
		{"json_ok", `{"status":"ok","message":"All systems operational"}`},
		{"build_output", "Build succeeded: 42 tests passed, 0 failed"},
		{"go_code", `func main() { fmt.Println("hello world") }`},
	}
	for _, tt := range normalTexts {
		t.Run(tt.name, func(t *testing.T) {
			if s.ContainsSecret(tt.text) {
				t.Errorf("false positive: ContainsSecret(%q) = true", tt.text)
			}
		})
	}
}

func TestSecurityContainsSecretTruePositives(t *testing.T) {
	s := New()
	// Build test secrets at runtime to avoid scanner flags
	anthKey := buildTestKey("sk-ant-api03-", 20)
	ghpKey := buildTestKey("ghp_", 36)
	awsKey := "AKIA" + strings.Repeat("A", 16)
	googleKey := "AIzaSy" + strings.Repeat("A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q", 1)[:33]
	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0." + strings.Repeat("a", 20)

	secretTexts := []struct {
		name string
		text string
	}{
		{"anthropic", anthKey},
		{"github", ghpKey},
		{"aws", awsKey},
		{"slack", "xoxb-123456789-" + strings.Repeat("a", 16)},
		{"password_json", `"password": "mysecret123"`},
		{"bearer_jwt", "Bearer " + jwt},
		{"google", googleKey},
	}
	for _, tt := range secretTexts {
		t.Run(tt.name, func(t *testing.T) {
			if !s.ContainsSecret(tt.text) {
				t.Errorf("missed secret: ContainsSecret(%q) = false", tt.text)
			}
		})
	}
}

func TestSecurityConcurrentScrub(t *testing.T) {
	s := New()
	key := buildTestKey("sk-ant-api03-", 30)
	errCh := make(chan string, 100)
	for i := 0; i < 100; i++ {
		go func() {
			input := "key: " + key
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
