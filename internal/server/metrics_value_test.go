package server

import (
	"math"
	"strings"
	"testing"
)

// Prometheus values were formatted with strconv's 'g' verb, which switches to
// scientific notation once the exponent grows. Nothing had a large enough
// value to notice until migration versions became YYYYMMDDHHMMSS timestamps:
// crewshipd_db_migration_version then rendered as
//
//	crewshipd_db_migration_version{hostname="…"} 2.02607281102e+13
//
// Prometheus parses that, so nothing breaks loudly — the number is simply
// unreadable on a dashboard, and any alert or comparison written against the
// plain form silently stops matching. The exposition format's own examples use
// plain decimals; there is no reason to emit an exponent for a value that is
// conceptually an integer.

func TestFormatPromValue_NoScientificNotationForBigIntegers(t *testing.T) {
	// A migration version, which is what surfaced this.
	if got := formatPromValue(20260728110200); got != "20260728110200" {
		t.Errorf("formatPromValue = %q, want the plain integer", got)
	}
	for _, v := range []float64{1e13, 1e15, 123456789012345} {
		if got := formatPromValue(v); strings.ContainsAny(got, "eE") {
			t.Errorf("formatPromValue(%v) = %q — an exponent is unreadable on a dashboard", v, got)
		}
	}
}

func TestFormatPromValue_KeepsOrdinaryValuesUnchanged(t *testing.T) {
	// The fix must not disturb the values every other metric emits.
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{14, "14"},
		{0.75, "0.75"},
		{0.011378792, "0.011378792"},
		{-3, "-3"},
	} {
		if got := formatPromValue(tc.in); got != tc.want {
			t.Errorf("formatPromValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatPromValue_SpecialsStayParseable(t *testing.T) {
	// Prometheus accepts these spellings; a collector that hits one should
	// see something it understands rather than an empty field.
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{math.Inf(1), "+Inf"},
		{math.Inf(-1), "-Inf"},
	} {
		if got := formatPromValue(tc.in); got != tc.want {
			t.Errorf("formatPromValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := formatPromValue(math.NaN()); got != "NaN" {
		t.Errorf("formatPromValue(NaN) = %q, want NaN", got)
	}
}
