package redact

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestSecret_GoldenCases pins the canonical mask: ≤4 chars → "****",
// longer → "***" + last 4. Any change to this contract breaks the
// recognition cue operators rely on when grepping logs.
func TestSecret_GoldenCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", "****"},
		{"a", "****"},
		{"abcd", "****"},
		{"abcde", "***bcde"},
		{"FAKEPREFIX_1234567890abcdef", "***cdef"},
		{"webhook_secret_with_lots_of_entropy_xyz9", "***xyz9"},
	}
	for _, c := range cases {
		got := Secret(c.in)
		if got != c.want {
			t.Errorf("Secret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestToken_GoldenCases pins the bearer-token mask: <12 chars falls
// through to Secret; 12+ chars shows first 4 + "..." + last 4.
func TestToken_GoldenCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", "****"},
		{"short", "***hort"},
		{"elevenchars", "***hars"},      // 11 chars → Secret
		{"twelvecharss", "twel...arss"}, // 12 chars → masked
		{"crsh_AAAABBBBCCCCDDDDEEEEFFFF", "crsh...FFFF"},
		{"abcd1234efgh5678ijkl90mnopqrs", "abcd...pqrs"},
	}
	for _, c := range cases {
		got := Token(c.in)
		if got != c.want {
			t.Errorf("Token(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestURL_StripsBasicAuth confirms user:pass@ get replaced with
// ***:*** while preserving the rest of the URL — a logged webhook
// URL with embedded credentials becomes safe to pipe into shared
// storage.
func TestURL_StripsBasicAuth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://user:pass@example.com/path", "https://xxx:xxx@example.com/path"},
		{"https://alice:s3cr3t@api.example.com:8443/v1/x", "https://xxx:xxx@api.example.com:8443/v1/x"},
		{"https://example.com/no-auth", "https://example.com/no-auth"},
		{"", ""},
	}
	for _, c := range cases {
		got := URL(c.in)
		if got != c.want {
			t.Errorf("URL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestURL_RedactsSecretQueryParams confirms the known list of
// secret-bearing query keys gets masked (case-insensitive).
func TestURL_RedactsSecretQueryParams(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string // substrings that MUST appear in output
		bad  []string // substrings that MUST NOT appear in output
	}{
		{
			name: "lowercase token",
			in:   "https://example.com/x?token=verysecretvalue&other=ok",
			want: []string{"xxxalue", "other=ok"},
			bad:  []string{"verysecretvalue"},
		},
		{
			name: "mixed-case Token",
			in:   "https://example.com/x?Token=verysecretvalue",
			want: []string{"xxxalue"},
			bad:  []string{"verysecretvalue"},
		},
		{
			name: "access_token + signature",
			in:   "https://example.com/x?access_token=abcdefghijkl&signature=zyxwvutsr",
			want: []string{"xxxijkl"},
			bad:  []string{"abcdefghijkl", "zyxwvutsr"},
		},
		{
			name: "api_key short",
			in:   "https://example.com/x?api_key=ab",
			want: []string{"xxxx"},
			bad:  []string{"api_key=ab&", "api_key=ab\n"},
		},
		{
			name: "untouched non-secret params",
			in:   "https://example.com/x?page=2&format=json",
			want: []string{"page=2", "format=json"},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := URL(c.in)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("URL(%q) = %q, want substring %q", c.in, got, w)
				}
			}
			for _, b := range c.bad {
				if strings.Contains(got, b) {
					t.Errorf("URL(%q) = %q, MUST NOT contain %q", c.in, got, b)
				}
			}
		})
	}
}

// TestURL_PreservesMalformedInput documents the deliberate
// non-redaction-on-parse-failure behaviour. A printf format mismatch
// passed in would otherwise vanish into "****" and hide the bug.
func TestURL_PreservesMalformedInput(t *testing.T) {
	t.Parallel()
	// url.Parse is extremely permissive — most strings parse. We pick
	// a value that *does* fail (control char in scheme).
	malformed := "ht\x00tps://broken"
	got := URL(malformed)
	if got != malformed {
		t.Errorf("URL(%q) altered malformed input → %q (want passthrough)", malformed, got)
	}
}

// TestSecret_AttackCorpus runs every adversarial input from the
// shared corpus through Secret() and asserts:
//   - never panics
//   - never returns the input verbatim for inputs >4 chars
//   - output length is bounded by max(4, 7) = always small
//
// The corpus includes oversized strings, null bytes, zero-width
// glyphs, homoglyphs, SQL/shell/HTML injection bodies. Catches a
// future "optimisation" that accidentally returns input unchanged
// for some pattern.
func TestSecret_AttackCorpus(t *testing.T) {
	t.Parallel()
	corpus := append([]string{
		testutil.OversizeString(1024),
		testutil.OversizeString(64 * 1024),
	}, testutil.AllUntrustedInputSamples()...)
	for _, in := range corpus {
		got := Secret(in)
		if len(in) > 4 && got == in {
			t.Errorf("Secret(%q) returned input verbatim — leak", truncForErr(in))
		}
		if len(got) > 64 {
			t.Errorf("Secret(%q) produced %d-byte output, want bounded", truncForErr(in), len(got))
		}
	}
}

// truncForErr keeps t.Errorf messages readable when an attack corpus
// entry is 64 KiB of "A". Caps at 80 chars + an ellipsis marker.
func truncForErr(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// TestToken_AttackCorpus mirrors TestSecret_AttackCorpus but for
// Token() — same panic/leak/bound guarantees.
func TestToken_AttackCorpus(t *testing.T) {
	t.Parallel()
	corpus := append([]string{
		testutil.OversizeString(1024),
		testutil.OversizeString(64 * 1024),
	}, testutil.AllUntrustedInputSamples()...)
	for _, in := range corpus {
		got := Token(in)
		if len(in) >= 12 && got == in {
			t.Errorf("Token(%q) returned input verbatim — leak", truncForErr(in))
		}
		// Token output is first4 + "..." + last4 = 11 chars for
		// 12+ inputs; short inputs fall through to Secret (≤7
		// chars). Allow a small upper bound headroom for multi-
		// byte UTF-8 edges where len() ≠ rune count.
		if len(got) > 64 {
			t.Errorf("Token(%q) produced %d-byte output, want bounded", truncForErr(in), len(got))
		}
	}
}

// TestURL_AttackCorpus runs adversarial inputs through URL() and
// asserts no panic + bounded output. URL is more permissive than
// the others (it returns input unchanged on parse failure), so this
// is mainly a panic / runaway-allocation regression test.
func TestURL_AttackCorpus(t *testing.T) {
	t.Parallel()
	// All-samples + a couple of oversize strings. URL() is permissive
	// (returns input unchanged on parse failure), so this is mainly
	// a panic / runaway-allocation regression test.
	corpus := append([]string{
		testutil.OversizeString(1024),
	}, testutil.AllUntrustedInputSamples()...)
	for _, in := range corpus {
		got := URL(in)
		// Output should never balloon. Even with our basic-auth
		// rewrite, we add ~11 chars max ("***:***@"). 10× input
		// length is a generous upper bound that still flags any
		// quadratic behaviour.
		if max := 10 * (len(in) + 64); len(got) > max {
			t.Errorf("URL(%q) produced %d-byte output, want ≤%d", truncForErr(in), len(got), max)
		}
	}
}

// TestSecret_NoSubstringExposure pins that ANY 5+ char prefix of
// the original NEVER appears in the redacted form. This is the
// most important security property — a future change that, say,
// "shows the first 4 chars too for better UX" would silently break
// the contract and surface here.
func TestSecret_NoSubstringExposure(t *testing.T) {
	t.Parallel()
	// Synthetic non-provider prefixes — real prefixes (sk_live_, ghp_,
	// gho_) trigger GitHub push-protection scanners on the test file
	// itself. The redaction property is independent of the prefix
	// pattern, so we use neutral prefixes for the assertion.
	secrets := []string{
		"FAKEPREFIX_AAAAAAAAAAAAAAAAAAAAAAAA",
		"webhook_BBBBBBBBBBBBBBBBBBBBBBBB",
		"crsh_pat_CCCCCCCCCCCCCCCCCCCCCC",
	}
	for _, s := range secrets {
		got := Secret(s)
		// The first 5 chars of a sensitive token (e.g., "FAKEP",
		// "crsh_") often disclose the provider — we explicitly
		// forbid them in the redacted form.
		prefix := s[:5]
		if strings.Contains(got, prefix) {
			t.Errorf("Secret(%q) = %q leaked prefix %q", s, got, prefix)
		}
	}
}
