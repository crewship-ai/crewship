package consolidate

import (
	"math"
	"testing"
	"time"
)

func TestDefaultSignalWeights_SumToOne(t *testing.T) {
	w := DefaultSignalWeights()
	sum := w.Sum()
	if math.Abs(sum-1.0) > 1e-9 {
		t.Errorf("default weights sum = %v, want 1.0", sum)
	}
}

func TestRelevanceScore_ClampToUnit(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-0.1, 0},
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{1.5, 1},
		{math.NaN(), 0},
	}
	for _, c := range cases {
		if got := relevanceScore(c.in); got != c.want {
			t.Errorf("relevanceScore(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFrequencyScore_LogShape(t *testing.T) {
	// 0 -> 0; saturates near 1 by count=30.
	if got := frequencyScore(0); got != 0 {
		t.Errorf("frequencyScore(0) = %v, want 0", got)
	}
	if got := frequencyScore(30); got < 0.99 {
		t.Errorf("frequencyScore(30) = %v, want ~1.0", got)
	}
	// Monotonic.
	prev := -1.0
	for n := 1; n <= 20; n++ {
		got := frequencyScore(n)
		if got <= prev {
			t.Errorf("frequencyScore not monotonic at n=%d: %v <= %v", n, got, prev)
		}
		prev = got
	}
}

func TestQueryDiversityScore_Saturation(t *testing.T) {
	if got := queryDiversityScore(0); got != 0 {
		t.Errorf("queryDiversityScore(0) = %v, want 0", got)
	}
	if got := queryDiversityScore(10); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("queryDiversityScore(10) = %v, want 1.0 (saturation)", got)
	}
	if got := queryDiversityScore(100); got > 1.0+1e-9 {
		t.Errorf("queryDiversityScore(100) exceeds 1.0: %v", got)
	}
}

func TestRecencyScore_HalfLife(t *testing.T) {
	now := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	// Today -> ~1.0
	if got := recencyScore(now, now); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("recencyScore(now) = %v, want 1.0", got)
	}
	// 14-day half-life: 14d ago -> ~0.5
	twoWeeks := now.Add(-14 * 24 * time.Hour)
	if got := recencyScore(twoWeeks, now); math.Abs(got-0.5) > 0.01 {
		t.Errorf("recencyScore(now-14d) = %v, want ~0.5", got)
	}
	// 28d ago -> ~0.25
	fourWeeks := now.Add(-28 * 24 * time.Hour)
	if got := recencyScore(fourWeeks, now); math.Abs(got-0.25) > 0.01 {
		t.Errorf("recencyScore(now-28d) = %v, want ~0.25", got)
	}
	// Future timestamps clamp to 1.0 (clock-skew defense).
	future := now.Add(time.Hour)
	if got := recencyScore(future, now); got != 1.0 {
		t.Errorf("recencyScore(future) = %v, want 1.0 (clock-skew clamp)", got)
	}
	// Zero time -> 0
	if got := recencyScore(time.Time{}, now); got != 0 {
		t.Errorf("recencyScore(zero) = %v, want 0", got)
	}
}

func TestConsolidationScore_LinearRamp(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{1, 0.2},
		{3, 0.6},
		{5, 1.0},
		{10, 1.0}, // saturated
	}
	for _, c := range cases {
		got := consolidationScore(int(c.in))
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("consolidationScore(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestConceptualRichnessScore_DiversityWeighted(t *testing.T) {
	// 0 evidence, 0 types -> 0
	if got := conceptualRichnessScore(0, 0); got != 0 {
		t.Errorf("conceptualRichnessScore(0,0) = %v, want 0", got)
	}
	// Saturation: 8 entries + 4 types -> 1.0
	if got := conceptualRichnessScore(8, 4); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("conceptualRichnessScore(8,4) = %v, want 1.0", got)
	}
	// Diversity weighted higher than breadth: 8 entries of 1 type
	// should score lower than 4 entries across 4 types.
	manyOneType := conceptualRichnessScore(8, 1)
	fewManyTypes := conceptualRichnessScore(4, 4)
	if fewManyTypes <= manyOneType {
		t.Errorf("type diversity should outweigh raw count: 4*4 (%v) should beat 8*1 (%v)",
			fewManyTypes, manyOneType)
	}
}

