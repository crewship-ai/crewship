package orchestrator

import (
	"embed"
	"strings"
	"testing"
)

// The protected conformance lane runs a precompiled binary outside this package.
//
//go:embed testdata/codex-reconnect/*.jsonl
var codexReconnectFixtures embed.FS

func TestRunAgent_CodexRecoversFromCapturedReconnect(t *testing.T) {
	for _, fixture := range []string{
		"0.159.0-stream-recovered.jsonl",
		"0.169.0-routing-recovered.jsonl",
		"0.169.0-websocket-recovered.jsonl",
	} {
		t.Run(fixture, func(t *testing.T) {
			stream, err := codexReconnectFixtures.ReadFile("testdata/codex-reconnect/" + fixture)
			if err != nil {
				t.Fatal(err)
			}
			status, err := runInBandCase(t, "CODEX_CLI", string(stream), 0)
			if status != "completed" || err != nil {
				t.Fatalf("recovered Codex turn: status=%q err=%v", status, err)
			}
		})
	}
}

func TestRunAgent_CodexReconnectDoesNotClearFailures(t *testing.T) {
	data, err := codexReconnectFixtures.ReadFile("testdata/codex-reconnect/0.159.0-stream-recovered.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	retry, complete, found := strings.Cut(string(data), "\n")
	if !found {
		t.Fatal("fixture missing completion")
	}
	retry += "\n"
	for _, tc := range []struct {
		name, stream string
		exit         int
	}{
		{name: "incomplete retry", stream: retry},
		{name: "invalid completion", stream: retry + `{"type":"turn.completed"}` + "\n"},
		{name: "previous turn completed", stream: complete + retry},
		{name: "turn failed", stream: retry + `{"type":"turn.failed","error":{"message":"retry exhausted"}}` + "\n" + complete},
		{name: "fatal error remains sticky", stream: `{"type":"error","message":"authentication failed"}` + "\n" + retry + complete},
		{name: "unrecognized retry", stream: `{"type":"error","message":"Reconnecting... 6/5 (invalid retry shape)"}` + "\n" + complete},
		{name: "unknown structured error", stream: `{"type":"error","message":"Reconnecting... 2/5 (authentication rejected)","retryable":false}` + "\n" + complete},
		{name: "nested error", stream: `{"type":"error","error":{"message":"Reconnecting... 2/5 (unknown envelope)"}}` + "\n" + complete},
		{name: "nonzero exit", stream: retry + complete, exit: 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := runInBandCase(t, "CODEX_CLI", tc.stream, tc.exit)
			if status != "error" || err == nil {
				t.Fatalf("expected failed turn, status=%q err=%v", status, err)
			}
		})
	}
}
