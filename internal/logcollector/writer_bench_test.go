package logcollector

import (
	"log/slog"
	"testing"
	"time"
)

// BenchmarkWriterAppend exercises the steady-state hot path: file handle is
// already cached in the files map, we only pay for validation + marshal +
// key lookup + write. Mirrors production where a single agent writes many
// entries to its own JSONL file during a run.
func BenchmarkWriterAppend(b *testing.B) {
	dir := b.TempDir()
	w := NewWriter(dir, slog.Default())
	b.Cleanup(func() { w.Close() })

	entry := LogEntry{
		Timestamp: time.Date(2026, 4, 17, 2, 0, 0, 0, time.UTC),
		Level:     "info",
		Agent:     "anna",
		Event:     "text",
		Content:   "The agent made progress on step 2/5 of the mission.",
	}

	// Warm the file-handle cache so the first call doesn't dominate.
	if err := w.Append("crew-bench", "agent-bench", entry); err != nil {
		b.Fatalf("warm-up append: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.Append("crew-bench", "agent-bench", entry); err != nil {
			b.Fatalf("append: %v", err)
		}
	}
}
