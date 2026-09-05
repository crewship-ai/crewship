package server

// Percentile computation for the domain metrics block (B12, PRD-ISSUES-
// AND-ROUTINES-2026 §17/§19.3, finding F39).
//
// F39 is explicit that this capability does not exist anywhere in the
// codebase: internal/telemetry is OpenTelemetry tracing only, there is no
// Prometheus client in go.mod (so no histogram type), and SQLite has no
// PERCENTILE_CONT/PERCENTILE_DISC. Every p50/p95 in §19.3's SLO table is
// therefore computed here, in Go, over a bounded row window read by each
// collector — never in SQL, and never as a running/streaming estimate
// (a bounded window is cheap enough at this data volume that an
// approximation algorithm like t-digest would be solving a problem this
// codebase does not have).

import "sort"

// percentile returns the p-th percentile (0 <= p <= 1) of samples using
// the nearest-rank method: sort ascending, then take the sample at rank
// ceil(p * n) (1-indexed, clamped to [1, n]).
//
// Nearest-rank, not linear interpolation: every value this function
// returns was an actual observation, never a number invented between two
// real ones. For the sample sizes a single scrape window holds (tens to
// low hundreds of rows, per collectPercentileWindow below) the difference
// from interpolation is sub-percent and not worth a second code path to
// maintain.
//
// ok is false only when samples is empty — there is no percentile of
// nothing, and returning 0 there would be exactly the "metric claims a
// number it cannot compute" B12's accept line forbids. Callers must skip
// emitting the series entirely when ok is false, not substitute zero.
//
// Does not mutate samples: every collector below computes p50 and p95 from
// the same slice, so this makes its own copy before sorting.
func percentile(samples []float64, p float64) (value float64, ok bool) {
	n := len(samples)
	if n == 0 {
		return 0, false
	}
	sorted := make([]float64, n)
	copy(sorted, samples)
	sort.Float64s(sorted)

	rank := int(ceilPercentileRank(p, n))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1], true
}

// ceilPercentileRank computes ceil(p * n) without pulling in math.Ceil for
// a single call site — p*n is always representable exactly enough at these
// sample sizes that a small epsilon avoids float rounding from turning an
// exact boundary (e.g. p=0.5, n=10 -> 5.0) into rank 6 due to 5.000000001.
func ceilPercentileRank(p float64, n int) float64 {
	const epsilon = 1e-9
	v := p*float64(n) - epsilon
	f := float64(int(v))
	if v > f {
		return f + 1
	}
	return f
}

// percentiles50And95 is the shape every B12 latency/size collector needs:
// p50 and p95 over one sample set, plus the sample count so the caller can
// decide whether to emit the quantile series at all. Returns (0, 0, 0) for
// an empty input — callers key emission on n, not on the p50/p95 values.
func percentiles50And95(samples []float64) (p50, p95 float64, n int) {
	n = len(samples)
	if n == 0 {
		return 0, 0, 0
	}
	p50, _ = percentile(samples, 0.5)
	p95, _ = percentile(samples, 0.95)
	return p50, p95, n
}
