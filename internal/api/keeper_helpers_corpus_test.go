package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestEnvVarNamePattern_RejectsAttackCorpora is the third canonical
// consumer of internal/testutil/inputcorpus (after slug + service
// name). Pins envVarNamePattern (POSIX env var name shape:
// ^[a-zA-Z_][a-zA-Z0-9_]*$) against the shared attack corpus so a
// "relax the regex" refactor that started accepting "../etc/passwd"
// or "FOO\x00BAR" surfaces here.
//
// Env var names matter extra: they land directly on the agent
// process's environment via Keeper. A spoofed name that bypasses
// the validator could:
//
//   - shadow a legitimate credential (export EVIL_PATH that the
//     agent looks up under PATH if the regex accepted ';');
//   - inject a payload through the env-aware exec path on a
//     non-Go runtime later in the chain (Python / Node / shell);
//   - smuggle a zero-width / homoglyph variant of a known name
//     past a downstream allow-list comparison.
//
// The existing TestEnvVarNamePattern in keeper_helpers_test.go
// covers 10 happy-path / simple-negative cases. This test
// complements it with the BREADTH of the shared corpus — adding
// a new attack variant to inputcorpus auto-improves coverage here.
//
// The SQL injection corpus is intentionally omitted: some payloads
// (`admin`, `0x27` after lowercase fold) are character-shape-valid
// as env var names. Same reasoning as the slug + service consumers.
func TestEnvVarNamePattern_RejectsAttackCorpora(t *testing.T) {
	t.Parallel()

	corpora := []struct {
		name    string
		samples []string
	}{
		{"PathTraversal", testutil.PathTraversalSamples()},
		{"NullByte", testutil.NullByteSamples()},
		{"ZeroWidth", testutil.ZeroWidthSamples()},
		{"ShellInjection", testutil.ShellInjectionSamples()},
		{"HTMLInjection", testutil.HTMLInjectionSamples()},
		{"UnicodeHomoglyph", testutil.UnicodeHomoglyphSamples()},
	}

	for _, c := range corpora {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for i, sample := range c.samples {
				if sample == "" {
					if envVarNamePattern.MatchString(sample) {
						t.Errorf("%s[%d]: empty string should not be a valid env var name", c.name, i)
					}
					continue
				}
				if envVarNamePattern.MatchString(sample) {
					t.Errorf("%s[%d] = %q was accepted as a valid env var name — pattern must reject every entry in attack corpora",
						c.name, i, sample)
				}
			}
		})
	}
}

// TestEnvVarNamePattern_BoundaryCases pins shape edges that the
// attack corpora don't cover but that matter for the pattern's
// safety contract. These are NOT in attack corpora because they're
// just oddly-shaped, not hostile — but a refactor flipping any of
// them silently changes the env-var-name semantic.
func TestEnvVarNamePattern_BoundaryCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		// Boundary-positive — make sure these still pass
		{"single underscore", "_", true},
		{"single letter", "a", true},
		{"single uppercase", "A", true},
		{"long all-underscore", "____", true},
		{"long underscores + digits + letters", "_a1b2c3_xyz", true},
		// Boundary-negative — these MUST still be rejected
		{"single digit", "1", false},
		{"only digits", "12345", false},
		{"trailing whitespace", "FOO ", false},
		{"leading whitespace", " FOO", false},
		{"embedded newline", "FOO\nBAR", false},
		{"embedded tab", "FOO\tBAR", false},
		{"embedded dot", "FOO.BAR", false},
		{"embedded colon", "FOO:BAR", false},
		// Oversize — pattern has no explicit length cap, but the env
		// itself does. Document that the validator alone won't reject
		// a megabyte name; the caller MUST cap separately.
		{"1 KiB of valid chars (regex accepts)", strings.Repeat("A", 1024), true},
		{"64 KiB of valid chars (regex accepts)", strings.Repeat("A", 64*1024), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := envVarNamePattern.MatchString(tc.input)
			if got != tc.want {
				t.Errorf("envVarNamePattern.MatchString(%q...len=%d) = %v, want %v",
					tc.input[:min(len(tc.input), 32)], len(tc.input), got, tc.want)
			}
		})
	}
}
