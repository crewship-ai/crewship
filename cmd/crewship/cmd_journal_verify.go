package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// journalVerifyCmd walks the current workspace's audit hash-chain and reports
// whether it is intact. It is the CLI half of GET /api/v1/admin/journal/verify
// (issue #1369) — the tamper-evidence check for the append-only journal.
//
// Exit code is load-bearing: a broken chain returns a non-nil error so the
// process exits non-zero, letting an operator's cron / the test-harness assert
// integrity without parsing stdout.
var journalVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify the audit journal hash-chain is intact (tamper-evidence)",
	Long: `Walk the current workspace's journal hash-chain and report whether it is
intact. Each journal entry commits to its own content plus the hash of the
preceding entry, so any after-the-fact edit, in-place reorder, or deletion of
a middle row breaks the chain and is detected here.

Requires ADMIN or OWNER. Exits non-zero if the chain is broken.

Examples:
  crewship journal verify
  crewship journal verify --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/journal/verify")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var res struct {
			WorkspaceID string `json:"workspace_id"`
			OK          bool   `json:"ok"`
			Count       int    `json:"count"`
			BrokenSeq   int64  `json:"broken_seq"`
			BrokenID    string `json:"broken_id"`
			Reason      string `json:"reason"`
		}
		if err := cli.ReadJSON(resp, &res); err != nil {
			return err
		}

		f := newFormatter()
		if err := f.AutoHuman(res, func() {
			if res.OK {
				fmt.Printf("Journal chain OK — %d entries verified, no tampering detected.\n", res.Count)
				return
			}
			fmt.Printf("Journal chain BROKEN at seq %d (entry %s): %s\n", res.BrokenSeq, res.BrokenID, res.Reason)
			fmt.Printf("Verified %d entries before the break.\n", res.Count)
		}); err != nil {
			return err
		}

		// Non-zero exit on a broken chain so cron / the test-harness can
		// assert integrity without parsing output (holds in every format).
		if !res.OK {
			return fmt.Errorf("audit journal integrity check failed")
		}
		return nil
	},
}

func init() {
	journalCmd.AddCommand(journalVerifyCmd)
}
