package cli

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"x", 1},                        // min 1 for non-empty
		{"abcd", 1},                     // exactly 4 chars → 1 token
		{"abcdefgh", 2},                 // 8 chars → 2 tokens
		{strings.Repeat("a", 400), 100}, // 400/4 = 100
	}
	for _, c := range cases {
		if got := EstimateTokens(c.in); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestEstimateTokens_UnicodeRuneCount(t *testing.T) {
	// Czech: "české znaky" — 11 runes. len() in bytes would be ~16.
	in := "české znaky"
	got := EstimateTokens(in)
	// 11 / 4 = 2 tokens (integer division)
	if got != 2 {
		t.Errorf("got %d, want 2 (rune-based count)", got)
	}
}

func TestFormatEstimate_Contents(t *testing.T) {
	out := FormatEstimate(strings.Repeat("hello world ", 100))
	for _, want := range []string{"prompt size", "tokens", "Sonnet", "Opus", "Haiku", "$"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestFormatEstimate_Empty(t *testing.T) {
	out := FormatEstimate("")
	if !strings.Contains(out, "0 chars") {
		t.Errorf("empty input should show 0 chars: %s", out)
	}
}

func TestFormatThousands(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		1:       "1",
		999:     "999",
		1000:    "1,000",
		12345:   "12,345",
		1234567: "1,234,567",
		-1234:   "-1,234",
	}
	for in, want := range cases {
		if got := formatThousands(in); got != want {
			t.Errorf("formatThousands(%d) = %q, want %q", in, got, want)
		}
	}
}
