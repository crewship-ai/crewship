package main

import "testing"

// The four-eyes notice names the agent whose OWNER is refused, and on a keeper
// credential escalation that is not the sender: the keeper raises the item, so
// SenderName reads "Keeper" while the server compares the approver against
// agents.created_by_user_id of the agent that ASKED.
//
// On dev2 the CLI printed "whoever owns Keeper cannot resolve it", which names
// nobody — the Keeper has no owner, and Riley does. A security notice pointing
// at the wrong party is as bad as one that never renders: it sends the reader to
// the wrong person.
func TestFourEyesAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
		sender  string
		want    string
	}{
		{
			"the keeper raised it, so the payload names the real agent",
			map[string]any{"agent_name": "Riley", "request_type": "access"},
			"Keeper",
			"Riley",
		},
		{
			"escalations-backed flow: sender IS the agent",
			map[string]any{"escalation_type": "CREDENTIAL"},
			"casey",
			"casey",
		},
		{
			"an empty agent_name is not an answer",
			map[string]any{"agent_name": ""},
			"casey",
			"casey",
		},
		{
			"a non-string agent_name is not an answer either",
			map[string]any{"agent_name": 42},
			"casey",
			"casey",
		},
		{
			"neither known: describe rather than name nobody",
			nil,
			"",
			"the agent that raised this",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fourEyesAgent(tc.payload, tc.sender); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
