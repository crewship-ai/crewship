package orchestrator

import "testing"

// #974 S5: private-network model egress requires BOTH the per-crew opt-in AND
// the instance-level ceiling, so a workspace admin can't self-grant private
// egress on a shared/cloud host by flipping only the per-crew flag.
func TestEffectiveAllowPrivateEndpoints_InstanceCap(t *testing.T) {
	cases := []struct {
		name     string
		crewFlag bool
		env      string
		want     bool
	}{
		{"crew on, instance unset", true, "", false},
		{"crew on, instance off", true, "false", false},
		{"crew on, instance on", true, "true", true},
		{"crew on, instance on (1)", true, "1", true},
		{"crew off, instance on", false, "true", false},
		{"both off", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				t.Setenv(allowPrivateEndpointsEnvVar, "")
			} else {
				t.Setenv(allowPrivateEndpointsEnvVar, tc.env)
			}
			if got := effectiveAllowPrivateEndpoints(tc.crewFlag); got != tc.want {
				t.Errorf("effectiveAllowPrivateEndpoints(crew=%v, env=%q) = %v, want %v", tc.crewFlag, tc.env, got, tc.want)
			}
		})
	}
}
