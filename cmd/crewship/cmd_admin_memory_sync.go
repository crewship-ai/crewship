//go:build !clionly

package main

// `crewship admin memory sync user-model|peer-cards` — run one of the two
// daily memory sweeps now (#1702).
//
//	POST /api/v1/admin/memory/user-model-sync
//	POST /api/v1/admin/memory/peer-card-sync
//
// The operator-model sweep fires at 05:00 UTC, the peer-card sweep at
// 04:00, and neither could be triggered any other way — so an operator who
// had just configured the curator slot, or flipped memory.user_model_profile,
// could only find out whether it works by waiting a day and reading logs.
// This is the button. Same sweep, same extractor, same workspace set; the
// response is the per-workspace summary the worker only ever logged.
//
// Scope: the CLI's current workspace (-w / config) by default, because that
// is what every other `admin` command means by "this workspace". --all runs
// what the scheduled sweep runs — every active workspace on the instance.
// --dry-run reports what the sweep would do and writes nothing, which is
// how #1700's kind of measurement becomes repeatable.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// memorySyncWorkspaceRow mirrors internal/api.memorySyncWorkspaceSummary.
type memorySyncWorkspaceRow struct {
	WorkspaceID      string `json:"workspace_id,omitempty" yaml:"workspace_id,omitempty"`
	Candidates       int    `json:"candidates" yaml:"candidates"`
	Writes           int    `json:"writes" yaml:"writes"`
	SkippedThreshold int    `json:"skipped_threshold" yaml:"skipped_threshold"`
	SkippedEmpty     int    `json:"skipped_empty" yaml:"skipped_empty"`
	SkippedOptOut    int    `json:"skipped_opt_out" yaml:"skipped_opt_out"`
	PurgedOptOut     int    `json:"purged_opt_out" yaml:"purged_opt_out"`
	Errors           int    `json:"errors" yaml:"errors"`
	Error            string `json:"error,omitempty" yaml:"error,omitempty"`
}

// memorySyncResult mirrors internal/api.memorySyncResponse.
type memorySyncResult struct {
	Sweep      string                   `json:"sweep" yaml:"sweep"`
	DryRun     bool                     `json:"dry_run" yaml:"dry_run"`
	Workspaces []memorySyncWorkspaceRow `json:"workspaces" yaml:"workspaces"`
	Totals     memorySyncWorkspaceRow   `json:"totals" yaml:"totals"`
	DurationMs int64                    `json:"duration_ms" yaml:"duration_ms"`
}

// memorySyncTimeout is the per-request cap. The server runs the sweep
// inside the request, one model call per candidate on the curator slot's
// own per-call budget (30s when the slot states none), so a workspace with
// a few dozen active operators can legitimately take minutes. The client's
// default 30s would cancel the request — and with it the server's context,
// so the sweep too — a minute before a real run finishes. --timeout raises
// it further.
const memorySyncTimeout = 10 * time.Minute

var adminMemoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Operate the memory subsystem's sweeps (admin)",
	Long: `Operator controls for the memory subsystem's background work.

Today that is the two daily sweeps — 'sync user-model' and 'sync peer-cards'
— which otherwise fire only at 05:00 and 04:00 UTC. For the retention window
see 'admin memory-config'; for what is being retained, 'admin memory-stats'.`,
}

var adminMemorySyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Run one of the daily memory sweeps now (ADMIN)",
	Long: `Run one of the two daily memory sweeps now instead of waiting for its
scheduled tick, and get the per-workspace summary the sweep otherwise only
logs: candidates, writes, and why each candidate that was not written was
skipped.

Both sweeps are safe to re-run: the merge is idempotent and the write is
capped, so a manual run followed by the scheduled one does no harm.

Scope defaults to the current workspace (-w / config). --all runs every
active workspace on the instance, exactly as the scheduler does.

--dry-run runs everything up to the write — including the extractor, which
is the model-backed step worth observing — and reports what WOULD have
happened. Nothing lands on disk, in the index tables or in the audit log.`,
}

var adminMemorySyncUserModelCmd = &cobra.Command{
	Use:   "user-model",
	Short: "Run the operator-model sweep now (the 05:00 UTC one)",
	Long: `Run the operator-model sweep now.

The sweep walks every operator who crossed the interaction threshold in the
workspace, asks the curator slot's model to refresh their model from their
own recent turns, and writes the merged result to the crew's shared memory.
Every failure mode is silent by design — nothing is written when the model
proposes nothing, when every candidate is refused, when the curator slot
cannot be built, or when the person is below threshold — and this command
is how those are told apart: skipped_empty vs skipped_threshold vs errors,
now, rather than a Warn line at 05:00 UTC.

Examples:
  crewship admin memory sync user-model
  crewship admin memory sync user-model --dry-run
  crewship admin memory sync user-model --all --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAdminMemorySync(cmd, "/api/v1/admin/memory/user-model-sync", "operator-model")
	},
}

var adminMemorySyncPeerCardsCmd = &cobra.Command{
	Use:   "peer-cards",
	Short: "Run the peer-card sweep now (the 04:00 UTC one)",
	Long: `Run the peer-card sweep now.

The sweep walks every (agent, operator) pair that crossed the interaction
threshold, purges the cards of operators who opted out, and writes a card
where the extractor produced one. No extractor is wired on the server today,
so a run indexes and purges without writing new bodies — the same thing the
04:00 UTC sweep does — and the summary shows the purge and threshold counts.

Examples:
  crewship admin memory sync peer-cards
  crewship admin memory sync peer-cards --all --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAdminMemorySync(cmd, "/api/v1/admin/memory/peer-card-sync", "peer-card")
	},
}

