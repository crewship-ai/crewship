package main

import (
	"fmt"
	"os"
	"text/tabwriter"

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

		// Mirrors journal.VerifyResult. The machine formats re-serialise THIS
		// struct, so a field missing here is a field the harness never sees.
		var res struct {
			WorkspaceID string `json:"workspace_id"`
			OK          bool   `json:"ok"`
			Count       int    `json:"count"`
			Checkpoints int    `json:"checkpoints"`
			BrokenSeq   int64  `json:"broken_seq"`
			BrokenID    string `json:"broken_id"`
			Reason      string `json:"reason"`
			Breaks      []struct {
				Seq    int64  `json:"seq"`
				ID     string `json:"id"`
				Kind   string `json:"kind"`
				Reason string `json:"reason"`
			} `json:"breaks,omitempty"`
			// omitempty so an OLDER server — which sends none of these — yields
			// byte-identical JSON to before. Without it the CLI would assert
			// "break_count: 0", i.e. "no further breaks", when the truth is
			// "this server never told us".
			BreakCount      int  `json:"break_count,omitempty"`
			BreaksTruncated bool `json:"breaks_truncated,omitempty"`
		}
		if err := cli.ReadJSON(resp, &res); err != nil {
			return err
		}

		f := newFormatter()
		if err := f.AutoHuman(res, func() {
			if res.OK {
				fmt.Printf("Journal chain OK — %d entries verified against the keyed HMAC chain, no tampering detected.\n", res.Count)
				if res.Checkpoints > 0 {
					fmt.Printf("%d signed compaction checkpoint(s) bridged legitimately-deleted ranges.\n", res.Checkpoints)
				}
				return
			}
			// NOT "verified N entries before the break" any more: the walk no
			// longer stops at the first one, so Count is every entry examined.
			// Saying "before" would understate what was checked and hide that
			// there may be more than one break.
			n := res.BreakCount
			if n == 0 {
				n = 1 // older server: only the single-break fields are populated
			}
			fmt.Printf("Journal chain BROKEN — %d of %d entries failed an integrity check.\n", n, res.Count)
			fmt.Printf("First break at seq %d (entry %s): %s\n", res.BrokenSeq, res.BrokenID, res.Reason)
			if len(res.Breaks) > 1 {
				shown := len(res.Breaks)
				if res.BreaksTruncated {
					fmt.Printf("\nFirst %d of %d breaks:\n", shown, res.BreakCount)
				} else {
					fmt.Printf("\nAll %d breaks:\n", shown)
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "SEQ\tKIND\tENTRY\tREASON")
				for _, b := range res.Breaks {
					reason := b.Reason
					if len(reason) > 72 {
						reason = reason[:69] + "..."
					}
					fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", b.Seq, b.Kind, b.ID, reason)
				}
				w.Flush() //nolint:errcheck // best-effort table render
			}
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
