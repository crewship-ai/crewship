package ws

import "testing"

// clampMaxTurns guards the cost-safety turn cap against an untrusted WS client:
// negatives collapse to 0 (adapter default), and no value can exceed the
// ceiling (a huge value would otherwise disable the guard).
func TestClampMaxTurns(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0},
		{7, 7},
		{maxAllowedTurns, maxAllowedTurns},
		{-1, 0},
		{-999999, 0},
		{maxAllowedTurns + 1, maxAllowedTurns},
		{1 << 30, maxAllowedTurns},
	}
	for _, c := range cases {
		if got := clampMaxTurns(c.in); got != c.want {
			t.Errorf("clampMaxTurns(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
