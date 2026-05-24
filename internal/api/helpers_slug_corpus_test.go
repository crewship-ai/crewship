package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestValidSlugFormat_RejectsAttackCorpora exercises validSlugFormat
// against the shared attack corpus in internal/testutil/inputcorpus.go.
// Six categories are checked here; every entry in every category MUST
// be rejected because none of them are valid slugs under the
// `^[a-z0-9][a-z0-9_-]*$` rule.
//
// Why this test exists:
//   - It demonstrates the documented "consumer pattern" for the
//     inputcorpus package (a future hardening test can copy this
//     shape verbatim).
//   - It pins the slug validator's rejection behaviour against a
//     SHARED catalogue — adding a new attack variant to the corpus
//     auto-improves this test's coverage without anyone editing
//     this file.
//   - Catches the regression where a "let's be more permissive"
//     refactor of validSlugRe quietly starts accepting "../../etc"
//     or "<script>" because the suite never explicitly checks the
//     hostile cases.
//
// The SQL injection corpus is intentionally OMITTED — some of its
// entries ("0x27", "--", legacy comment-only forms) are technically
// valid slug-shape characters even though they're attack-shaped in a
// SQL context. The slug validator's job is character allow-listing,
// not domain-aware payload detection; checking SQL payloads against
// it would generate false positives that obscure the real signal.
func TestValidSlugFormat_RejectsAttackCorpora(t *testing.T) {
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
				// Empty is the obvious accept-no-slug case but our
				// regex requires at least one character, so empty IS
				// rejected. Skip it from the loop only to keep the
				// error message useful — printing %q on the empty
				// string is noisy.
				if sample == "" {
					if validSlugFormat(sample) {
						t.Errorf("%s[%d]: empty string should not be a valid slug", c.name, i)
					}
					continue
				}
				if validSlugFormat(sample) {
					t.Errorf("%s[%d] = %q was accepted as a valid slug — validator must reject every entry in attack corpora",
						c.name, i, sample)
				}
			}
		})
	}
}

// TestValidSlugFormat_AcceptsHappyPath complements the rejection
// suite with positive cases so a future refactor that accidentally
// rejects EVERY input (a more dangerous regression) surfaces too.
func TestValidSlugFormat_AcceptsHappyPath(t *testing.T) {
	t.Parallel()
	cases := []string{
		"engineering",
		"eng",
		"e",
		"crew-1",
		"my_crew",
		"abc123",
		"123abc",
		"a-b-c-d",
		"long-slug-with-many-hyphens-but-still-valid",
	}
	for _, s := range cases {
		s := s
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			if !validSlugFormat(s) {
				t.Errorf("%q should be a valid slug but was rejected", s)
			}
		})
	}
}
