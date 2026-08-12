package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Four drivers assembled a run's terminal metadata inline and drifted: the
// chat path merged result usage only on COMPLETED, and the assignment and
// peer-query paths captured result metadata with CaptureResultMeta and then
// merged only the session-init half. So permission_denials was recorded on a
// completed chat run and missing from the failed one — the case it was added
// for — and missing entirely from delegated work.
//
// One call decides what a terminal record carries, so the next key added
// applies to every dispatch path and every terminal status instead of to
// whichever copies someone remembered.
func TestMergeRunAccumulator(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","model":"claude-opus-4-8",` +
		`"claude_code_version":"2.1.219","session_id":"sess-1","apiKeySource":"none",` +
		`"permissionMode":"bypassPermissions"}`
	const resultLine = `{"type":"result","subtype":"success","is_error":false,` +
		`"total_cost_usd":0.02,"num_turns":3,"usage":{"input_tokens":10,"output_tokens":5},` +
		`"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}]}`

	feed := func(acc *Accumulator, h EventHandler, lines ...string) {
		for _, l := range lines {
			parseClaudeCodeStreamJSON([]byte(l), h)
		}
	}

	cases := []struct {
		name           string
		lines          []string
		requested      string
		wantModel      string
		wantKeys       []string
		wantMissing    []string
		wantEffectiveM string
	}{
		{
			name:           "a completed run carries provenance, usage and denials",
			lines:          []string{initLine, resultLine},
			requested:      "claude-sonnet-4-5",
			wantKeys:       []string{"cli_version", "session_id", "permission_mode", "api_key_source", "total_cost_usd", "num_turns", "usage", "permission_denials", "model"},
			wantEffectiveM: "claude-opus-4-8",
		},
		{
			// The failing run is the one an operator reads for "why did it do
			// nothing", so it must carry the denials too — this is the case
			// the inline copies dropped.
			name:           "a run that produced only an init still carries provenance",
			lines:          []string{initLine},
			requested:      "claude-sonnet-4-5",
			wantKeys:       []string{"cli_version", "session_id", "model"},
			wantMissing:    []string{"permission_denials", "total_cost_usd"},
			wantEffectiveM: "claude-opus-4-8",
		},
		{
			// A non-Claude adapter reports none of this. Absence has to stay
			// absence: a gate keys off it, and the requested model is not an
			// answer to "what actually served the run".
			name:           "an adapter that reports nothing adds nothing",
			lines:          nil,
			requested:      "gpt-5",
			wantMissing:    []string{"cli_version", "model", "permission_denials", "total_cost_usd"},
			wantEffectiveM: "gpt-5",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, acc := NewBufferingHandler(BufferingHandlerOpts{CaptureResultMeta: true})
			feed(acc, h, tc.lines...)

			dst := map[string]any{"duration_ms": int64(12)}
			got := MergeRunAccumulator(dst, acc, tc.requested)

			if got != tc.wantEffectiveM {
				t.Errorf("effective model = %q, want %q", got, tc.wantEffectiveM)
			}
			for _, k := range tc.wantKeys {
				if _, ok := dst[k]; !ok {
					t.Errorf("missing %q; got %v", k, dst)
				}
			}
			for _, k := range tc.wantMissing {
				if v, ok := dst[k]; ok {
					t.Errorf("%q = %v, want absent", k, v)
				}
			}
			if dst["duration_ms"] != int64(12) {
				t.Errorf("caller's own keys were clobbered: %v", dst)
			}
			// The denied tool INPUT must never survive: this map becomes a
			// hash-chained journal payload.
			blob, _ := json.Marshal(dst)
			if strings.Contains(string(blob), "rm -rf") {
				t.Errorf("denied tool input reached the run record: %s", blob)
			}
		})
	}
}

// A nil accumulator is the early-dispatch path: the run failed before an agent
// ever started. It must not panic and must not invent a model.
func TestMergeRunAccumulator_NilAccumulator(t *testing.T) {
	dst := map[string]any{"duration_ms": int64(3)}
	if got := MergeRunAccumulator(dst, nil, "claude-opus-4-8"); got != "claude-opus-4-8" {
		t.Errorf("effective model = %q, want the requested one", got)
	}
	if len(dst) != 1 {
		t.Errorf("a nil accumulator contributed keys: %v", dst)
	}
}
