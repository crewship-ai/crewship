package tui

import (
	"testing"
	"unicode/utf8"
)

// TestTruncateCountsRunesNotBytes pins that dashboard cells are cut by
// characters, not bytes: a byte cut tears Czech diacritics into U+FFFD
// and a two-byte rune also cost two columns of the budget.
func TestTruncateCountsRunesNotBytes(t *testing.T) {
	t.Parallel()
	const text = "žluťoučký kůň" // 13 runes, 18 bytes
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"fits by runes", text, 13, text},
		{"mid-rune byte offset", text, 2, "ž…"},
		{"cut before ť", text, 4, "žlu…"},
		{"single column", text, 1, "ž"},
		{"zero", text, 0, ""},
	}
	for _, c := range cases {
		got := truncate(c.s, c.max)
		if !utf8.ValidString(got) {
			t.Errorf("%s: truncate(%q, %d) = %q is not valid UTF-8", c.name, c.s, c.max, got)
		}
		if got != c.want {
			t.Errorf("%s: truncate(%q, %d) = %q, want %q", c.name, c.s, c.max, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > c.max && c.max > 0 {
			t.Errorf("%s: %d runes exceed width %d", c.name, n, c.max)
		}
	}
}
