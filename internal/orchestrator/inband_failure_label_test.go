package orchestrator

import (
	"strings"
	"testing"
)

// A Claude Code result envelope can carry subtype "success" and is_error true
// at the same time — a hard authentication failure on 2.1.226 does exactly
// that, with terminal_reason "api_error" and api_error_status 401:
//
//	{"type":"result","subtype":"success","is_error":true,
//	 "terminal_reason":"api_error","api_error_status":401,
//	 "result":"Not logged in · Please run /login"}
//
// Keying the label on subtype alone produced "agent reported a failed run
// (success)", which reads as a parser bug and sends the operator to the journal
// to find out what actually happened. terminal_reason names the cause.
func TestInBandFailure_LabelsFromTerminalReason(t *testing.T) {
	cases := []struct {
		name     string
		meta     map[string]interface{}
		content  string
		want     []string
		wantNot  []string
		wantNone bool
	}{
		{
			name: "auth failure labels api_error, never success",
			meta: map[string]interface{}{
				"subtype": "success", "is_error": true,
				"terminal_reason": "api_error", "api_error_status": 401,
			},
			content: "Not logged in · Please run /login",
			want:    []string{"api_error", "401", "Not logged in"},
			wantNot: []string{"(success)"},
		},
		{
			// The same auth failure, captured live on dev1 from the CLI the
			// containers actually run (2.1.204): terminal_reason is
			// "completed" there, which names a cause no better than
			// "success" does. The status code is the only thing left.
			name: "2.1.204 shape — completed + 401 falls back to the status",
			meta: map[string]interface{}{
				"subtype": "success", "is_error": true,
				"terminal_reason": "completed", "api_error_status": 401,
			},
			content: "Failed to authenticate. API Error: 401 API key is invalid.",
			want:    []string{"401", "API key is invalid"},
			wantNot: []string{"(success)", "(completed", "completed 401"},
		},
		{
			name: "a real error subtype still wins over terminal_reason",
			meta: map[string]interface{}{
				"subtype": "error_during_execution", "is_error": true,
				"terminal_reason": "api_error",
			},
			content: "tool loop aborted",
			want:    []string{"error_during_execution", "tool loop aborted"},
		},
		{
			name: "turn cap keeps its own message",
			meta: map[string]interface{}{
				"subtype": maxTurnsSubtype, "is_error": true,
				"terminal_reason": "max_turns", "num_turns": 20,
			},
			content: "partial answer",
			want:    []string{"turn cap", "20"},
			wantNot: []string{"partial answer"},
		},
		{
			name: "terminal_reason alone, no message",
			meta: map[string]interface{}{
				"subtype": "success", "is_error": true,
				"terminal_reason": "refusal",
			},
			want:    []string{"refusal"},
			wantNot: []string{"(success)"},
		},
		{
			name:     "a clean run stays clean",
			meta:     map[string]interface{}{"subtype": "success", "is_error": false, "terminal_reason": "success"},
			content:  "done",
			wantNone: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f inBandFailure
			f.observe(AgentEvent{Type: "result", Content: tc.content, Metadata: tc.meta})

			err := f.Err()
			if tc.wantNone {
				if err != nil {
					t.Fatalf("Err() = %v, want nil — is_error was false", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Err() = nil, want a failure")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Err() = %q, want it to mention %q", err, want)
				}
			}
			for _, notWant := range tc.wantNot {
				if strings.Contains(err.Error(), notWant) {
					t.Errorf("Err() = %q, must not mention %q", err, notWant)
				}
			}
		})
	}
}
