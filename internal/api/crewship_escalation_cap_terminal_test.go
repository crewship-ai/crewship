package api

// The escalation backlog cap counts rows that are still outstanding, and it
// does so with a NOT IN over the terminal statuses so a status nobody thought
// about counts as outstanding and the cap fails closed.
//
// That is the right default and it is also a standing obligation: when the
// vocabulary grew (EXPIRED, CANCELLED — see escalation_lifecycle.go), a list
// left at just RESOLVED would have let a crew's budget fill with dead
// questions and refuse every new one from then on. This test is what makes the
// obligation fail loudly instead of quietly.

import (
	"context"
	"testing"
)

func TestCountPendingEscalationsIgnoresTerminalStates(t *testing.T) {
	cases := []struct {
		name   string
		status string
		// outstanding is whether a row in this state should hold budget.
		outstanding bool
	}{
		{"pending questions hold the budget", escalationStatusPending, true},
		{"a decided question does not", escalationStatusResolved, false},
		{"an expired question does not — nobody is going to answer it now", escalationStatusExpired, false},
		{"a withdrawn question does not", escalationStatusCancelled, false},
		// The fail-closed direction, still intact: an unrecognised status is
		// treated as outstanding rather than assumed finished.
		{"an unknown status is assumed outstanding", "SOME_FUTURE_STATE", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			crewID := seedCrewRow(t, db, "cap-crew", wsID, "Crew", "cap-crew")
			agentID := seedAgentRow(t, db, "cap-agent", wsID, crewID, "Agent", "cap-agent", "AGENT")
			execOrFatal(t, db, `INSERT INTO escalations
				(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
				VALUES ('cap-esc', ?, ?, 'cap-chat', ?, 'r', 'TEXT', ?, datetime('now'))`,
				wsID, crewID, agentID, tc.status)

			n, err := countPendingEscalations(context.Background(), db, crewID)
			if err != nil {
				t.Fatalf("countPendingEscalations: %v", err)
			}
			want := 0
			if tc.outstanding {
				want = 1
			}
			if n != want {
				t.Errorf("count = %d, want %d for status %q", n, want, tc.status)
			}
		})
	}
}
