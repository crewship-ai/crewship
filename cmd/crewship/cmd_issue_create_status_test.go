package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestIssueCreateCLI_StatusFlag is the CLI half of #2426: `issue create
// --status TODO` must reach the server and the row must start in TODO,
// not the BACKLOG the create handler used to hard-code. Runs against
// the real router (setupIssuePrefixServer), so it also proves the
// server-side validation: a status that is not one step from BACKLOG
// is a 400 the CLI reports, not a silent BACKLOG.
func TestIssueCreateCLI_StatusFlag(t *testing.T) {
	saveCLIState(t)
	db := setupIssuePrefixServer(t)

	declare := func(c *cobra.Command) {
		prefixDeclareIssueCreateFlags(c)
		c.Flags().String("status", "", "")
	}

	c := covFreshCmd(issueCreateCmd, declare)
	covSetFlagsCli4(t, c, map[string]string{"crew": "engineering", "title": "starts in todo", "status": "TODO"})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("`issue create --status TODO`: %v (output: %s)", err, out)
	}
	var stored string
	if err := db.QueryRow(`SELECT status FROM missions WHERE title = 'starts in todo'`).Scan(&stored); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if stored != "TODO" {
		t.Fatalf("stored status = %q, want TODO — the flag never reached the server", stored)
	}

	c = covFreshCmd(issueCreateCmd, declare)
	covSetFlagsCli4(t, c, map[string]string{"crew": "engineering", "title": "cannot start done", "status": "DONE"})
	_, err = covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("`issue create --status DONE` should fail naming status, got %v", err)
	}
}
