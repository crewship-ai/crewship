//go:build !clionly

package main

// `crewship admin workspaces` — GET /api/v1/admin/workspaces (#2147).
//
// The endpoint had no CLI command. It is server-backed (not the
// local-only recovery family in cmd_admin.go): it returns the CURRENT
// workspace — the one the CLI's auth/workspace context resolves to — or,
// for an instance administrator, every workspace on the instance, each with
// member/agent/crew counts, runs over the last week, spend over the last
// 30 days and the last audited change. Scope is decided server-side (see
// internal/api/admin_people.go) and reported in the X-Admin-Scope header.
//
// Deliberately mirrors `crewship admin stats` (server-backed, ADMIN+,
// same file-per-command convention) rather than the `--local` family:
// there is no host-only equivalent, because "workspace + counts" is
// exactly what the server is positioned to answer and a raw SQLite
// read would still need to pick a workspace.

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// adminWorkspaceRow mirrors internal/api.AdminHandler.ListWorkspaces's wsRow.
type adminWorkspaceRow struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Slug        string `json:"slug" yaml:"slug"`
	CreatedAt   string `json:"created_at" yaml:"created_at"`
	UpdatedAt   string `json:"updated_at" yaml:"updated_at"`
	MemberCount int    `json:"_count_members" yaml:"_count_members"`
	AgentCount  int    `json:"_count_agents" yaml:"_count_agents"`
	CrewCount   int    `json:"_count_crews" yaml:"_count_crews"`
	// Nil from a server older than these fields — printed as "-", not 0.
	Runs7d         *int     `json:"runs_7d,omitempty" yaml:"runs_7d,omitempty"`
	Cost30dUSD     *float64 `json:"cost_30d_usd,omitempty" yaml:"cost_30d_usd,omitempty"`
	LastActivityAt *string  `json:"last_activity_at,omitempty" yaml:"last_activity_at,omitempty"`
	Current        bool     `json:"current,omitempty" yaml:"current,omitempty"`
}

var adminWorkspacesCmd = &cobra.Command{
	Use:   "workspaces",
	Short: "List the workspaces you administer, with counts, runs and spend (admin)",
	Long: `GET /api/v1/admin/workspaces. A workspace ADMIN or OWNER gets one row, the
workspace the CLI is authenticated against; an instance administrator
(see 'crewship admin instance') gets every workspace on the instance.
Each row has counts for members, agents and crews, runs in the last 7 days,
spend in the last 30 days and the time of the last audited change; the
current workspace is marked with *.

Requires OWNER or ADMIN (canRole "manage").

Examples:
  crewship admin workspaces
  crewship admin workspaces --format json | jq '.[0]._count_agents'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		resp, err := client.Get("/api/v1/admin/workspaces")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []adminWorkspaceRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(rows, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSLUG\tMEMBERS\tAGENTS\tCREWS\tRUNS 7D\tSPEND 30D\tLAST ACTIVITY\tCREATED")
			for _, r := range rows {
				name := r.Name
				if r.Current {
					name += " *"
				}
				runs, spend, last := "-", "-", "-"
				if r.Runs7d != nil {
					runs = fmt.Sprintf("%d", *r.Runs7d)
				}
				if r.Cost30dUSD != nil {
					spend = fmt.Sprintf("$%.2f", *r.Cost30dUSD)
				}
				if r.LastActivityAt != nil {
					last = shortAdminTime(*r.LastActivityAt)
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\n",
					name, r.Slug, r.MemberCount, r.AgentCount, r.CrewCount, runs, spend, last, r.CreatedAt)
			}
			_ = w.Flush()
			if len(rows) == 0 {
				fmt.Println("(no workspace in context)")
			}
		})
	},
}

func init() {
	adminCmd.AddCommand(adminWorkspacesCmd)
}
