package main

import "testing"

// An endpoint that is not an agent's has no agent to name; it is not deleted.
func TestWorkEndpointCell_SaysDeletedOnlyForAnAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent *workLedgerAgent
		kind  string
		id    string
		want  string
	}{
		{"live agent", &workLedgerAgent{Name: "Casey"}, "agent", "a1", "Casey"},
		{"soft-deleted agent", &workLedgerAgent{Name: "Robin", Deleted: true}, "agent", "a2", "(deleted) Robin"},
		{"agent with no record", nil, "agent", "a3", "(deleted) a3"},
		{"routine endpoint", nil, "routine", "rt-1", "routine rt-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workEndpointCell(tc.agent, tc.kind, tc.id); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
