package statuses

import "testing"

func TestIsValidTransition(t *testing.T) {
	tests := []struct {
		name        string
		transitions map[string][]string
		current     string
		target      string
		want        bool
	}{
		// Issue transitions
		{"issue: BACKLOG→TODO", ValidIssueTransitions, "BACKLOG", "TODO", true},
		{"issue: BACKLOG→DONE", ValidIssueTransitions, "BACKLOG", "DONE", false},
		{"issue: IN_PROGRESS→REVIEW", ValidIssueTransitions, "IN_PROGRESS", "REVIEW", true},
		{"issue: DONE→BACKLOG", ValidIssueTransitions, "DONE", "BACKLOG", true},
		{"issue: DONE→IN_PROGRESS", ValidIssueTransitions, "DONE", "IN_PROGRESS", false},
		{"issue: DUPLICATE→anything", ValidIssueTransitions, "DUPLICATE", "BACKLOG", false},
		{"issue: unknown→TODO", ValidIssueTransitions, "UNKNOWN", "TODO", false},

		// Mission transitions
		{"mission: PLANNING→IN_PROGRESS", ValidMissionTransitions, "PLANNING", "IN_PROGRESS", true},
		{"mission: PLANNING→REVIEW", ValidMissionTransitions, "PLANNING", "REVIEW", false},
		{"mission: REVIEW→COMPLETED", ValidMissionTransitions, "REVIEW", "COMPLETED", true},

		// Task transitions
		{"task: PENDING→IN_PROGRESS", ValidTaskTransitions, "PENDING", "IN_PROGRESS", true},
		{"task: PENDING→COMPLETED", ValidTaskTransitions, "PENDING", "COMPLETED", false},
		{"task: IN_PROGRESS→COMPLETED", ValidTaskTransitions, "IN_PROGRESS", "COMPLETED", true},
		{"task: BLOCKED→PENDING", ValidTaskTransitions, "BLOCKED", "PENDING", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidTransition(tt.transitions, tt.current, tt.target)
			if got != tt.want {
				t.Errorf("IsValidTransition(%q, %q) = %v, want %v", tt.current, tt.target, got, tt.want)
			}
		})
	}
}

func TestAllTransitionMapsHaveEntries(t *testing.T) {
	if len(ValidIssueTransitions) == 0 {
		t.Error("ValidIssueTransitions is empty")
	}
	if len(ValidMissionTransitions) == 0 {
		t.Error("ValidMissionTransitions is empty")
	}
	if len(ValidTaskTransitions) == 0 {
		t.Error("ValidTaskTransitions is empty")
	}
}
