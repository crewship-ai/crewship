package sidecar

import "testing"

// BenchmarkDomainAllowlistLookup_NoPort exercises the common case when the
// Host header lacks a :port suffix (HTTP proxy mode with relative URLs).
// The previous implementation went through net.SplitHostPort which returns
// an error — and allocates — whenever the host has no colon.
func BenchmarkDomainAllowlistLookup_NoPort(b *testing.B) {
	al := NewDomainAllowlist(DefaultAllowedDomains)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = al.IsAllowed("api.anthropic.com")
	}
}

// BenchmarkProviderForHost_NoPort mirrors the same pattern on the sibling
// helper that proxy.handleHTTP also calls for every request.
func BenchmarkProviderForHost_NoPort(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = providerForHost("api.anthropic.com")
	}
}
