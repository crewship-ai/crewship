package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// #3590: spend reads are OWNER/ADMIN on the server. The CLI must surface the
// server's 403 reason instead of an empty table, for every spend command.
func TestSpendCommands_ForbiddenSurfacesReason(t *testing.T) {
	const reason = "Spend requires OWNER or ADMIN"
	cases := []struct {
		name string
		path string
		run  func() error
	}{
		{"cost", "/api/v1/paymaster/top-spenders", func() error { return costCmd.RunE(costCmd, nil) }},
		{"spend", "/api/v1/journal/spend", func() error { return spendCmd.RunE(spendCmd, nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			s.OnGet(c.path, clitest.ErrorResponse(403, reason))
			covSetupCli10(t, s.URL())
			err := c.run()
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("err = %v, want it to contain %q", err, reason)
			}
		})
	}
}
