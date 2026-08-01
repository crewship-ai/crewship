package main

import "testing"

func boolPtr(b bool) *bool { return &b }

// TestNetworkModeDisplay_MarksAModeTheServerSaysIsNotEnforced is the CLI half
// of #1648. `crewship crew get` printed the configured egress mode and nothing
// else, so it agreed with the crews row and the dashboard about a fence that
// no provider was applying. An operator reading it has to be able to tell the
// two apart without knowing which container runtime the instance uses.
func TestNetworkModeDisplay_MarksAModeTheServerSaysIsNotEnforced(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		enforced *bool
		want     string
	}{
		{"unenforced restricted is marked", "restricted", boolPtr(false), "restricted (NOT ENFORCED)"},
		{"enforced restricted reads plainly", "restricted", boolPtr(true), "restricted"},
		{"free is never marked", "free", boolPtr(true), "free"},
		{
			// An older daemon does not send the field. Rendering the zero
			// value would mark every crew on every legacy server.
			"server that does not report it says nothing", "restricted", nil, "restricted",
		},
		{"empty mode still defaults to free", "", nil, "free"},
		{"unenforced free would still be marked if a provider ever said so", "free", boolPtr(false), "free (NOT ENFORCED)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := networkModeDisplay(tc.mode, tc.enforced); got != tc.want {
				t.Errorf("networkModeDisplay(%q, %v) = %q, want %q", tc.mode, tc.enforced, got, tc.want)
			}
		})
	}
}
