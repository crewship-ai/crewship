package server

import "testing"

// TestPercentile_KnownDistributions pins percentile's nearest-rank
// arithmetic against distributions whose p50/p95 can be checked by hand —
// F39's "percentile computation has its own tests" (B12, PRD §17). Every
// case here fails to compile before percentile exists, and once it exists
// each value is independently verifiable, not just "whatever the code
// produces".
func TestPercentile_KnownDistributions(t *testing.T) {
	t.Parallel()

	oneToN := func(n int) []float64 {
		out := make([]float64, n)
		for i := 0; i < n; i++ {
			out[i] = float64(i + 1)
		}
		return out
	}

	cases := []struct {
		name    string
		samples []float64
		p       float64
		want    float64
	}{
		{"1..100 p50", oneToN(100), 0.5, 50},
		{"1..100 p95", oneToN(100), 0.95, 95},
		{"1..10 p50", oneToN(10), 0.5, 5},
		{"1..10 p95", oneToN(10), 0.95, 10},
		{"single value p50", []float64{42}, 0.5, 42},
		{"single value p95", []float64{42}, 0.95, 42},
		{"unsorted input is sorted first", []float64{5, 1, 3, 2, 4}, 0.5, 3},
		{"duplicates count toward rank", []float64{1, 1, 1, 1, 10}, 0.95, 10},
		{"duplicates p50 stays in the cluster", []float64{1, 1, 1, 1, 10}, 0.5, 1},
		{"two values p50 takes the lower rank", []float64{10, 20}, 0.5, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := percentile(tc.samples, tc.p)
			if !ok {
				t.Fatalf("percentile(%v, %v) reported ok=false, want a value", tc.samples, tc.p)
			}
			if got != tc.want {
				t.Errorf("percentile(%v, %v) = %v, want %v", tc.samples, tc.p, got, tc.want)
			}
		})
	}
}

// TestPercentile_EmptyIsAbsentNotZero pins F39's honesty rule at the
// smallest possible unit: a percentile of zero samples is "cannot
// compute", reported as ok=false, never a fabricated 0.
func TestPercentile_EmptyIsAbsentNotZero(t *testing.T) {
	t.Parallel()
	if _, ok := percentile(nil, 0.5); ok {
		t.Error("percentile(nil, 0.5) reported ok=true; an empty sample set has no percentile")
	}
	if _, ok := percentile([]float64{}, 0.95); ok {
		t.Error("percentile([]float64{}, 0.95) reported ok=true; an empty sample set has no percentile")
	}
}

// TestPercentile_DoesNotMutateInput guards against a sort.Float64s call
// on the caller's own slice — every collector below reuses its samples
// slice for both p50 and p95, and a percentile that sorted in place would
// make the second call's "unsorted" test case above pass for the wrong
// reason (it would already be sorted from the first call).
func TestPercentile_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []float64{5, 1, 3, 2, 4}
	want := []float64{5, 1, 3, 2, 4}
	if _, ok := percentile(in, 0.5); !ok {
		t.Fatal("percentile reported ok=false for a non-empty slice")
	}
	for i := range in {
		if in[i] != want[i] {
			t.Fatalf("percentile mutated its input: got %v, want %v", in, want)
		}
	}
}

// TestPercentiles50And95_SampleCountAndValues pins the two-value shape
// every collector below actually calls: p50, p95 and the sample count
// together, so a collector can decide whether to emit the quantile series
// at all.
func TestPercentiles50And95_SampleCountAndValues(t *testing.T) {
	t.Parallel()
	p50, p95, n := percentiles50And95(oneToNFloat(20))
	if n != 20 {
		t.Errorf("n = %d, want 20", n)
	}
	if p50 != 10 {
		t.Errorf("p50 = %v, want 10", p50)
	}
	if p95 != 19 {
		t.Errorf("p95 = %v, want 19", p95)
	}

	p50, p95, n = percentiles50And95(nil)
	if n != 0 || p50 != 0 || p95 != 0 {
		t.Errorf("percentiles50And95(nil) = (%v, %v, %d), want (0, 0, 0)", p50, p95, n)
	}
}

func oneToNFloat(n int) []float64 {
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = float64(i + 1)
	}
	return out
}
