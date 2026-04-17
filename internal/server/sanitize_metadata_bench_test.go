package server

import "testing"

// benchSanitizeSink forces the sanitizeMetadata result to escape so the
// compiler can't stack-allocate the returned map — production usage feeds
// the map into a cross-goroutine WS broadcast, so the heap allocation is
// real; a package-level sink reproduces that in the benchmark.
var benchSanitizeSink map[string]interface{}

// BenchmarkSanitizeMetadata_Typical mirrors the actual hot-path shape:
// an agent "result" event with 4–6 keys, of which most are on the allowlist.
// sanitizeMetadata runs on every AgentEvent that feeds the "agent.log"
// workspace broadcast, so its per-call cost scales with token streaming.
func BenchmarkSanitizeMetadata_Typical(b *testing.B) {
	raw := map[string]interface{}{
		"source":         "claude_code",
		"duration_ms":    12345,
		"tool_name":      "Bash",
		"total_cost_usd": 0.0021,
		"model":          "claude-sonnet-4-6",
		"session_id":     "sess_abcdef1234567890",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSanitizeSink = sanitizeMetadata(raw)
	}
}

// BenchmarkSanitizeMetadata_Empty is the nil/wrong-type fast path — tests
// that the common no-metadata event isn't slowed down.
func BenchmarkSanitizeMetadata_Empty(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchSanitizeSink = sanitizeMetadata(nil)
	}
}
