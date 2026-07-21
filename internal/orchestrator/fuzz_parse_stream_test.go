package orchestrator

import (
	"embed"
	"strings"
	"testing"
)

//go:embed testdata/cli-fixtures
var cliFixturesFS embed.FS

// seedFuzzFromFixture seeds f with every non-empty line recorded from a real
// CLI run (internal/orchestrator/testdata/cli-fixtures/*.ndjson), plus a
// handful of adversarial edge cases a killed or misbehaving process could
// plausibly emit: empty input, bare JSON literals, truncated envelopes,
// oversized fields, invalid UTF-8, and an out-of-range float. Each
// ParseStreamLine implementation consumes byte-for-byte JSONL from a
// third-party binary we don't control, so these functions are the fuzz
// surface, not the fixtures themselves.
func seedFuzzFromFixture(f *testing.F, fixture string) {
	f.Helper()
	data, err := cliFixturesFS.ReadFile("testdata/cli-fixtures/" + fixture)
	if err != nil {
		f.Fatalf("read fixture %s: %v", fixture, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f.Add([]byte(line))
	}
	for _, edge := range [][]byte{
		{},
		[]byte("null"),
		[]byte("{}"),
		[]byte("["),
		[]byte(`{"type":`),
		[]byte(`{"type":"` + strings.Repeat("x", 4096) + `"}`),
		[]byte("\xff\xfe\x00"),
		[]byte(`{"type":"result","total_cost_usd":1e400}`),
	} {
		f.Add(edge)
	}
}

func FuzzParseStreamLine_Claude(f *testing.F) {
	seedFuzzFromFixture(f, "claude.ndjson")
	f.Fuzz(func(t *testing.T, line []byte) {
		parseClaudeCodeStreamJSON(line, func(AgentEvent) {})
	})
}

func FuzzParseStreamLine_Codex(f *testing.F) {
	seedFuzzFromFixture(f, "codex.ndjson")
	f.Fuzz(func(t *testing.T, line []byte) {
		parseCodexStreamJSON(line, func(AgentEvent) {})
	})
}

func FuzzParseStreamLine_Cursor(f *testing.F) {
	seedFuzzFromFixture(f, "cursor.ndjson")
	f.Fuzz(func(t *testing.T, line []byte) {
		parseCursorStreamJSON(line, func(AgentEvent) {})
	})
}

func FuzzParseStreamLine_Droid(f *testing.F) {
	seedFuzzFromFixture(f, "droid.ndjson")
	f.Fuzz(func(t *testing.T, line []byte) {
		parseDroidStreamJSON(line, func(AgentEvent) {})
	})
}

func FuzzParseStreamLine_Gemini(f *testing.F) {
	seedFuzzFromFixture(f, "gemini.ndjson")
	f.Fuzz(func(t *testing.T, line []byte) {
		parseGeminiStreamJSON(line, func(AgentEvent) {})
	})
}

func FuzzParseStreamLine_OpenCode(f *testing.F) {
	seedFuzzFromFixture(f, "opencode.ndjson")
	f.Fuzz(func(t *testing.T, line []byte) {
		newOpenCodeStreamParser().parseLine(line, func(AgentEvent) {})
	})
}
