package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// keeperCmd groups the Keeper watchdog governance commands (issue #1001, M0)
// around GET/PUT /api/v1/admin/keeper/governance plus the existing server
// status endpoint. Governance is workspace-scoped and ADMIN+: the in-app
// enable toggle, the named security contact escalations are routed to, and
// the risk threshold at which a DENY decision also lands in the inbox.
//
// Distinct from `crewship system keeper` (server-level status only): this
// command combines both layers and is the mutation surface for governance.
var keeperCmd = &cobra.Command{
	Use:   "keeper",
	Short: "Keeper watchdog status and governance settings",
	Long: `Inspect and configure the Keeper security watchdog for the current
workspace.

The server layer (Ollama-backed Keeper engine) is configured by the operator;
the governance layer is the per-workspace overlay: whether the behavioral
watchdog is enabled, which OWNER/ADMIN member receives escalations, and the
risk score (1-10) at or above which a DENY decision is also notified.

The behavioral watchdog is opt-in and default OFF per workspace — it only runs
once an OWNER/ADMIN enables it. Each subcommand updates exactly one setting.

Examples:
  crewship keeper status
  crewship keeper enable
  crewship keeper contact admin@example.com
  crewship keeper contact --clear
  crewship keeper threshold 8
  crewship keeper second-approver enable`,
}

// keeperGovernance mirrors the GET/PUT /api/v1/admin/keeper/governance
// response shape (internal/api/keeper_governance.go).
type keeperGovernance struct {
	Configured            bool     `json:"configured"`
	Enabled               bool     `json:"enabled"`
	SecurityContactUserID string   `json:"security_contact_user_id"`
	DenyNotifyMinRisk     int      `json:"deny_notify_min_risk"`
	WatchSpec             string   `json:"watch_spec"`
	WatchPresets          []string `json:"watch_presets"`
	// RequireSecondApprover is the credential-escalation "four-eyes" toggle
	// (issue #1084): when true, the user recorded as the initiating agent's
	// owner cannot resolve a CREDENTIAL escalation that agent raised — OWNER
	// is not exempt. Rides on this same governance row/endpoint but is a
	// distinct concern from the behavioral watchdog above it.
	RequireSecondApprover bool `json:"require_second_approver"`
	// Warning is a non-blocking advisory the server returns on a mutation —
	// e.g. enabling second-approver with fewer than 2 eligible approvers.
	Warning string `json:"warning,omitempty"`
}

// keeperServerStatus mirrors GET /api/v1/system/keeper.
type keeperServerStatus struct {
	Enabled      bool   `json:"enabled"`
	OllamaURL    string `json:"ollama_url"`
	Model        string `json:"model"`
	OllamaOnline bool   `json:"ollama_online"`
	SecretCount  int    `json:"secret_count"`
}

// keeperStatusPayload is the machine shape for `keeper status --format json`.
type keeperStatusPayload struct {
	Server     keeperServerStatus `json:"server"`
	Governance keeperGovernance   `json:"governance"`
}

// getKeeperGovernance fetches the current workspace governance settings.
func getKeeperGovernance(client *cli.Client) (keeperGovernance, error) {
	var gov keeperGovernance
	if err := getJSON(client, "/api/v1/admin/keeper/governance", &gov); err != nil {
		return keeperGovernance{}, keeperPermissionHint(err)
	}
	return gov, nil
}

// putKeeperGovernanceFields sends a partial update: only the fields present in
// body are changed server-side, the rest of the row is left untouched. Each
// subcommand sends exactly its own field, so there is no read-merge-write
// (which could echo a stale value back and clobber a setting) and concurrent
// single-field edits commute instead of overwriting each other.
func putKeeperGovernanceFields(client *cli.Client, body map[string]any) (keeperGovernance, error) {
	var out keeperGovernance
	if err := putJSON(client, "/api/v1/admin/keeper/governance", body, &out); err != nil {
		return keeperGovernance{}, keeperPermissionHint(err)
	}
	return out, nil
}

// printKeeperGovernance renders the governance block of the human output.
// Shared by status and every mutation so the shape can't drift.
func printKeeperGovernance(gov keeperGovernance) {
	configured := "no (opt-in — off by default)"
	if gov.Configured {
		configured = "yes"
	}
	enabled := cli.Red + "disabled" + cli.Reset
	if gov.Enabled {
		enabled = cli.Green + "enabled" + cli.Reset
	}
	contact := gov.SecurityContactUserID
	if contact == "" {
		contact = "— (MANAGER fanout)"
	}

	secondApprover := cli.Red + "off" + cli.Reset
	if gov.RequireSecondApprover {
		secondApprover = cli.Green + "on" + cli.Reset
	}

	fmt.Printf("%sWatchdog Governance (workspace)%s\n", cli.Bold, cli.Reset)
	fmt.Printf("  Configured:   %s\n", configured)
	fmt.Printf("  Watchdog:     %s\n", enabled)
	fmt.Printf("  Contact:      %s\n", contact)
	fmt.Printf("  DENY-notify:  risk >= %d\n", gov.DenyNotifyMinRisk)
	fmt.Printf("  2nd approver: %s\n", secondApprover)
}

var keeperStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Keeper server status and workspace watchdog governance",
	Long: `Show the combined Keeper picture: the server-level engine status
(GET /api/v1/system/keeper) and the workspace watchdog governance settings
(GET /api/v1/admin/keeper/governance). Both routes require ADMIN or OWNER
role in the current workspace.

Examples:
  crewship keeper status
  crewship keeper status --format json | jq .governance.enabled`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		var server keeperServerStatus
		if err := getJSON(client, "/api/v1/system/keeper", &server); err != nil {
			return keeperPermissionHint(err)
		}

		gov, err := getKeeperGovernance(client)
		if err != nil {
			return err
		}

		payload := keeperStatusPayload{Server: server, Governance: gov}
		return newFormatter().AutoHuman(payload, func() {
			status := cli.Red + "disabled" + cli.Reset
			if server.Enabled {
				status = cli.Green + "enabled" + cli.Reset
			}
			ollamaStatus := cli.Red + "offline" + cli.Reset
			if server.OllamaOnline {
				ollamaStatus = cli.Green + "online" + cli.Reset
			}

			fmt.Printf("%sKeeper Security (server)%s\n", cli.Bold, cli.Reset)
			fmt.Printf("  Status:       %s\n", status)
			fmt.Printf("  Ollama URL:   %s\n", server.OllamaURL)
			fmt.Printf("  Model:        %s\n", server.Model)
			fmt.Printf("  Ollama:       %s\n", ollamaStatus)
			fmt.Printf("  Secret creds: %d\n", server.SecretCount)
			fmt.Println()
			printKeeperGovernance(gov)
		})
	},
}

// setKeeperWatchdogEnabled flips only the enabled flag; contact and threshold
// are untouched by the partial update.
func setKeeperWatchdogEnabled(enabled bool) error {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}

	out, err := putKeeperGovernanceFields(client, map[string]any{"enabled": enabled})
	if err != nil {
		return err
	}

	return newFormatter().AutoHuman(out, func() {
		verb := "disabled"
		if out.Enabled {
			verb = "enabled"
		}
		cli.PrintSuccess(fmt.Sprintf("Keeper watchdog %s for this workspace.", verb))
		printKeeperGovernance(out)
	})
}

var keeperEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable the Keeper watchdog for this workspace (requires OWNER or ADMIN)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setKeeperWatchdogEnabled(true)
	},
}

var keeperDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable the Keeper watchdog for this workspace (requires OWNER or ADMIN)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setKeeperWatchdogEnabled(false)
	},
}

// resolveContactUserID maps an email to a workspace member's user id via
// GET /workspaces/{id}/members, enforcing the OWNER/ADMIN requirement
// client-side so the operator gets a role-specific message instead of the
// server's generic 400 (the server still validates on PUT).
func resolveContactUserID(client *cli.Client, email string) (string, error) {
	var members []struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
		User   struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	wsID := client.GetWorkspaceID()
	if err := getJSON(client, "/api/v1/workspaces/"+wsID+"/members", &members); err != nil {
		return "", err
	}

	for _, m := range members {
		if !strings.EqualFold(m.User.Email, email) {
			continue
		}
		if m.Role != "OWNER" && m.Role != "ADMIN" {
			return "", fmt.Errorf("security contact must have OWNER or ADMIN role — %s is %s", email, m.Role)
		}
		return m.UserID, nil
	}
	return "", cli.NotFoundf("no workspace member with email %q", email)
}

