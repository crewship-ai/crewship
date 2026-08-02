//go:build memorybench

package episodic

// EscapeFTSQueryForBench exposes the unexported episodic query builder to
// scripts/memory-retrieval-bench so the benchmark measures the SHIPPING
// expression builder rather than a copy that can drift.
//
// The `memorybench` build tag keeps this out of every normal build, every
// test run and the shipped binary.
func EscapeFTSQueryForBench(s string) string { return escapeFTSQuery(s) }
