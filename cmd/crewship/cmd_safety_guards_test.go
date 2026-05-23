package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestDestructiveCommandsHaveYesFlag is a regression guard. Each command in
// the matrix mutates or revokes state in a way that has no undo. They all
// must accept the standard --yes/-y flag so they pair with confirmAction()
// the same way the rest of the CLI does. If a future refactor drops the
// flag, this test catches it before users discover it the hard way.
func TestDestructiveCommandsHaveYesFlag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"pipeline delete", pipelineDeleteCmd},
		{"notification delete", notificationDeleteCmd},
		{"token revoke", tokenRevokeCmd},
		{"session revoke", sessionRevokeCmd},
		{"expose revoke", exposeRevokeCmd},
		{"integration remove", intgRemoveCmd},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := tc.cmd.Flags().Lookup("yes")
			if f == nil {
				t.Fatalf("%s: missing --yes flag", tc.name)
			}
			if f.Shorthand != "y" {
				t.Errorf("%s: --yes shorthand = %q, want \"y\"", tc.name, f.Shorthand)
			}
			if f.DefValue != "false" {
				t.Errorf("%s: --yes default = %q, want \"false\" (prompt by default)", tc.name, f.DefValue)
			}
		})
	}
}
