package scrubber

import (
	"strings"
	"testing"
)

// TestScrubber_InstanceIsolation pins the #1054 refactor safety property: the
// built-in patterns are now a shared package-level precompiled slice, but each
// New() copies it into its own backing array, so a custom AddPattern on one
// Scrubber must NOT leak into another instance (or into the shared built-ins).
func TestScrubber_InstanceIsolation(t *testing.T) {
	a := New()
	b := New()

	if err := a.AddPattern("custom", `WIDGET-[0-9]{4}`); err != nil {
		t.Fatalf("AddPattern: %v", err)
	}

	const secret = "token WIDGET-1234 here"
	if got := a.Scrub(secret); strings.Contains(got, "WIDGET-1234") {
		t.Errorf("scrubber a did not apply its own custom pattern: %q", got)
	}
	// b must be unaffected by a's AddPattern (no shared-slice mutation).
	if got := b.Scrub(secret); !strings.Contains(got, "WIDGET-1234") {
		t.Errorf("scrubber b leaked scrubber a's custom pattern: %q", got)
	}

	// A fresh Scrubber built after the mutation must also be clean.
	if got := New().Scrub(secret); !strings.Contains(got, "WIDGET-1234") {
		t.Errorf("a later New() inherited a's custom pattern — shared built-ins were mutated: %q", got)
	}
}

// TestScrubber_BuiltinsStillRedact confirms the hoist didn't drop any built-in:
// a representative sample of provider keys is still redacted by a plain New().
func TestScrubber_BuiltinsStillRedact(t *testing.T) {
	s := New()
	for _, tc := range []string{
		"ghp_" + strings.Repeat("a", 20),
		"sk-ant-" + strings.Repeat("b", 20),
		"AKIA" + strings.Repeat("C", 16),
		"glpat-" + strings.Repeat("d", 20),
	} {
		if got := s.Scrub("value: " + tc); strings.Contains(got, tc) {
			t.Errorf("built-in pattern failed to redact %q: %q", tc, got)
		}
	}
}
