package main

import (
	"math"
	"testing"
)

// Tests for the pure helpers in cmd_routine_bench.go. The Cobra
// command itself runs N HTTP round-trips so it's tested at the
// integration layer (live dev VM run captured in PR #285 description);
// here we guard the percentile math, fail-reason classifier, and
// readiness-verdict mapping that are easy to regress.

func TestPercentileF64_EdgeCases(t *testing.T) {
	if got := percentileF64(nil, 50); got != 0 {
		t.Errorf("nil slice should return 0, got %v", got)
	}
	if got := percentileF64([]float64{}, 50); got != 0 {
		t.Errorf("empty slice should return 0, got %v", got)
	}
	if got := percentileF64([]float64{1.5}, 50); got != 1.5 {
		t.Errorf("single-element should return that element, got %v", got)
	}
	if got := percentileF64([]float64{1.5}, 95); got != 1.5 {
		t.Errorf("single-element p95 should return that element, got %v", got)
	}
}

func TestPercentileF64_Interpolation(t *testing.T) {
	// p50 of [1, 2, 3, 4, 5] = 3 (rank 2.0, lower==upper)
	if got := percentileF64([]float64{1, 2, 3, 4, 5}, 50); got != 3 {
		t.Errorf("p50 of [1..5] = %v, want 3", got)
	}
	// p100 = max
	if got := percentileF64([]float64{1, 2, 3, 4, 5}, 100); got != 5 {
		t.Errorf("p100 of [1..5] = %v, want 5", got)
	}
	// p0 = min
	if got := percentileF64([]float64{1, 2, 3, 4, 5}, 0); got != 1 {
		t.Errorf("p0 of [1..5] = %v, want 1", got)
	}
	// p25 of [1, 2, 3, 4, 5] = rank 1.0 = 2
	if got := percentileF64([]float64{1, 2, 3, 4, 5}, 25); got != 2 {
		t.Errorf("p25 of [1..5] = %v, want 2", got)
	}
	// p75 of [1, 2, 3, 4, 5] = rank 3.0 = 4
	if got := percentileF64([]float64{1, 2, 3, 4, 5}, 75); got != 4 {
		t.Errorf("p75 of [1..5] = %v, want 4", got)
	}
	// p95 of [1, 2, 3, 4] = rank 2.85 = lerp(3, 4, 0.85) = 3.85
	got := percentileF64([]float64{1, 2, 3, 4}, 95)
	if math.Abs(got-3.85) > 0.001 {
		t.Errorf("p95 lerp of [1..4] = %v, want 3.85", got)
	}
}

func TestPercentileInt64_TruncationBehaviour(t *testing.T) {
	// Sorted int slices should still produce sensible values
	// without floating-point surprises at the boundaries.
	cases := []struct {
		name   string
		sorted []int64
		p      int
		want   int64
	}{
		{"empty", []int64{}, 50, 0},
		{"single", []int64{42}, 50, 42},
		{"single p100", []int64{42}, 100, 42},
		{"odd p50", []int64{10, 20, 30}, 50, 20},
		{"even p50", []int64{10, 20, 30, 40}, 50, 25}, // lerp(20, 30, 0.5) = 25
		{"max", []int64{100, 200, 300}, 100, 300},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := percentileInt64(c.sorted, c.p); got != c.want {
				t.Errorf("p%d of %v = %d, want %d", c.p, c.sorted, got, c.want)
			}
		})
	}
}

