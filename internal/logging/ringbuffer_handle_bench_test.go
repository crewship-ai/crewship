package logging

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// nopHandler is a minimal slog.Handler that discards records; benchmarks only
// need RingHandler.Handle path, not the inner handler's cost.
type nopHandler struct{}

func (nopHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (nopHandler) Handle(context.Context, slog.Record) error { return nil }
func (nopHandler) WithAttrs([]slog.Attr) slog.Handler        { return nopHandler{} }
func (nopHandler) WithGroup(string) slog.Handler             { return nopHandler{} }

func newBenchHandler() *RingHandler {
	return NewRingHandler(nopHandler{}, NewRingBuffer(1024))
}

func BenchmarkRingHandler_Handle_NoAttrs(b *testing.B) {
	h := newBenchHandler()
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "steady-state ping", 0)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, r)
	}
}

func BenchmarkRingHandler_Handle_ThreeAttrs(b *testing.B) {
	h := newBenchHandler()
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "task completed", 0)
	r.AddAttrs(
		slog.String("task_id", "t_abc123"),
		slog.String("mission_id", "m_def456"),
		slog.Duration("elapsed", 250*time.Millisecond),
	)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, r)
	}
}

