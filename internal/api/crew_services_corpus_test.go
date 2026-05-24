package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestServiceNameRe_RejectsAttackCorpora is the second canonical
// consumer of the shared input attack corpus (after
// TestValidSlugFormat_RejectsAttackCorpora). It pins the sidecar
// service-name validator (RFC 1035 DNS label shape, max 63 chars,
// must start with a letter and end with a letter or digit) against
// the same hostile sample set so a future "let's be permissive"
// refactor that started accepting "../etc/passwd", null-byte
// truncation, or zero-width smuggling would surface here.
//
// Six categories are checked. The SQL injection corpus is omitted
// for the same reason as the slug consumer: some payloads (`0x27`,
// `--`) are character-shape-valid under a DNS-label-allow-list even
// though they're attack-shaped in their domain. The validator's job
// is the character allow-list; domain-aware payload detection lives
// elsewhere.
//
// The Unicode homoglyph corpus matters extra here — service names
// land on the crew's internal bridge network as hostnames. A
// homoglyph-spoofed "postgrеs" (Cyrillic 'е') resolving alongside a
// real "postgres" would be a real DNS-poisoning shaped attack the
// sidecar must refuse to wire up.
func TestServiceNameRe_RejectsAttackCorpora(t *testing.T) {
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
					if serviceNameRe.MatchString(sample) {
						t.Errorf("%s[%d]: empty string should not match a DNS label", c.name, i)
					}
					continue
				}
				if serviceNameRe.MatchString(sample) {
					t.Errorf("%s[%d] = %q was accepted as a service name — DNS-label validator must reject every entry in attack corpora",
						c.name, i, sample)
				}
			}
		})
	}
}

// TestServiceNameRe_AcceptsHappyPath pins the positive matrix so a
// refactor that accidentally rejects EVERY input (the more dangerous
// regression) also surfaces.
func TestServiceNameRe_AcceptsHappyPath(t *testing.T) {
	t.Parallel()
	cases := []string{
		"postgres",
		"redis",
		"p", // 1 char
		"postgres-v16",
		"x1",
		"abc-123-def",
		strings.Repeat("a", 63), // exactly at DNS label limit
	}
	for _, s := range cases {
		s := s
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			if !serviceNameRe.MatchString(s) {
				t.Errorf("%q should be a valid DNS-label service name but was rejected", s)
			}
		})
	}
}

// TestServiceNameRe_RejectsBoundaryCases pins the regex's
// boundary semantics so a "small simplification" doesn't quietly
// flip them. These are NOT in the attack corpora because they're
// shape-edge cases, not hostile inputs — empty, leading hyphen,
// leading digit, trailing hyphen, length > 63, uppercase.
func TestServiceNameRe_RejectsBoundaryCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"leading hyphen", "-postgres"},
		{"trailing hyphen", "postgres-"},
		{"leading digit (DNS labels must start with letter)", "1postgres"},
		{"uppercase", "Postgres"},
		{"mixed case", "postGRES"},
		{"underscore is not DNS-safe", "post_gres"},
		{"dot is not DNS-safe (separator only)", "post.gres"},
		{"slash", "post/gres"},
		{"64 chars — 1 over DNS limit", strings.Repeat("a", 64)},
		{"oversize from corpus (1 KiB)", testutil.OversizeString(1024)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if serviceNameRe.MatchString(tc.input) {
				t.Errorf("%q should not match serviceNameRe", tc.input)
			}
		})
	}
}
