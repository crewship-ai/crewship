package orchestrator

import (
	"io"
	"log/slog"
	"testing"
)

// BenchmarkHandleStreamJSONLine_TextDelta measures the dominant Claude Code
// streaming event — a token-level text_delta line. streamOutput fans one of
// these out for every token the model emits, so this runs at LLM throughput
// (hundreds to thousands per second under load).
func BenchmarkHandleStreamJSONLine_TextDelta(b *testing.B) {
	o := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	line := []byte(`{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"Hello, I'll help with that."}}}`)
	handler := func(e AgentEvent) {}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.handleStreamJSONLine(line, handler)
	}
}
