package api

import "testing"

// BenchmarkSQLPlaceholders_10 covers the typical batch-loader size — most
// list endpoints page at 20–50 items but batch loaders fan out in groups of
// 5–15 foreign-key lookups per page.
func BenchmarkSQLPlaceholders_10(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = sqlPlaceholders(10)
	}
}

// BenchmarkSQLPlaceholders_50 covers the limit=50 default list page.
func BenchmarkSQLPlaceholders_50(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = sqlPlaceholders(50)
	}
}
