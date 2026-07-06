package policy

import "testing"

// TestApprovalModeForLevel pins the autonomy_level → harbormaster gate mode
// mapping the request-builder uses to revive the HITL gate (#810).
func TestApprovalModeForLevel(t *testing.T) {
	cases := []struct {
		level AutonomyLevel
		want  string
	}{
		{AutonomyStrict, ApprovalModeSync},
		{AutonomyGuided, ApprovalModeSync},
		{AutonomyTrusted, ApprovalModeAsync},
		{AutonomyFull, ApprovalModeNone},
		{AutonomyLevel("bogus"), ApprovalModeSync}, // unknown fails safe to sync
	}
	for _, c := range cases {
		if got := ApprovalModeForLevel(c.level); got != c.want {
			t.Errorf("ApprovalModeForLevel(%q) = %q, want %q", c.level, got, c.want)
		}
	}
}
