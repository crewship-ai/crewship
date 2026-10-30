package backup

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateKeepsMultibyteTextValid pins that the flush-hook detail cut
// at its byte budget never splits a UTF-8 sequence; the detail is written
// into the backup manifest and the log.
func TestTruncateKeepsMultibyteTextValid(t *testing.T) {
	t.Parallel()
	const text = "žluťoučký kůň" // ž and ť are two bytes each
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"fits", text, len(text), text},
		{"ascii cut", "hello world", 5, "hello…"},
		{"mid first rune", text, 1, "…"},
		{"after first rune", text, 2, "ž…"},
		{"mid ť", text, 5, "žlu…"},
		{"zero budget", text, 0, "…"},
	}
	for _, c := range cases {
		got := truncate(c.s, c.n)
		if !utf8.ValidString(got) {
			t.Errorf("%s: truncate(%q, %d) = %q is not valid UTF-8", c.name, c.s, c.n, got)
		}
		if got != c.want {
			t.Errorf("%s: truncate(%q, %d) = %q, want %q", c.name, c.s, c.n, got, c.want)
		}
		if kept := strings.TrimSuffix(got, "…"); len(kept) > c.n {
			t.Errorf("%s: kept %d bytes, budget %d", c.name, len(kept), c.n)
		}
	}
}
