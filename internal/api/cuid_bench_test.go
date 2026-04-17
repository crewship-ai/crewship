package api

import "testing"

// BenchmarkGenerateCUID measures end-to-end CUID generation on every call.
// Every row inserted by the API goes through this path (missions, comments,
// audit logs, assignments, etc.), so steady-state allocations here show up
// as persistent GC pressure under load.
func BenchmarkGenerateCUID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = generateCUID()
	}
}
