package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// journalVerifyResponse mirrors journal.VerifyResult. The machine formats
// re-serialise THIS struct, so a field missing here is a field the harness
// never sees.
type journalVerifyResponse struct {
	WorkspaceID string              `json:"workspace_id"`
	OK          bool                `json:"ok"`
	Count       int                 `json:"count"`
	Checkpoints int                 `json:"checkpoints"`
	BrokenSeq   int64               `json:"broken_seq"`
	BrokenID    string              `json:"broken_id"`
	Reason      string              `json:"reason"`
	Breaks      []journalChainBreak `json:"breaks,omitempty"`
	// omitempty so an OLDER server — which sends none of these — yields
	// byte-identical JSON to before. Without it the CLI would assert
	// "break_count: 0", i.e. "no further breaks", when the truth is
	// "this server never told us".
	BreakCount      int  `json:"break_count,omitempty"`
	BreaksTruncated bool `json:"breaks_truncated,omitempty"`
	// #1572: the server has always sent these; the CLI dropped them on the
	// floor. A repairable row is one whose CONTENT the keyed hash proves
	// authentic but whose priority column the record cannot account for —
	// indistinguishable from an attacker downgrading a `permanent` entry so
	// compaction removes it. Decoding them is half the fix; the exit code is
	// the other half.
	Repairable          []journalRepairableEntry `json:"repairable,omitempty"`
	RepairableCount     int                      `json:"repairable_count,omitempty"`
	RepairableTruncated bool                     `json:"repairable_truncated,omitempty"`
}

type journalChainBreak struct {
	Seq    int64  `json:"seq"`
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type journalRepairableEntry struct {
	Seq            int64  `json:"seq"`
	ID             string `json:"id"`
	StoredPriority string `json:"stored_priority"`
	EmitPriority   string `json:"emit_priority"`
}

// repairableTotal prefers the server's count and falls back to the length of
// the list, so a server that sends the rows without the count is not reported
// as "0 rows" — the same defensive reading the breaks list gets.
func repairableTotal(count, listed int) int {
	if count > listed {
		return count
	}
	return listed
}

// printRepairable renders the unresolved rows and what to do about them. It is
// a distinct, non-green block: the recovered emit-time priority is the value
// the keyed hash proves the entry was written with, so an operator can compare
// it against what the row claims today and decide whether they are looking at
// v166 backfill damage or at someone taking a `permanent` record out of
// compaction's exemption. Nothing in the output resolves that for them —
// nothing can, from the row alone.
func printRepairable(rows []journalRepairableEntry, count int, truncated bool) {
	total := repairableTotal(count, len(rows))
	if total == 0 {
		return
	}
	fmt.Printf("\n%d entr(y|ies) with an unresolved priority (content authentic, live priority unaccounted for):\n", total)
	if len(rows) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SEQ\tENTRY\tSTORED\tEMITTED (proven by the keyed hash)")
		for _, r := range rows {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", r.Seq, r.ID, r.StoredPriority, r.EmitPriority)
		}
		w.Flush() //nolint:errcheck // best-effort table render
	}
	if truncated {
		fmt.Printf("(showing %d of %d)\n", len(rows), total)
	}
}

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

Entries whose content is authentic but whose priority the record cannot
account for are listed separately, with the emit-time priority the keyed hash
proves they were written with.

Requires ADMIN or OWNER. Exits non-zero if the chain is broken OR if any entry
has an unresolved priority.

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

		var res journalVerifyResponse
		if err := cli.ReadJSON(resp, &res); err != nil {
			return err
		}

		// A journal carrying repairable rows is NOT a clean journal, whatever
		// `ok` says. A server that predates #1572 answers OK=true with a
		// non-empty repairable list for exactly the state an attacker produces
		// by writing `priority` and `priority_at_emit` together — so the CLI
		// makes its own judgement rather than relaying that verdict, and an
		// up-to-date CLI reports the truth against an older server.
		clean := res.OK && res.RepairableCount == 0 && len(res.Repairable) == 0

		f := newFormatter()
		if err := f.AutoHuman(res, func() {
			if clean {
				fmt.Printf("Journal chain OK — %d entries verified against the keyed HMAC chain, no tampering detected.\n", res.Count)
				if res.Checkpoints > 0 {
					fmt.Printf("%d signed compaction checkpoint(s) bridged legitimately-deleted ranges.\n", res.Checkpoints)
				}
				return
			}
			if res.OK {
				// Reachable only against a pre-#1572 server: the chain walk
				// called it clean while reporting rows it could not account for.
				fmt.Printf("Journal chain UNRESOLVED — the server reported no break, but %d entr(y|ies) have a priority the record cannot account for.\n",
					repairableTotal(res.RepairableCount, len(res.Repairable)))
				printRepairable(res.Repairable, res.RepairableCount, res.RepairableTruncated)
				return
			}
			// NOT "verified N entries before the break" any more: the walk no
			// longer stops at the first one, so Count is every entry examined.
			//
			// And the unit is CHECKS, not entries: one row runs both a content
			// and a priority check, so a single bad entry can contribute two
			// breaks. Zero means either a STRUCTURAL break — a sequence gap or
			// disorder, which halts before any per-row check is recorded — or an
			// older server that does not send the field. Neither justifies
			// inventing a count of 1.
			if res.BreakCount > 0 {
				fmt.Printf("Journal chain BROKEN — %d integrity check(s) failed across %d entries examined.\n",
					res.BreakCount, res.Count)
			} else {
				fmt.Printf("Journal chain BROKEN after examining %d entries.\n", res.Count)
			}
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
			printRepairable(res.Repairable, res.RepairableCount, res.RepairableTruncated)
		}); err != nil {
			return err
		}

		// Non-zero exit on a broken OR unresolved chain so cron / the
		// test-harness can assert integrity without parsing output (holds in
		// every format). Exiting 0 while the server is reporting rows it cannot
		// account for is the last line of defence failing quietly — #1572.
		if !clean {
			return fmt.Errorf("audit journal integrity check failed")
		}
		return nil
	},
}

func init() {
	journalCmd.AddCommand(journalVerifyCmd)
}
