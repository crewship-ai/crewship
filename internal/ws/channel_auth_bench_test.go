package ws

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// BenchmarkCanSubscribe_Parse isolates the parse/dispatch portion of
// CanSubscribe by benchmarking the "providers" channel (global, always-allow
// for authenticated users — no DB roundtrip). That way the measurement
// focuses on the per-call string parsing cost rather than sqlite latency.
func BenchmarkCanSubscribe_Parse(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	a := NewDBChannelAuthorizer(db)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.CanSubscribe(ctx, "user-123", "providers:all")
	}
}
