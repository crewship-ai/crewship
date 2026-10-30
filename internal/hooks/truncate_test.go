package hooks

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateKeepsMultibyteTextValid pins that hook output cut at the
// byte budget never splits a UTF-8 sequence: the message is shown to the
// operator and stored in the run payload, and a torn rune renders as
// U+FFFD and breaks strict JSON consumers.
func TestTruncateKeepsMultibyteTextValid(t *testing.T) {
	t.Parallel()
	const text = "žluťoučký kůň" // ž and ť are two bytes each
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"fits", text, len(text), text},
		{"ascii cut", "hello world", 5, "hello...[truncated]"},
		{"mid first rune", text, 1, "...[truncated]"},
		{"after first rune", text, 2, "ž...[truncated]"},
		{"mid ť", text, 5, "žlu...[truncated]"},
		{"zero budget", text, 0, "...[truncated]"},
	}
	for _, c := range cases {
		got := truncate(c.s, c.max)
		if !utf8.ValidString(got) {
			t.Errorf("%s: truncate(%q, %d) = %q is not valid UTF-8", c.name, c.s, c.max, got)
		}
		if got != c.want {
			t.Errorf("%s: truncate(%q, %d) = %q, want %q", c.name, c.s, c.max, got, c.want)
		}
		if kept := strings.TrimSuffix(got, "...[truncated]"); len(kept) > c.max {
			t.Errorf("%s: kept %d bytes, budget %d", c.name, len(kept), c.max)
		}
	}
}
