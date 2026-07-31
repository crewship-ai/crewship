package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// keeperFindingsCmd drives POST /api/v1/admin/keeper/findings/test — the
// routing check for Keeper's findings.
//
// Keeper writes an inbox item when it escalates, and on a high-risk DENY. That
// path has always worked; what nobody could do is CONFIRM it before an incident
// did the confirming. A security contact pointing at a member who has since left,
// or a workspace with nobody holding MANAGER, is silent until the night it
// matters. This sends one synthetic finding down the same path and prints who it
// reached.
var keeperFindingsCmd = &cobra.Command{
	Use:   "findings",
	Short: "Verify that Keeper's findings actually reach a human",
	Long: `Check where a Keeper finding would land (requires OWNER or ADMIN).

Keeper routes a finding to the workspace's named security contact and fans it out
to everyone with MANAGER or above. 'findings test' sends one synthetic finding
through that exact path — same inbox writer, same target resolution, same
realtime push — and prints the recipients it resolved.

No model is called, so it costs nothing and the judge does not need to be running.

Examples:
  crewship keeper findings test`,
}

// keeperFindingsRecipient mirrors the response shape of
// internal/api/admin_keeper_findings.go.
type keeperFindingsRecipient struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Reason string `json:"reason"`
}

type keeperFindingsTestResult struct {
	InboxItemID           string                    `json:"inbox_item_id"`
	Recipients            []keeperFindingsRecipient `json:"recipients"`
	SecurityContactUserID string                    `json:"security_contact_user_id"`
	Warning               string                    `json:"warning"`
}

var keeperFindingsTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send a test finding and report who it reaches (requires OWNER or ADMIN)",
	Long: `Send one synthetic finding through Keeper's real findings path and print the
recipients. The item is clearly labelled as a test, is non-blocking, and carries
a test flag in its payload — but it is a real inbox item, written by the same
code a real escalation uses.

Exits non-zero when the finding resolved to nobody: a finding with no audience is
a security control that is switched on and unheard.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperFindingsTestResult
		if err := postJSON(client, "/api/v1/admin/keeper/findings/test", map[string]any{}, &out); err != nil {
			return keeperPermissionHint(err)
		}

		if err := newFormatter().AutoHuman(out, func() {
			if len(out.Recipients) == 0 {
				cli.PrintError("Test finding sent — and it reached nobody.")
			} else {
				cli.PrintSuccess(fmt.Sprintf("Test finding sent — it reaches %d %s.",
					len(out.Recipients), pluralRecipients(len(out.Recipients))))
			}
			if out.Warning != "" {
				fmt.Printf("%s%s%s\n", cli.Yellow, out.Warning, cli.Reset)
			}
			fmt.Printf("%sRecipients%s\n", cli.Bold, cli.Reset)
			for _, r := range out.Recipients {
				who := r.Email
				if who == "" {
					who = r.Name
				}
				if who == "" {
					who = r.UserID
				}
				role := r.Role
				if role == "" {
					role = "—"
				}
				fmt.Printf("  %-32s %-8s %s\n", who, role, cli.Dim+r.Reason+cli.Reset)
			}
			fmt.Printf("%sInbox item: %s%s\n", cli.Dim, out.InboxItemID, cli.Reset)
		}); err != nil {
			return err
		}
		// A finding nobody can see is a failed check, not a successful send —
		// the exit code is what a harness or a cron reads.
		if len(out.Recipients) == 0 {
			return cli.WithExitCode(
				fmt.Errorf("the test finding resolved to no recipients — set a security contact or give somebody MANAGER"),
				cli.ExitGeneric)
		}
		return nil
	},
}

func pluralRecipients(n int) string {
	if n == 1 {
		return "person"
	}
	return "people"
}

func init() {
	keeperFindingsCmd.AddCommand(keeperFindingsTestCmd)
	keeperCmd.AddCommand(keeperFindingsCmd)
}
