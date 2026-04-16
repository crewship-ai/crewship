package orchestrator

import "testing"

// BenchmarkBuildMemoryInstructions exercises the memory-instructions template
// that buildMemoryContext injects into every agent-run system prompt. Result
// varies only by date, so steady-state the same string is materialized over
// and over.
func BenchmarkBuildMemoryInstructions(b *testing.B) {
	const today = "2026-04-17"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildMemoryInstructions(today)
	}
}
