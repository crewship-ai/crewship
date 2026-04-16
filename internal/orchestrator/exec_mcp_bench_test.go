package orchestrator

import "testing"

// BenchmarkSanitizeMCPName measures the cost of sanitizing an MCP server/package
// name. The baseline compiles the regex on every call; the fix hoists the regex
// to a package-level var.
func BenchmarkSanitizeMCPName(b *testing.B) {
	names := []string{
		"@modelcontextprotocol/server-github",
		"@some-org/mcp-server",
		"simple-mcp-server",
		"../path/traversal/attempt",
		"foo bar baz!@#$%",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeMCPName(names[i%len(names)])
	}
}