var keeperContactCmd = &cobra.Command{
	Use:   "contact <email>",
	Short: "Set the security contact escalations are routed to (requires OWNER or ADMIN)",
	Long: `Route Keeper watchdog escalations to a named workspace member,
resolved by email. The contact must hold OWNER or ADMIN role — escalations
target someone who can act on them. Pass --clear to unset the contact and
fall back to the MANAGER-role fanout.

Examples:
  crewship keeper contact admin@example.com
  crewship keeper contact --clear`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		clear, _ := cmd.Flags().GetBool("clear")
		if clear && len(args) > 0 {
			return fmt.Errorf("pass either an email or --clear, not both")
		}
		if !clear && len(args) == 0 {
			return fmt.Errorf("an email argument is required (or --clear to unset the contact)")
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		contactID := ""
		if !clear {
			contactID, err = resolveContactUserID(client, args[0])
			if err != nil {
				return err
			}
		}

		out, err := putKeeperGovernanceFields(client, map[string]any{"security_contact_user_id": contactID})
		if err != nil {
			return err
		}

		return newFormatter().AutoHuman(out, func() {
			if clear {
				cli.PrintSuccess("Security contact cleared — escalations fan out to MANAGER+ members.")
			} else {
				cli.PrintSuccess(fmt.Sprintf("Security contact set to %s (%s).", args[0], out.SecurityContactUserID))
			}
			printKeeperGovernance(out)
		})
	},
}

var keeperThresholdCmd = &cobra.Command{
	Use:   "threshold <1-10>",
	Short: "Set the DENY-notify risk threshold (requires OWNER or ADMIN)",
	Long: `Set the risk score (1-10) at or above which a Keeper DENY decision
also lands in the inbox. ESCALATE decisions always notify regardless of
this threshold. The server default is 7.

Examples:
  crewship keeper threshold 8`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		risk, err := strconv.Atoi(args[0])
		if err != nil || risk < 1 || risk > 10 {
			return fmt.Errorf("threshold must be an integer between 1 and 10, got %q", args[0])
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		out, err := putKeeperGovernanceFields(client, map[string]any{"deny_notify_min_risk": risk})
		if err != nil {
			return err
		}

		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("DENY-notify threshold set to risk >= %d.", out.DenyNotifyMinRisk))
			printKeeperGovernance(out)
		})
	},
}

// keeperSecondApproverCmd groups the credential-escalation "four-eyes" toggle
// (issue #1084). Distinct from the behavioral watchdog above it: this gate
// governs who may RESOLVE a CREDENTIAL escalation, not tool-call monitoring.
// It rides on the same governance row/endpoint (GET/PUT
// /api/v1/admin/keeper/governance), so each subcommand is a one-field partial
// update just like enable/disable/contact/threshold.
var keeperSecondApproverCmd = &cobra.Command{
	Use:   "second-approver",
	Short: "Require a different human to approve credential escalations their own agent raised",
	Long: `Toggle the credential-escalation segregation-of-duties rule for this
workspace (requires OWNER or ADMIN).

When enabled, resolving a CREDENTIAL escalation (crewship escalation resolve,
or the inbox Approve/Reject button) is refused for the user recorded as the
owner of the agent that raised it — approver must differ from initiator.
This is a strict four-eyes rule: workspace OWNER is NOT exempt, even for an
escalation raised by an agent they created.

Default is off — existing single-approver workflows are unaffected until an
OWNER/ADMIN opts in.

Examples:
  crewship keeper second-approver enable
  crewship keeper second-approver disable`,
}

func setKeeperRequireSecondApprover(enabled bool) error {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}

	out, err := putKeeperGovernanceFields(client, map[string]any{"require_second_approver": enabled})
	if err != nil {
		return err
	}

	return newFormatter().AutoHuman(out, func() {
		verb := "disabled"
		if out.RequireSecondApprover {
			verb = "enabled"
		}
		cli.PrintSuccess(fmt.Sprintf("Second-approver rule %s for credential escalations in this workspace.", verb))
		if out.Warning != "" {
			fmt.Printf("%s⚠ %s%s\n", cli.Yellow, out.Warning, cli.Reset)
		}
		printKeeperGovernance(out)
	})
}

var keeperSecondApproverEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Require a second approver for credential escalations (requires OWNER or ADMIN)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setKeeperRequireSecondApprover(true)
	},
}

var keeperSecondApproverDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Allow a single approver for credential escalations again (requires OWNER or ADMIN)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setKeeperRequireSecondApprover(false)
	},
}

func init() {
	keeperContactCmd.Flags().Bool("clear", false, "Unset the security contact (fall back to MANAGER fanout)")

	keeperSecondApproverCmd.AddCommand(keeperSecondApproverEnableCmd)
	keeperSecondApproverCmd.AddCommand(keeperSecondApproverDisableCmd)

	keeperCmd.AddCommand(keeperStatusCmd)
	keeperCmd.AddCommand(keeperEnableCmd)
	keeperCmd.AddCommand(keeperDisableCmd)
	keeperCmd.AddCommand(keeperContactCmd)
	keeperCmd.AddCommand(keeperThresholdCmd)
	keeperCmd.AddCommand(keeperWatchCmd)
	keeperCmd.AddCommand(keeperSecondApproverCmd)

	rootCmd.AddCommand(keeperCmd)
}