func runAdminMemorySync(cmd *cobra.Command, path, label string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	timeout, _ := cmd.Flags().GetDuration("timeout")

	client := newAPIClient()
	body := map[string]any{"dry_run": dryRun}
	if !all {
		// The sweep is instance-wide unless told otherwise; the CLI's
		// notion of "this workspace" is the one the client already
		// resolved (slug → id) for the session.
		body["workspace_id"] = client.GetWorkspaceID()
	}
	resp, err := client.WithTimeout(timeout).Post(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var out memorySyncResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return resolvedFormatter(cmd).AutoHuman(out, func() {
		printMemorySyncTable(out, label)
	})
}

// printMemorySyncTable renders one row per workspace plus totals. Every
// skip reason gets its own column because telling them apart is the whole
// point of the command: "0 writes" alone is exactly the silence #1702 is
// about.
func printMemorySyncTable(out memorySyncResult, label string) {
	if out.DryRun {
		cli.PrintWarning(fmt.Sprintf("Dry run — the %s sweep reported what it would do and wrote nothing.", label))
	} else {
		cli.PrintSuccess(fmt.Sprintf("The %s sweep ran over %d workspace(s) in %s.",
			label, len(out.Workspaces), (time.Duration(out.DurationMs) * time.Millisecond).Round(time.Millisecond)))
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WORKSPACE\tCANDIDATES\tWRITES\tBELOW THRESHOLD\tEMPTY\tOPTED OUT\tPURGED\tERRORS\tNOTE")
	for _, ws := range out.Workspaces {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			ws.WorkspaceID, ws.Candidates, ws.Writes, ws.SkippedThreshold, ws.SkippedEmpty,
			ws.SkippedOptOut, ws.PurgedOptOut, ws.Errors, strings.TrimSpace(ws.Error))
	}
	if len(out.Workspaces) != 1 {
		t := out.Totals
		fmt.Fprintf(w, "TOTAL\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t\n",
			t.Candidates, t.Writes, t.SkippedThreshold, t.SkippedEmpty, t.SkippedOptOut, t.PurgedOptOut, t.Errors)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	if len(out.Workspaces) == 0 {
		fmt.Println("No active workspace to sweep.")
		return
	}
	if out.Totals.Candidates == 0 {
		fmt.Println("\nNo candidates: nobody has talked to an agent in the lookback window (14 days).")
	} else if out.Totals.Writes == 0 && out.Totals.Errors == 0 && out.Totals.SkippedEmpty > 0 {
		switch out.Sweep {
		case "user_model":
			fmt.Println("\nNothing written. EMPTY counts candidates the extractor had nothing to say about —")
			fmt.Println("with no model configured on the curator slot that is every candidate; check")
			fmt.Println("'crewship keeper aux' and the server log's 'user model extraction' lines.")
		case "peer_card":
			fmt.Println("\nNothing written. No peer-card extractor is wired on the server, so every")
			fmt.Println("threshold-crosser counts as EMPTY; the sweep still indexes and purges opt-outs.")
		}
	}
	if out.Totals.Errors > 0 {
		fmt.Println("\nSome candidates failed; the server log has one 'user model extractor failed' or")
		fmt.Println("'peer card sync candidate failed' line per failure.")
	}
}

func init() {
	for _, c := range []*cobra.Command{adminMemorySyncUserModelCmd, adminMemorySyncPeerCardsCmd} {
		c.Flags().Bool("all", false, "Sweep every active workspace on the instance, as the scheduled run does (default: the current workspace)")
		c.Flags().Bool("dry-run", false, "Run the sweep up to the write and report what it would do; nothing is written")
		c.Flags().Duration("timeout", memorySyncTimeout, "How long to wait for the sweep (one model call per candidate; a big workspace can take minutes)")
	}
	adminMemorySyncCmd.AddCommand(adminMemorySyncUserModelCmd)
	adminMemorySyncCmd.AddCommand(adminMemorySyncPeerCardsCmd)
	adminMemoryCmd.AddCommand(adminMemorySyncCmd)
	adminCmd.AddCommand(adminMemoryCmd)
}
