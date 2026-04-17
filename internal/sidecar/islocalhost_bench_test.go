package sidecar

import "testing"

// BenchmarkIsLocalhost_Remote measures the typical proxy hot-path: an
// outbound request to a legitimate LLM host. With the new fast-path,
// isLocalhost short-circuits to false before calling net.ParseIP when
// the hostname obviously can't be a loopback IP, avoiding the parse
// allocation that previously dominated this benchmark.
func BenchmarkIsLocalhost_Remote(b *testing.B) {
	host := "api.anthropic.com:443"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isLocalhost(host)
	}
}

// BenchmarkIsLocalhost_RemoteNoPort mirrors the HTTP_PROXY-relative-URL
// shape (no port suffix).
func BenchmarkIsLocalhost_RemoteNoPort(b *testing.B) {
	host := "api.openai.com"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isLocalhost(host)
	}
}

// BenchmarkIsLocalhost_Positive keeps the loopback true-path honest so
// any optimization doesn't regress the sidecar's internal control-plane
// calls.
func BenchmarkIsLocalhost_Positive(b *testing.B) {
	host := "localhost:9119"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isLocalhost(host)
	}
}
