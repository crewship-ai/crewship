package main

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// exposeCmd groups port-exposure administration commands. Agents create
// exposures themselves via the sidecar `/expose-port` endpoint; this CLI
// is for the human side: audit (list) and teardown (revoke). MVP doesn't
// ship an `approve` subcommand because the default policy is open — when
// a future policy introduces approval, `approve` lands here alongside the
// existing verbs.
var exposeCmd = &cobra.Command{
	Use:   "expose",
	Short: "Manage agent-initiated port exposures (capability URLs)",
}

// exposeListCmd lists exposures for a crew. We always scope to a single
// crew because the server enforces crew-level auth on the list endpoint;
// trying to list across crews would require N calls anyway.
var exposeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List port exposures for a crew",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		crewSlug, _ := cmd.Flags().GetString("crew")
		statusFilter, _ := cmd.Flags().GetString("status")
		if crewSlug == "" {
			return fmt.Errorf("--crew is required (crew slug or ID)")
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		path := "/api/v1/crews/" + crewID + "/port-expose"
		if statusFilter != "" {
			path += "?status=" + strings.ToLower(statusFilter)
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var items []struct {
			ID            string  `json:"id"`
			AgentSlug     string  `json:"agent_slug"`
			ContainerPort int     `json:"container_port"`
			Description   string  `json:"description,omitempty"`
			Status        string  `json:"status"`
			ExpiresAt     string  `json:"expires_at"`
			RevokedAt     *string `json:"revoked_at,omitempty"`
			RevokedReason *string `json:"revoked_reason,omitempty"`
			CreatedAt     string  `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "AGENT", "PORT", "STATUS", "EXPIRES", "DESCRIPTION"}
		rows := make([][]string, 0, len(items))
		for _, it := range items {
			idStr := it.ID
			if len(idStr) > 14 {
				idStr = idStr[:14]
			}
			desc := it.Description
			if len(desc) > 40 {
				desc = desc[:37] + "..."
			}
			rows = append(rows, []string{
				idStr,
				it.AgentSlug,
				fmt.Sprintf("%d", it.ContainerPort),
				it.Status,
				it.ExpiresAt,
				desc,
			})
		}
		return f.Auto(items, headers, rows)
	},
}

// exposeRevokeCmd flips an active exposure to REVOKED. Requires MANAGER+
// (same as escalation resolve). The reason ends up in the audit row, which
// is the right place to record WHY you killed a teammate's demo URL.
var exposeRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an active port exposure",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		crewSlug, _ := cmd.Flags().GetString("crew")
		reason, _ := cmd.Flags().GetString("reason")
		if crewSlug == "" {
			return fmt.Errorf("--crew is required (crew slug or ID)")
		}
		if err := confirmAction(cmd, fmt.Sprintf("Revoke port exposure %q on crew %q?", args[0], crewSlug)); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		if reason != "" {
			body["reason"] = reason
		}
		resp, err := client.Post("/api/v1/crews/"+crewID+"/port-expose/"+args[0]+"/revoke", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Exposure %s revoked.", args[0]))
		return nil
	},
}

func init() {
	exposeListCmd.Flags().String("crew", "", "Crew slug or ID (required)")
	exposeListCmd.Flags().String("status", "", "Filter by status: active|revoked|expired|all (default: active)")

	exposeRevokeCmd.Flags().String("crew", "", "Crew slug or ID (required)")
	exposeRevokeCmd.Flags().String("reason", "", "Optional human-readable reason recorded in audit")
	exposeRevokeCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	exposeCmd.AddCommand(exposeListCmd)
	exposeCmd.AddCommand(exposeRevokeCmd)
}
