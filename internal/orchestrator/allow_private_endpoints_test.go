package orchestrator

import "testing"

// TestEffectiveAllowPrivateEndpoints locks the #974-S5 rule: a crew's
// private-endpoint opt-in is inert unless the instance-level cap is also on.
// This is the multi-tenant ceiling — a workspace ADMIN cannot self-grant
// egress into the host's private network on a hosted instance where the
// operator hasn't opted in.
func TestEffectiveAllowPrivateEndpoints(t *testing.T) {
	cases := []struct {
		name        string
		crewFlag    bool
		instanceCap bool
		want        bool
	}{
		{"both off", false, false, false},
		{"crew on, instance off (self-grant blocked)", true, false, false},
		{"crew off, instance on", false, true, false},
		{"both on (allowed)", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveAllowPrivateEndpoints(tc.crewFlag, tc.instanceCap); got != tc.want {
				t.Fatalf("effectiveAllowPrivateEndpoints(%v, %v) = %v, want %v",
					tc.crewFlag, tc.instanceCap, got, tc.want)
			}
		})
	}
}
