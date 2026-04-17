package sidecar

import "testing"

// BenchmarkIsLocalhost_Remote measures the typical proxy hot-path: an
// outbound request to a legitimate LLM host. The function currently
// falls through to net.ParseIP even though the hostname can't possibly
// be a loopback IP.
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
