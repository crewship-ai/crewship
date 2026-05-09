package main

import (
	"testing"
)

// Tests for the pure helpers in the eval CLI. The Cobra commands
// themselves live behind a network round-trip, so we don't exercise
// runEvalScenarios end-to-end here — that integration is tested at
// the binary level by `crewship eval scenarios` against the
// in-memory test server in the repo's E2E harness (separate). This
// file guards the regression-prone helpers: verdict mapping,
// matrix aggregation, CSV split, key uniqueness.

func TestSemanticAgreementVerdict_AgreePass(t *testing.T) {
	a := compareSide{Status: "COMPLETED"}
	b := compareSide{Status: "COMPLETED"}
	if got := semanticAgreementVerdict(a, b); got != "AGREE-PASS" {
		t.Errorf("got %q, want AGREE-PASS", got)
	}
	// DEDUPED counts as a pass — same as production semantics.
	a.Status, b.Status = "DEDUPED", "COMPLETED"
	if got := semanticAgreementVerdict(a, b); got != "AGREE-PASS" {
		t.Errorf("DEDUPED+COMPLETED should be AGREE-PASS, got %q", got)
	}
}

func TestSemanticAgreementVerdict_AgreeFail(t *testing.T) {
	a := compareSide{Status: "FAILED"}
	b := compareSide{Status: "FAILED"}
	if got := semanticAgreementVerdict(a, b); got != "AGREE-FAIL" {
		t.Errorf("got %q, want AGREE-FAIL", got)
	}
	// Mixed terminal failure modes: still AGREE-FAIL — the
	// verdict is about gate-pass agreement, not error class
	// agreement. A weak-tier FAILED + smart-tier CANCELLED
	// is still "neither passed."
	a.Status, b.Status = "FAILED", "CANCELLED"
	if got := semanticAgreementVerdict(a, b); got != "AGREE-FAIL" {
		t.Errorf("FAILED+CANCELLED should be AGREE-FAIL, got %q", got)
	}
}

func TestSemanticAgreementVerdict_Diverge(t *testing.T) {
	a := compareSide{Status: "COMPLETED"}
	b := compareSide{Status: "FAILED"}
	if got := semanticAgreementVerdict(a, b); got != "DIVERGE-A-PASS" {
		t.Errorf("got %q, want DIVERGE-A-PASS", got)
	}

	a.Status, b.Status = "FAILED", "COMPLETED"
	if got := semanticAgreementVerdict(a, b); got != "DIVERGE-B-PASS" {
		t.Errorf("got %q, want DIVERGE-B-PASS", got)
	}
}

func TestSemanticAgreementVerdict_Ambiguous(t *testing.T) {
	// HTTP_5xx on either side: can't tell whether the routine
	// would've passed the gate, so verdict is AMBIGUOUS rather
	// than a misleading AGREE-FAIL that would imply the routine
	// is broken.
	a := compareSide{Status: "HTTP_503"}
	b := compareSide{Status: "COMPLETED"}
	if got := semanticAgreementVerdict(a, b); got != "AMBIGUOUS" {
		t.Errorf("got %q, want AMBIGUOUS", got)
	}

	// Empty status (transport error before server even shaped a
	// response) → AMBIGUOUS.
	a = compareSide{Status: ""}
	b = compareSide{Status: "COMPLETED"}
	if got := semanticAgreementVerdict(a, b); got != "AMBIGUOUS" {
		t.Errorf("empty A status should be AMBIGUOUS, got %q", got)
	}
}

