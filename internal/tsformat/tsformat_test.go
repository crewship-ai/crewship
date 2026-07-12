package tsformat

import (
	"math/rand"
	"testing"
	"time"
)

// TestFormat_TrailingZeroNanosOrderCorrectly pins the #990 bug class:
// time.RFC3339Nano TRUNCATES trailing zeros in fractional seconds, so
// "…02.5Z" (500ms even) compares lexicographically GREATER than
// "…02.500123456Z" ('Z' 0x5A > '0' 0x30) — inverted against real time
// order. The fixed-width layout must order these correctly.
func TestFormat_TrailingZeroNanosOrderCorrectly(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 0, 2, 0, time.UTC)
	earlier := base.Add(500000000) // …02.500000000 — RFC3339Nano renders "…02.5Z"
	later := base.Add(500123456)   // …02.500123456

	// Demonstrate the RFC3339Nano failure this package exists to prevent.
	if earlier.Format(time.RFC3339Nano) < later.Format(time.RFC3339Nano) {
		t.Fatal("test premise broken: RFC3339Nano now orders trailing-zero fractions correctly?")
	}

	a, b := Format(earlier), Format(later)
	if !(a < b) {
		t.Errorf("fixed-width strings must sort like the times: %q !< %q", a, b)
	}
}

func TestFormat_LexicographicOrderMatchesTimeOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(990))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2000; i++ {
		t1 := base.Add(time.Duration(rng.Int63n(int64(365 * 24 * time.Hour))))
		t2 := base.Add(time.Duration(rng.Int63n(int64(365 * 24 * time.Hour))))
		s1, s2 := Format(t1), Format(t2)
		if t1.Before(t2) != (s1 < s2) || t1.Equal(t2) != (s1 == s2) {
			t.Fatalf("order mismatch: %v vs %v → %q vs %q", t1, t2, s1, s2)
		}
	}
}

func TestFormat_FixedWidthAndUTC(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	for _, tc := range []time.Time{
		time.Date(2026, 7, 12, 10, 0, 2, 0, time.UTC),
		time.Date(2026, 7, 12, 10, 0, 2, 500000000, time.UTC),
		time.Date(2026, 7, 12, 11, 0, 2, 999999999, loc), // non-UTC input normalizes
	} {
		s := Format(tc)
		if len(s) != len("2026-07-12T10:00:02.000000000Z") {
			t.Errorf("Format(%v) = %q — not fixed width", tc, s)
		}
		if s[len(s)-1] != 'Z' {
			t.Errorf("Format(%v) = %q — must normalize to UTC/Z", tc, s)
		}
	}
}

// TestFormat_ParsesBackWithRFC3339Nano guards interop: every reader in the
// codebase parses with time.RFC3339Nano, which accepts the fixed-width form.
func TestFormat_ParsesBackWithRFC3339Nano(t *testing.T) {
	orig := time.Date(2026, 7, 12, 10, 0, 2, 500000000, time.UTC)
	parsed, err := time.Parse(time.RFC3339Nano, Format(orig))
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if !parsed.Equal(orig) {
		t.Errorf("round trip: got %v, want %v", parsed, orig)
	}
}
