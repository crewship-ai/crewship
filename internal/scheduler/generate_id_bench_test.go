package scheduler

import "testing"

// BenchmarkGenerateID measures the scheduler's chat+run ID generator.
// Called 2× per scheduled agent trigger (chat ID + run ID). Under a
// workspace with many cron-triggered agents this accumulates a measurable
// allocation rate.
func BenchmarkGenerateID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = generateID()
	}
}
