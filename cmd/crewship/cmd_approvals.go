package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// approvalsCmd groups the human-in-the-loop approval workflow commands.
// Mirrors the web UI approvals queue; list is safe for any member while
// approve/deny require OWNER or ADMIN role server-side (403 otherwise).
//
// NOTE: `cancel` is deliberately absent — the backend does not expose a
// cancel endpoint yet. Track its arrival and add the subcommand when
// `POST /api/v1/approvals/{id}/cancel` lands.
var approvalsCmd = &cobra.Command{
	Use:   "approvals",
	Short: "List and decide pending human-in-the-loop approval requests",
	Long: `Manage the approval queue — the set of agent-initiated actions that
require a human decision (OWNER/ADMIN) before the agent can proceed.

Examples:
  crewship approvals list
  crewship approvals list --status approved --limit 100
  crewship approvals approve <id> --comment "looks safe"
  crewship approvals deny <id> --comment "wrong mission"

Subcommand status:
  list      — live (GET /api/v1/approvals)
  approve   — live (POST /api/v1/approvals/{id}/decide)
  deny      — live (POST /api/v1/approvals/{id}/decide)
  cancel    — backend endpoint pending; not yet available on the CLI.`,
}

var approvalsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List approval requests",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		status, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")

		q := url.Values{}
		if status != "" {
			q.Set("status", status)
		}
		if limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", limit))
		}
		path := "/api/v1/approvals?" + q.Encode()

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Rows []struct {
				ID          string `json:"id"`
				CrewID      string `json:"crew_id"`
				AgentID     string `json:"agent_id"`
				MissionID   string `json:"mission_id"`
				Kind        string `json:"kind"`
				Reason      string `json:"reason"`
				Status      string `json:"status"`
				RequestedBy string `json:"requested_by"`
				DecidedBy   string `json:"decided_by"`
				CreatedAt   string `json:"created_at"`
			} `json:"rows"`
			Status string `json:"status"`
			Count  int    `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body.Rows)
		}
		if f.Format == "yaml" {
			return f.YAML(body.Rows)
		}

		// Table output — color the STATUS column to make the queue
		// scannable at a glance (same idiom as journal's severity chip).
		for _, r := range body.Rows {
			color := cli.Gray
			switch r.Status {
			case "pending":
				color = cli.Yellow
			case "approved":
				color = cli.Green
			case "denied":
				color = cli.Red
			case "timeout", "cancelled":
				color = cli.Gray
			}
			fmt.Printf("%s%-24s%s  %s[%-9s]%s  %s%-16s%s  %-16s  %-16s  %s\n",
				cli.Dim, truncateString(r.ID, 24), cli.Reset,
				color, r.Status, cli.Reset,
				cli.Bold, truncateString(r.Kind, 16), cli.Reset,
				truncateString(r.CrewID, 16),
				truncateString(r.AgentID, 16),
				truncateString(r.Reason, 48),
			)
		}
		return nil
	},
}

var approvalsApproveCmd = &cobra.Command{
	Use:   "approve <id>",
	Short: "Approve a pending request (requires OWNER or ADMIN)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return decideApproval(cmd, args[0], "approved")
	},
}

var approvalsDenyCmd = &cobra.Command{
	Use:   "deny <id>",
	Short: "Deny a pending request (requires OWNER or ADMIN)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return decideApproval(cmd, args[0], "denied")
	},
}

// decideApproval POSTs to /decide with either "approved" or "denied".
// Shared by both approve and deny so the request/response decode stays
// in one place and can't drift.
func decideApproval(cmd *cobra.Command, id, status string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	comment, _ := cmd.Flags().GetString("comment")
	client := newAPIClient()

	body := map[string]string{"status": status}
	if comment != "" {
		body["comment"] = comment
	}
	resp, err := client.Post("/api/v1/approvals/"+url.PathEscape(id)+"/decide", body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}

	var out struct {
		Status    string `json:"status"`
		DecidedBy string `json:"decided_by"`
	}
	if err := cli.ReadJSON(resp, &out); err != nil {
		return err
	}

	cli.PrintSuccess(fmt.Sprintf("Approval %s: %s (by %s)", id, out.Status, out.DecidedBy))
	return nil
}

func init() {
	approvalsListCmd.Flags().String("status", "pending", "Filter by status: pending|approved|denied|timeout|cancelled|all")
	approvalsListCmd.Flags().Int("limit", 50, "Max rows to return (server caps at 200)")

	approvalsApproveCmd.Flags().String("comment", "", "Optional comment recorded with the decision")
	approvalsDenyCmd.Flags().String("comment", "", "Optional comment recorded with the decision")

	approvalsCmd.AddCommand(approvalsListCmd)
	approvalsCmd.AddCommand(approvalsApproveCmd)
	approvalsCmd.AddCommand(approvalsDenyCmd)
	approvalsCmd.AddCommand(approvalsResetAutoTuningCmd)
}

// approvalsResetAutoTuningCmd wipes the rolling reward history for a
// tool so the next Gate() call falls back to the operator-requested
// mode. Use when a gate was auto-tuned toward approve (e.g. a period
// of rubber-stamping) and you want to re-sensitise humans to the
// same decision type without editing the gate rule itself.
var approvalsResetAutoTuningCmd = &cobra.Command{
	Use:   "reset-auto-tuning <tool>",
	Short: "Wipe the rolling reward history for a tool (re-train Harbor Master gating)",
	Long: `Wipe the reward history used by Harbor Master gate auto-tuning for the given tool.

Auto-tuning downgrades sync→async after 90%+ approvals and upgrades async→sync
after 70%+ denials, over the last 20 decisions per tool+args shape. When this
goes wrong (e.g. automation approved on behalf of humans for a while, skewing
the window) operators can reset to make the gate respect the configured mode
again for the next decisions, until humans re-train it naturally.

Requires OWNER or ADMIN on the caller's workspace.

Examples:
  crewship approvals reset-auto-tuning shell.exec
  crewship approvals reset-auto-tuning "terraform apply"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/approvals/reset-auto-tuning", map[string]string{"tool": args[0]})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Tool        string `json:"tool"`
			RowsDeleted int    `json:"rows_deleted"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		fmt.Printf("Reset auto-tuning for %q — cleared %d rows from gate_reward_history\n",
			out.Tool, out.RowsDeleted)
		return nil
	},
}
