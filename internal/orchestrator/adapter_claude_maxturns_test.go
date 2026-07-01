package orchestrator

import (
	"strconv"
	"testing"
)

// argAfter returns the argv element immediately following the first occurrence
// of flag, or "" if the flag is absent / trailing.
func argAfter(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func TestClaudeAdapter_MaxTurns(t *testing.T) {
	adapter := claudeCodeAdapter{}

	cases := []struct {
		name string
		req  AgentRunRequest
		want int
	}{
		{"unset defaults to DefaultMaxTurns", AgentRunRequest{}, DefaultMaxTurns},
		{"routine cap honored", AgentRunRequest{MaxTurns: RoutineMaxTurns}, RoutineMaxTurns},
		{"explicit override honored", AgentRunRequest{MaxTurns: 7}, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := argAfter(adapter.BuildCommand(tc.req), "--max-turns")
			if got != strconv.Itoa(tc.want) {
				t.Fatalf("--max-turns = %q, want %q", got, strconv.Itoa(tc.want))
			}
		})
	}
}

// RoutineMaxTurns must stay strictly below DefaultMaxTurns — the whole point is
// that unattended jobs get a tighter leash than interactive chat.
func TestRoutineMaxTurns_TighterThanDefault(t *testing.T) {
	if RoutineMaxTurns >= DefaultMaxTurns {
		t.Fatalf("RoutineMaxTurns (%d) must be < DefaultMaxTurns (%d)", RoutineMaxTurns, DefaultMaxTurns)
	}
}
