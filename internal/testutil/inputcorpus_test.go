package testutil

import (
	"strings"
	"testing"
)

// Each corpus returns a non-empty slice. Catches the regression
// where a refactor accidentally drops every entry.
func TestCorpora_NonEmpty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  []string
	}{
		{"PathTraversal", PathTraversalSamples()},
		{"NullByte", NullByteSamples()},
		{"ZeroWidth", ZeroWidthSamples()},
		{"SQLInjection", SQLInjectionSamples()},
		{"ShellInjection", ShellInjectionSamples()},
		{"HTMLInjection", HTMLInjectionSamples()},
		{"UnicodeHomoglyph", UnicodeHomoglyphSamples()},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if len(tc.got) == 0 {
				t.Errorf("%s returned empty slice", tc.name)
			}
		})
	}
}

// Each call returns a fresh slice — callers can mutate without
// affecting subsequent invocations. The test mutates a returned
// slice then re-calls and asserts the second call's entries are
// untouched.
func TestCorpora_FreshSlicePerCall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		fn   func() []string
	}{
		{"PathTraversal", PathTraversalSamples},
		{"NullByte", NullByteSamples},
		{"ZeroWidth", ZeroWidthSamples},
		{"SQLInjection", SQLInjectionSamples},
		{"ShellInjection", ShellInjectionSamples},
		{"HTMLInjection", HTMLInjectionSamples},
		{"UnicodeHomoglyph", UnicodeHomoglyphSamples},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := tc.fn()
			if len(first) == 0 {
				t.Skip("empty corpus")
			}
			original := first[0]
			first[0] = "MUTATED-SHOULD-NOT-PERSIST"
			second := tc.fn()
			if second[0] != original {
				t.Errorf("%s: second call returned mutated slice (got %q, want %q) — callers must get fresh slices",
					tc.name, second[0], original)
			}
		})
	}
}

// Path traversal samples should contain the classic ../ — sanity-
// check that the corpus actually covers the canonical case rather
// than only obscure variants.
func TestPathTraversal_IncludesClassic(t *testing.T) {
	t.Parallel()
	got := PathTraversalSamples()
	for _, want := range []string{"../", "../../", `..\`, "..%2F"} {
		found := false
		for _, s := range got {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PathTraversalSamples missing canonical entry %q", want)
		}
	}
}

func TestNullByte_AlwaysContainsZeroByte(t *testing.T) {
	t.Parallel()
	for i, s := range NullByteSamples() {
		if !strings.ContainsRune(s, 0) {
			t.Errorf("NullByteSamples[%d] = %q does not contain a null byte", i, s)
		}
	}
}

func TestZeroWidth_AlwaysContainsZeroWidthRune(t *testing.T) {
	t.Parallel()
	// The corpus must contain AT LEAST ONE codepoint from the
	// zero-width / directional-override class per entry — a sample
	// that's pure ASCII has snuck into the wrong corpus.
	// Reuse the package's documented codepoint set rather than
	// re-listing it — keeps this test honest if the corpus grows.
	suspect := make(map[rune]bool)
	for _, r := range ZeroWidthCodepoints() {
		suspect[r] = true
	}
	for i, s := range ZeroWidthSamples() {
		ok := false
		for _, r := range s {
			if suspect[r] {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("ZeroWidthSamples[%d] = %q contains no zero-width / bidi codepoint", i, s)
		}
	}
}

func TestOversizeString_Length(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n    int
		want int
	}{
		{0, 0},
		{-5, 0},
		{1, 1},
		{1024, 1024},
		{1 << 20, 1 << 20},
		// Clamped at 64 MiB; pass 100 MiB and assert it caps.
		{100 << 20, 64 << 20},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			got := OversizeString(tc.n)
			if len(got) != tc.want {
				t.Errorf("OversizeString(%d) len = %d, want %d", tc.n, len(got), tc.want)
			}
		})
	}
}

func TestOversizeBytes_LengthMatchesString(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 100, 4096} {
		s := OversizeString(n)
		b := OversizeBytes(n)
		if len(b) != len(s) {
			t.Errorf("OversizeBytes(%d) len = %d, OversizeString len = %d", n, len(b), len(s))
		}
	}
}

func TestAllUntrustedInputSamples_AggregatesAll(t *testing.T) {
	t.Parallel()
	got := AllUntrustedInputSamples()
	want := len(PathTraversalSamples()) +
		len(NullByteSamples()) +
		len(ZeroWidthSamples()) +
		len(SQLInjectionSamples()) +
		len(ShellInjectionSamples()) +
		len(HTMLInjectionSamples()) +
		len(UnicodeHomoglyphSamples())
	if len(got) != want {
		t.Errorf("AllUntrustedInputSamples len = %d, want %d (sum of each corpus)", len(got), want)
	}
}

// Sanity smoke: every SQL injection sample contains either a quote,
// SQL keyword, or comment marker — the classes of byte sequences a
// reasonable filter would key on.
func TestSQLInjection_RecognisableShape(t *testing.T) {
	t.Parallel()
	for i, s := range SQLInjectionSamples() {
		lower := strings.ToLower(s)
		if !(strings.ContainsAny(s, "'\"") ||
			strings.Contains(lower, "select") ||
			strings.Contains(lower, "union") ||
			strings.Contains(lower, "drop") ||
			strings.Contains(lower, "--") ||
			strings.Contains(lower, "/*") ||
			strings.Contains(lower, "0x")) {
			t.Errorf("SQLInjectionSamples[%d] = %q has no SQLi-shaped tokens", i, s)
		}
	}
}

func TestShellInjection_RecognisableShape(t *testing.T) {
	t.Parallel()
	// Every entry must contain at least one shell metachar to
	// justify its inclusion. The set is conservative — matches the
	// chars a basic shlex-style sanitiser would key on.
	metas := ";&|`$*\n\r\\<>"
	for i, s := range ShellInjectionSamples() {
		if !strings.ContainsAny(s, metas) {
			t.Errorf("ShellInjectionSamples[%d] = %q has no shell metacharacter", i, s)
		}
	}
}
