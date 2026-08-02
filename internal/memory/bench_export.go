//go:build memorybench

package memory

// SanitizeFTSQueryForBench exposes the unexported query sanitiser to
// scripts/memory-retrieval-bench so the benchmark measures the SHIPPING
// expression builder rather than a copy of it that can drift.
//
// The `memorybench` build tag keeps this out of every normal build, every
// test run and the shipped binary. It exists only so a measurement cannot
// quietly stop measuring production code.
func SanitizeFTSQueryForBench(q string) string { return sanitizeFTSQuery(q) }
