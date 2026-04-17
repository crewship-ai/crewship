package ws

import (
	"encoding/json"
	"testing"
)

// BenchmarkHubPingMarshal_Inline mirrors the old hot path:
// json.Marshal(ServerMessage{Type: ..., Payload: nil}) executed on every
// server-initiated ping tick (per Client every 30 s) and every client ping
// (server replies with a fresh "pong"). This benchmark measures what the
// cached-bytes fix eliminates.
func BenchmarkHubPingMarshal_Inline(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(ServerMessage{Type: "ping", Payload: nil})
	}
}

// BenchmarkHubPingMarshal_Cached represents the new hot path: a precomputed
// []byte is read from a package-level var with zero allocations per use.
func BenchmarkHubPingMarshal_Cached(b *testing.B) {
	var sink []byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink = pingMessageBytes
	}
	_ = sink
}
