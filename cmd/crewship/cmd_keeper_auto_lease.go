package main

import (
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/spf13/cobra"
)

// keeperAutoLeaseCmd is the credential-lease auto-issuance toggle (#1373). It
// rides the same governance row/endpoint as enable/contact/threshold/
// second-approver, so each subcommand is a one-field partial update.
//
// The distinction from `crewship credential assign --ttl` matters and is spelled
// out in the help text: --ttl leases ONE grant by hand, this leases every L3/L4
// grant automatically at the moment Keeper approves its use.
var keeperAutoLeaseCmd = &cobra.Command{
	Use:   "auto-lease",
	Short: "Auto-issue short-lived credential leases when Keeper approves access",
	Long: `Configure automatic credential-lease issuance for this workspace
(requires OWNER or ADMIN).

Credential grants are normally STANDING: once an agent has a credential it keeps
it indefinitely, so a stolen grant stays valuable forever. With auto-lease set,
every approval re-issues the grant as a short-lived LEASE instead:

  * a Keeper ALLOW on /keeper/request or /keeper/execute, and
  * a human approving an agent-proposed CREDENTIAL escalation

set the grant to expire after the configured TTL. Once it lapses the credential
is refused at every injection point (boot env vars, /secrets files, the sidecar
credstore, and per-command /keeper/execute injection) and the agent must ask
again — which mints a fresh lease.

Scope and safety rails:

  * OPT-IN. Default off; nothing changes until you set a TTL.
  * L3/L4 ONLY. L1/L2 self-service credentials are delivered to the agent for
    the whole run (they are how it calls its own model), so they are never
    auto-leased.
  * NEVER SHORTENS A LONGER LEASE. A grant you leased by hand with
    'credential assign --ttl 7d' keeps its 7 days.
  * Minimum 60s — a shorter lease can lapse inside Keeper's own evaluation.
    Maximum 30 days, the same cap as --ttl.

Examples:
  crewship keeper auto-lease status
  crewship keeper auto-lease set 15m
  crewship keeper auto-lease off`,
}

// printKeeperAutoLease renders the one-line lease state, shared by the
// subcommands so their output cannot drift.
func printKeeperAutoLease(gov keeperGovernance) {
	fmt.Printf("%sCredential auto-lease (workspace)%s\n", cli.Bold, cli.Reset)
	fmt.Printf("  Auto-lease:   %s\n", formatAutoLease(gov.AutoLeaseSeconds))
}

// formatAutoLease renders the TTL the way an operator entered it (a duration),
// not as a raw second count.
func formatAutoLease(seconds int) string {
	if seconds <= 0 {
		return cli.Red + "off" + cli.Reset + " (grants stay standing)"
	}
	return cli.Green + (time.Duration(seconds) * time.Second).String() + cli.Reset +
		" (L3/L4 grants expire this long after each approval)"
}

var keeperAutoLeaseStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current credential auto-lease TTL",
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
		return newFormatter().AutoHuman(gov, func() { printKeeperAutoLease(gov) })
	},
}

var keeperAutoLeaseSetCmd = &cobra.Command{
	Use:   "set <duration>",
	Short: "Auto-issue leases of this length on Keeper approval (requires OWNER or ADMIN)",
	Long: `Set the auto-lease TTL as a Go duration, e.g. 15m, 1h, 24h. Minimum 60s,
maximum 30 days (Go durations have no "d" unit — 30 days is 720h).

Examples:
  crewship keeper auto-lease set 15m
  crewship keeper auto-lease set 4h`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		d, perr := time.ParseDuration(args[0])
		if perr != nil {
			return fmt.Errorf("invalid duration %q: %w (use e.g. 15m, 1h, 24h)", args[0], perr)
		}
		secs := int(d.Seconds())
		// Validate client-side with the same bounds the server enforces, so the
		// operator gets an actionable message instead of a generic 400. The
		// server still validates — this is a UX layer, not the gate.
		if secs < governance.MinAutoLeaseSeconds {
			return fmt.Errorf("auto-lease must be at least %s — a shorter lease can lapse inside Keeper's own evaluation (got %s)",
				(time.Duration(governance.MinAutoLeaseSeconds) * time.Second).String(), d)
		}
		if secs > governance.MaxAutoLeaseSeconds {
			return fmt.Errorf("auto-lease must be at most 30 days — a longer one is a standing grant in disguise (got %s)", d)
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{"auto_lease_seconds": secs})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf(
				"Keeper approvals now issue %s credential leases for L3/L4 grants in this workspace.", d))
			printKeeperAutoLease(out)
		})
	},
}

var keeperAutoLeaseOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Stop auto-issuing credential leases (requires OWNER or ADMIN)",
	Long: `Turn auto-issuance off. Grants stay standing again — note this does NOT
un-lease grants that are already leased; clear those with
'crewship credential unassign' + 'credential assign' (no --ttl), which is
deliberate: silently extending live leases on a config change would be the
wrong direction for a security control.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{"auto_lease_seconds": 0})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Credential auto-lease disabled — new approvals leave grants standing.")
			fmt.Printf("%sNote:%s grants already leased keep their expiry.\n", cli.Yellow, cli.Reset)
			printKeeperAutoLease(out)
		})
	},
}

func init() {
	keeperAutoLeaseCmd.AddCommand(keeperAutoLeaseStatusCmd)
	keeperAutoLeaseCmd.AddCommand(keeperAutoLeaseSetCmd)
	keeperAutoLeaseCmd.AddCommand(keeperAutoLeaseOffCmd)
	keeperCmd.AddCommand(keeperAutoLeaseCmd)
}