func TestAggregateMatrix_CountsAndAverages(t *testing.T) {
	outcomes := []scenarioOutcome{
		{Scenario: "a", Tier: "fast", Status: "COMPLETED", CostUSD: 0.001, DurationMs: 100},
		{Scenario: "a", Tier: "fast", Status: "COMPLETED", CostUSD: 0.003, DurationMs: 300},
		{Scenario: "a", Tier: "fast", Status: "FAILED", CostUSD: 0.000, DurationMs: 50},
		{Scenario: "a", Tier: "smart", Status: "COMPLETED", CostUSD: 0.020, DurationMs: 800},
		{Scenario: "a", Tier: "smart", Status: "DEDUPED", CostUSD: 0.000, DurationMs: 5},
	}
	matrix := aggregateMatrix(outcomes, []string{"a"}, []string{"fast", "smart"})

	fast := matrix[matrixKey("a", "fast")]
	if fast.Pass != 2 || fast.Total != 3 {
		t.Errorf("fast cell: got %d/%d, want 2/3", fast.Pass, fast.Total)
	}
	wantAvgCost := (0.001 + 0.003 + 0.0) / 3
	if abs64(fast.AvgCost-wantAvgCost) > 0.00001 {
		t.Errorf("fast avg cost: got %f, want %f", fast.AvgCost, wantAvgCost)
	}

	smart := matrix[matrixKey("a", "smart")]
	if smart.Pass != 2 || smart.Total != 2 {
		t.Errorf("smart cell: got %d/%d, want 2/2 (DEDUPED counts as pass)", smart.Pass, smart.Total)
	}
}

func TestAggregateMatrix_NoOutcomesForCell(t *testing.T) {
	// A cell that received zero outcomes (e.g. user passed a
	// tier that no scenario ran on) should NOT appear as 0/0
	// noise in the matrix — it should simply be absent. The
	// renderer handles missing cells by treating them as 0/0,
	// but the matrix itself stays clean.
	outcomes := []scenarioOutcome{
		{Scenario: "a", Tier: "fast", Status: "COMPLETED"},
	}
	matrix := aggregateMatrix(outcomes, []string{"a"}, []string{"fast", "smart"})
	if _, exists := matrix[matrixKey("a", "smart")]; exists {
		t.Error("smart cell should not be present when no outcomes recorded for it")
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"":                       nil,
		"   ":                    nil,
		"a":                      {"a"},
		"a,b,c":                  {"a", "b", "c"},
		" a , b , c ":            {"a", "b", "c"},
		"fast,smart":             {"fast", "smart"},
		"a,,b":                   {"a", "b"},
		"eval-extract,eval-judge": {"eval-extract", "eval-judge"},
	}
	for in, want := range cases {
		got := splitCSV(in)
		if !slicesEqual(got, want) {
			t.Errorf("splitCSV(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMatrixKey_NoCollisions(t *testing.T) {
	// matrixKey concatenates with a NUL byte. Without that, slugs
	// with hyphens could collide with tier names: "eval-fast"+""
	// vs "eval"+"-fast" both produce "eval-fast" under naive
	// concatenation. The NUL guarantees uniqueness regardless of
	// content because slugs and tiers are validated to be NUL-free.
	k1 := matrixKey("eval-fast", "")
	k2 := matrixKey("eval", "-fast")
	if k1 == k2 {
		t.Errorf("matrixKey collision: %q == %q (NUL separator missing?)", k1, k2)
	}
}

func TestPrettyTierNames_AuthoredFallback(t *testing.T) {
	got := prettyTierNames([]string{"", "fast", "smart"})
	want := []string{"(authored)", "fast", "smart"}
	if !slicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCapOutput_Truncation(t *testing.T) {
	// Inputs at and around the threshold.
	if got := capOutput("short", 10); got != "short" {
		t.Errorf("under cap: got %q, want unchanged", got)
	}
	if got := capOutput("0123456789", 10); got != "0123456789" {
		t.Errorf("at cap: got %q, want unchanged", got)
	}
	long := "0123456789ABCDEFGHIJ" // 20 bytes
	got := capOutput(long, 10)
	if len(got) <= len(long) || got[:10] != "0123456789" {
		t.Errorf("over cap: prefix wrong or did not extend with truncation marker, got %q", got)
	}
}

// slicesEqual is a tiny string-slice equality helper — saves a
// dependency on slices.Equal for a test file that doesn't want to
// pull stdlib imports.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func abs64(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
