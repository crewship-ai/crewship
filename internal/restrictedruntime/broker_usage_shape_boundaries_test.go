//go:build linux

package restrictedruntime

import (
	"strings"
	"testing"
)

func TestBrokerUsageRetainsDebitForAmbiguousTerminalFrames(t *testing.T) {
	valid := `data: {"type":"response.completed","response":{"status":"completed","model":"pinned","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}` + "\n\n"
	for _, tc := range []struct{ name, wire string }{
		{"truncated frame", strings.TrimSuffix(valid, "\n")},
		{"trailing JSON", strings.Replace(valid, "\n\n", " {}\n\n", 1)},
		{"array event", "data: []\n\n"},
		{"failed after completion", valid + "data: {\"type\":\"response.failed\"}\n\n"},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n"},
		{"provider error", "data: {\"type\":\"error\"}\n\n"},
		{"missing usage", strings.Replace(valid, `"usage":`, `"other":`, 1)},
		{"details scalar", strings.Replace(valid, `"total_tokens":3`, `"total_tokens":3,"input_tokens_details":false`, 1)},
		{"missing cached count", strings.Replace(valid, `"total_tokens":3`, `"total_tokens":3,"input_tokens_details":{}`, 1)},
		{"string count", strings.Replace(valid, `"input_tokens":2`, `"input_tokens":"2"`, 1)},
		{"fractional count", strings.Replace(valid, `"input_tokens":2`, `"input_tokens":2.5`, 1)},
		{"oversize count", strings.Replace(valid, `"input_tokens":2`, `"input_tokens":1073741825`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalResponsesUsage([]byte(tc.wire), "pinned"); got != (BrokerUsage{}) {
				t.Fatalf("ambiguous upstream usage reduced reserved debit: %+v", got)
			}
		})
	}
	// SSE comments, multiple data lines and CRLF are framing, not extra verdicts.
	wire := ": heartbeat\r\n\r\n" + strings.Replace(strings.ReplaceAll(valid, "\n", "\r\n"), `,"response":`, ",\r\ndata: \"response\":", 1) + "data: [DONE]\r\n\r\n"
	if got := terminalResponsesUsage([]byte(wire), "pinned"); got != (BrokerUsage{Known: true, InputTokens: 2, OutputTokens: 1}) {
		t.Fatalf("valid multiline terminal usage lost: %+v", got)
	}
}
