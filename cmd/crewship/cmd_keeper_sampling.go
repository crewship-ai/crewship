package main

import (
	"fmt"
	"strconv"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/spf13/cobra"
)

// keeperSamplingCmd is the behaviour monitor's sampling cadence (#1001 M3). It
// rides the same governance row/endpoint as enable/contact/threshold/
// second-approver/auto-lease, so each subcommand is a one-field partial update.
//
// The cadence has always existed inside the hook and was reachable from
// nothing: every workspace ran on the hardwired every-5th-call default. This is
// the surface that reaches it.
var keeperSamplingCmd = &cobra.Command{
	Use:   "sampling",
	Short: "How often the watchdog reviews a tool call",
	Long: `Set how often the behavioural watchdog reviews a tool call in this
workspace (requires OWNER or ADMIN).

The watchdog does not review everything agents do — each review is a call to the
governance model, so it samples: one in every N tool calls per crew, counted per
crew. The default is one in ` + strconv.Itoa(governance.DefaultBehaviorSampleEvery) + `.

Which way to move it:

  * TIGHTER (a smaller number) catches more, later than never but sooner than
    the next sample, and costs a governance-model call proportionally more
    often. At 1 every single tool call is reviewed.
  * LOOSER (a bigger number) is cheaper and quieter. Past a point it stops
    being monitoring: the counter is per-crew and in memory, so a cadence
    larger than the number of tool calls a run makes means nothing is ever
    reviewed. That is why the ceiling is ` + strconv.Itoa(governance.MaxBehaviorSampleEvery) + `.

This is not an on/off switch. There is no cadence that means "stop" — use
'crewship keeper disable' for that, so the setting can never read "enabled"
while nothing is being looked at.

Takes effect on the next tool call; no restart.

Examples:
  crewship keeper sampling status
  crewship keeper sampling set 1
  crewship keeper sampling set 20
  crewship keeper sampling default`,
}

// formatSampleEvery renders the cadence the way an operator reads it — a rate,
// with the unset sentinel named as the default rather than printed as 0.
func formatSampleEvery(every int) string {
	if every <= 0 {
		return fmt.Sprintf("1 in %d tool calls %s(default)%s",
			governance.DefaultBehaviorSampleEvery, cli.Dim, cli.Reset)
	}
	if every == 1 {
		return cli.Yellow + "every tool call" + cli.Reset + " (a governance-model call each time)"
	}
	return fmt.Sprintf("1 in %d tool calls", every)
}

func printKeeperSampling(gov keeperGovernance) {
	fmt.Printf("%sBehaviour sampling (workspace)%s\n", cli.Bold, cli.Reset)
	fmt.Printf("  Reviews:      %s\n", formatSampleEvery(gov.BehaviorSampleEvery))
	if !gov.Enabled {
		fmt.Printf("  %sNote:%s the watchdog is off, so nothing is sampled at any rate.\n", cli.Yellow, cli.Reset)
	}
}

var keeperSamplingStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show how often tool calls are reviewed",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		gov, err := getKeeperGovernance(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(gov, func() { printKeeperSampling(gov) })
	},
}

var keeperSamplingSetCmd = &cobra.Command{
	Use:   "set <N>",
	Short: "Review one in every N tool calls (requires OWNER or ADMIN)",
	Long: fmt.Sprintf(`Review one in every N tool calls per crew. N is between %d and %d.

N=%d reviews every tool call — allowed, and worth understanding before you pick
it: it puts a governance-model round-trip behind essentially everything your
agents do, which on a hosted judge is a bill that scales with tool-call volume.

There is no N that turns the watchdog off; 'crewship keeper disable' does that.

Examples:
  crewship keeper sampling set 1
  crewship keeper sampling set 20`,
		governance.MinBehaviorSampleEvery, governance.MaxBehaviorSampleEvery,
		governance.MinBehaviorSampleEvery),
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("sampling rate must be a whole number, got %q", args[0])
		}
		// Validate client-side against the server's own constants so the operator
		// gets an actionable message instead of a generic 400. The server still
		// validates — this is a UX layer, not the gate.
		if n == 0 {
			return fmt.Errorf("a sampling rate of 0 would leave the watchdog enabled but never sampling — " +
				"use `crewship keeper disable` to turn it off")
		}
		if n < governance.MinBehaviorSampleEvery || n > governance.MaxBehaviorSampleEvery {
			return fmt.Errorf("sampling rate must be between %d and %d, got %d",
				governance.MinBehaviorSampleEvery, governance.MaxBehaviorSampleEvery, n)
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{"behavior_sample_every": n})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("The watchdog now reviews 1 in %d tool calls in this workspace.", n))
			if out.Warning != "" {
				fmt.Printf("%s⚠ %s%s\n", cli.Yellow, out.Warning, cli.Reset)
			}
			printKeeperSampling(out)
		})
	},
}

var keeperSamplingDefaultCmd = &cobra.Command{
	Use:   "default",
	Short: "Return to the built-in sampling rate (requires OWNER or ADMIN)",
	Long: fmt.Sprintf(`Clear the workspace's sampling rate and follow the built-in default
(1 in %d tool calls). This does NOT turn the watchdog off — 'crewship keeper
disable' does that.

Sent as the explicit default rather than as a null, so the stored row says what
this workspace decided rather than reverting to "never configured".`,
		governance.DefaultBehaviorSampleEvery),
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client,
			map[string]any{"behavior_sample_every": governance.DefaultBehaviorSampleEvery})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("Sampling rate reset to the default (1 in %d tool calls).",
				governance.DefaultBehaviorSampleEvery))
			printKeeperSampling(out)
		})
	},
}

func init() {
	keeperSamplingCmd.AddCommand(keeperSamplingStatusCmd)
	keeperSamplingCmd.AddCommand(keeperSamplingSetCmd)
	keeperSamplingCmd.AddCommand(keeperSamplingDefaultCmd)
	keeperCmd.AddCommand(keeperSamplingCmd)
}
