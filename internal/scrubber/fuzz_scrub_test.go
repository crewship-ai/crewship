package scrubber

import (
	"strings"
	"testing"
)

// FuzzScrub fuzzes the ~17 built-in credential regexes against arbitrary
// input. Scrub sits on the tool-output and memory-write paths, so it
// consumes bytes an agent (or a malicious tool result) fully controls.
// Alternating regex engines is a classic ReDoS surface — a slow match on a
// crafted input would show up as this fuzz target timing out rather than
// panicking, which is exactly the failure mode TestSecurityReDoSResistance
// already guards against for known shapes; fuzzing extends that to shapes
// nobody thought to write by hand.
func FuzzScrub(f *testing.F) {
	seeds := []string{
		"",
		"nothing interesting here",
		"sk-ant-" + strings.Repeat("a", 40),
		"AIzaSy" + strings.Repeat("B", 33),
		"ghp_" + strings.Repeat("c", 36),
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("d", 200) + "\n-----END OPENSSH PRIVATE KEY-----",
		"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc123",
		"key1=sk-ant-" + strings.Repeat("e", 20) + " key2=ghp_" + strings.Repeat("f", 20),
		"​sk-ant-" + strings.Repeat("g", 20) + "​", // zero-width obfuscation
		strings.Repeat("clean text ", 5000),
		"----BEGIN PRIVATE KEY----" + strings.Repeat("(", 5000), // malformed/unterminated PEM-ish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	s := New()
	f.Fuzz(func(t *testing.T, input string) {
		_ = s.Scrub(input)
		_ = s.ContainsSecret(input)
	})
}

// FuzzValidate exercises Validate across all three Mode policies with the
// same untrusted-input shape as FuzzScrub, since Validate runs its own
// normalisation (zero-width strip) + allowlist path that Scrub doesn't.
func FuzzValidate(f *testing.F) {
	seeds := []string{
		"",
		"sk-ant-" + strings.Repeat("a", 40),
		"​sk-ant-" + strings.Repeat("b", 20) + "​",
		"plain text with no secrets",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	s := New()
	f.Fuzz(func(t *testing.T, input string) {
		_ = s.Validate(input, ModeBlock)
		_ = s.Validate(input, ModeWarn)
		_ = s.Validate(input, ModeRedact)
		_ = s.ValidateWithAllowlist(input, ModeRedact, ".*")
	})
}
