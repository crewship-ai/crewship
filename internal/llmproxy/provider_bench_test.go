package llmproxy

import (
	"io"
	"log/slog"
	"testing"
)

// BenchmarkTokenPoolSelect exercises TokenPool.SelectToken with a realistic
// pool (3 workspaces × 3 providers × 3 connections = 9 active tokens). This
// runs on every LLM request routed through the llmproxy server.
func BenchmarkTokenPoolSelect(b *testing.B) {
	pool := NewTokenPool(slog.New(slog.NewTextHandler(io.Discard, nil)))
	var conns []ProviderConnection
	for _, ws := range []string{"ws1", "ws2", "ws3"} {
		for _, provider := range []ProviderType{ProviderAnthropic, ProviderOpenAI, ProviderGoogle} {
			for i := 0; i < 3; i++ {
				conns = append(conns, ProviderConnection{
					ID:          ws + "-" + string(provider) + "-" + string(rune('a'+i)),
					WorkspaceID: ws,
					Provider:    provider,
					Status:      StatusActive,
				})
			}
		}
	}
	pool.Update(conns)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pool.SelectToken("ws2", ProviderAnthropic)
	}
}
