package chatbridge

import (
	"fmt"
	"net/url"
	"testing"
)

// These benchmarks contrast the two URL shapes at ResolveChat's hot spot:
// an fmt.Sprintf with two %s substitutions vs. a plain multi-string concat
// (which Go's compiler fuses into a single allocation sized to the total
// length). ResolveChat fires per chat message, so per-call overhead and
// allocation count add up.

const (
	benchBaseURL = "http://127.0.0.1:8080"
	benchChatID  = "chat-12345678-abcd-ef01-2345-6789abcdef01"
)

func BenchmarkResolveURL_Sprintf(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("%s/api/v1/internal/chats/%s/resolve", benchBaseURL, url.PathEscape(benchChatID))
	}
}

func BenchmarkResolveURL_Concat(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = benchBaseURL + "/api/v1/internal/chats/" + url.PathEscape(benchChatID) + "/resolve"
	}
}