func TestClassifyFailReason_BucketsAllCommonModes(t *testing.T) {
	cases := map[string]string{
		"cost cap exceeded: $0.1399 > $0.0500 after step \"extract\"":         "cost-cap",
		"outcomes failed: word_count_in_range":                                "rubric-fail",
		"output length 5 below min 30":                                        "gate-fail",
		"output contains banned token: API_KEY=":                              "gate-fail",
		"output missing required token: \"qty\"":                              "gate-fail",
		"schema validation: missing required key qty":                         "gate-fail",
		"output not valid JSON: unexpected token":                             "gate-fail",
		"LLMRunner: complete: invalid Anthropic API key":                      "auth-fail",
		"no active Anthropic credential in workspace":                         "auth-fail",
		"context deadline exceeded":                                           "timeout",
		"step timeout after 30s":                                              "timeout",
		"weird unknown error from somewhere":                                  "other",
		"":                                                                    "other",
	}
	for msg, want := range cases {
		got := classifyFailReason(msg)
		if got != want {
			t.Errorf("classifyFailReason(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestContainsAny_EmptyAndMatch(t *testing.T) {
	if !containsAny("hello world", "world") {
		t.Error("expected match")
	}
	if !containsAny("hello world", "foo", "world") {
		t.Error("expected match on second sub")
	}
	if containsAny("hello world", "foo", "bar") {
		t.Error("expected no match")
	}
	if containsAny("", "anything") {
		t.Error("empty haystack should not match")
	}
	if containsAny("anything", "") {
		t.Error("empty needle should be skipped, not matched on empty string")
	}
	// Empty needles ALONGSIDE a real needle should not break the match
	if !containsAny("hello", "", "lo") {
		t.Error("real needle after empty should still match")
	}
}

func TestSummariseBench_AggregatesCorrectly(t *testing.T) {
	attempts := []benchAttempt{
		{Attempt: 1, Status: "COMPLETED", DurationMs: 100, CostUSD: 0.001},
		{Attempt: 2, Status: "COMPLETED", DurationMs: 200, CostUSD: 0.002},
		{Attempt: 3, Status: "FAILED", DurationMs: 50, CostUSD: 0.000, FailReason: "cost-cap"},
		{Attempt: 4, Status: "COMPLETED", DurationMs: 300, CostUSD: 0.003},
		{Attempt: 5, Status: "DEDUPED", DurationMs: 5, CostUSD: 0.000},
	}
	s := summariseBench("test-routine", "fast", attempts)

	if s.Runs != 5 {
		t.Errorf("Runs = %d, want 5", s.Runs)
	}
	// COMPLETED + DEDUPED count as pass (4 of 5)
	if s.Pass != 4 {
		t.Errorf("Pass = %d, want 4", s.Pass)
	}
	if math.Abs(s.PassRate-0.8) > 0.001 {
		t.Errorf("PassRate = %f, want 0.8", s.PassRate)
	}
	if math.Abs(s.CostTotal-0.006) > 0.0001 {
		t.Errorf("CostTotal = %f, want 0.006", s.CostTotal)
	}
	if math.Abs(s.CostMax-0.003) > 0.0001 {
		t.Errorf("CostMax = %f, want 0.003", s.CostMax)
	}
	if s.DurMaxMs != 300 {
		t.Errorf("DurMaxMs = %d, want 300", s.DurMaxMs)
	}
	if s.FailReasons["cost-cap"] != 1 {
		t.Errorf("expected 1 cost-cap fail, got %v", s.FailReasons)
	}
}

func TestSummariseBench_NoFailureKeyOmittedWhenEmpty(t *testing.T) {
	// FailReasons should be nil (not empty map) when every attempt
	// passes — keeps the JSON output clean (`omitempty` works).
	attempts := []benchAttempt{
		{Attempt: 1, Status: "COMPLETED", DurationMs: 100, CostUSD: 0.001},
	}
	s := summariseBench("happy", "", attempts)
	if s.FailReasons != nil {
		t.Errorf("FailReasons should be nil when no failures, got %v", s.FailReasons)
	}
}

func TestReadinessVerdict_ThresholdMapping(t *testing.T) {
	cases := []struct {
		name     string
		summary  benchSummary
		wantPrefix string
	}{
		{"insufficient", benchSummary{Runs: 0}, "INSUFFICIENT_DATA"},
		{"production-ready boundary", benchSummary{Runs: 10, PassRate: 0.9}, "PRODUCTION_READY"},
		{"production-ready well-above", benchSummary{Runs: 10, PassRate: 1.0}, "PRODUCTION_READY"},
		{"flaky boundary", benchSummary{Runs: 10, PassRate: 0.7}, "FLAKY"},
		{"flaky", benchSummary{Runs: 10, PassRate: 0.85}, "FLAKY"},
		{"unreliable", benchSummary{Runs: 10, PassRate: 0.3}, "UNRELIABLE"},
		{"broken", benchSummary{Runs: 10, PassRate: 0}, "BROKEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := readinessVerdict(c.summary)
			if !startsWith(got, c.wantPrefix) {
				t.Errorf("got %q, want prefix %q", got, c.wantPrefix)
			}
		})
	}
}

func startsWith(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}