func TestComputeScore_Promoted_WhenAllGatesPass(t *testing.T) {
	now := time.Now()
	m := CandidateMetrics{
		RawRelevance:       0.9,
		RecallCount:        10,
		UniqueQueries:      5,
		ConsolidationCount: 4,
		LastSeenAt:         now.Add(-2 * 24 * time.Hour),
		EvidenceCount:      6,
		DistinctEntryTypes: 3,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Composite < 0.80 {
		t.Errorf("composite = %v, want >= 0.80", res.Composite)
	}
	if !res.Promoted {
		t.Errorf("expected Promoted=true with all gates passing")
	}
}

func TestComputeScore_NotPromoted_RecallCountGate(t *testing.T) {
	now := time.Now()
	// Strong relevance + recent BUT only 1 recall — promotion blocked
	// by MinRecallCount gate, not by composite score.
	m := CandidateMetrics{
		RawRelevance:       1.0,
		RecallCount:        1,
		UniqueQueries:      5,
		ConsolidationCount: 5,
		LastSeenAt:         now,
		EvidenceCount:      8,
		DistinctEntryTypes: 4,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Promoted {
		t.Errorf("recall_count gate should block promotion; got Promoted=true (composite=%v)", res.Composite)
	}
}

func TestComputeScore_NotPromoted_UniqueQueriesGate(t *testing.T) {
	now := time.Now()
	m := CandidateMetrics{
		RawRelevance:       1.0,
		RecallCount:        10,
		UniqueQueries:      2, // < MinUniqueQueries=3
		ConsolidationCount: 5,
		LastSeenAt:         now,
		EvidenceCount:      8,
		DistinctEntryTypes: 4,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Promoted {
		t.Errorf("unique_queries gate should block promotion; got Promoted=true")
	}
}

func TestComputeScore_NotPromoted_ScoreGate(t *testing.T) {
	now := time.Now()
	// Composite below 0.80 despite passing recall+unique gates.
	m := CandidateMetrics{
		RawRelevance:       0.2, // weak
		RecallCount:        3,
		UniqueQueries:      3,
		ConsolidationCount: 1,
		LastSeenAt:         now.Add(-30 * 24 * time.Hour), // very stale
		EvidenceCount:      2,
		DistinctEntryTypes: 1,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Composite >= 0.80 {
		t.Errorf("expected composite < 0.80, got %v", res.Composite)
	}
	if res.Promoted {
		t.Errorf("score gate should block promotion; got Promoted=true")
	}
}

func TestComputeScore_Composite_Bounded(t *testing.T) {
	now := time.Now()
	// Max-out every input -> composite should clamp to ≤ 1.0
	m := CandidateMetrics{
		RawRelevance:       1.0,
		RecallCount:        1000,
		UniqueQueries:      1000,
		ConsolidationCount: 1000,
		LastSeenAt:         now,
		EvidenceCount:      1000,
		DistinctEntryTypes: 1000,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Composite > 1.0+1e-9 {
		t.Errorf("composite exceeds 1.0: %v", res.Composite)
	}
}

func TestNormaliseQuery_Trimming(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Hello World  ", "hello world"},
		{"\tFOO\n\nBAR", "foo bar"},
		{"already_clean", "already_clean"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormaliseQuery(c.in); got != c.want {
			t.Errorf("NormaliseQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestComputeScore_StaleStrongFreq pins the worked example: a
// candidate with strong frequency + diversity but a stale last-seen
// scores around 0.62, well under MinScore=0.80. Asserts the
// composite calculation matches the expected ballpark so a future
// weight tweak can't silently shift this row over the threshold.
func TestComputeScore_StaleStrongFreq(t *testing.T) {
	now := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	m := CandidateMetrics{
		RawRelevance:       0.7,
		RecallCount:        15,
		UniqueQueries:      8,
		ConsolidationCount: 3,
		LastSeenAt:         now.Add(-45 * 24 * time.Hour), // 45 days stale → recency ~0.11
		EvidenceCount:      5,
		DistinctEntryTypes: 2,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	// Spec ballpark: composite in [0.55, 0.70] for this profile.
	// Below MinScore=0.80, so Promoted=false.
	if res.Composite < 0.55 || res.Composite > 0.70 {
		t.Errorf("composite for stale-strong-freq candidate = %v, want [0.55, 0.70]", res.Composite)
	}
	if res.Promoted {
		t.Errorf("Promoted=true for sub-threshold candidate (composite=%v)", res.Composite)
	}
}
