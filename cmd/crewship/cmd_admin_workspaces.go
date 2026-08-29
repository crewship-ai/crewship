//go:build !clionly

package main

// `crewship admin workspaces` — GET /api/v1/admin/workspaces (#2147).
//
// The endpoint had no CLI command. It is server-backed (not the
// local-only recovery family in cmd_admin.go): it returns the CURRENT
// workspace — the one the CLI's auth/workspace context resolves to —
// together with member/agent/crew counts, scoped server-side so it can
// never leak another workspace's data. It is not a multi-tenant listing
// despite the plural name; see internal/api/admin.go's ListWorkspaces,
// which filters `WHERE w.id = <the caller's workspace>`.
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	MemberCount int    `json:"_count_members"`
	AgentCount  int    `json:"_count_agents"`
	CrewCount   int    `json:"_count_crews"`
}

var adminWorkspacesCmd = &cobra.Command{
	Use:   "workspaces",
	Short: "Show the current workspace with member/agent/crew counts (admin)",
	Long: `GET /api/v1/admin/workspaces, scoped server-side to the workspace the CLI
is currently authenticated against. Despite the plural name this is not an
instance-wide tenant listing — it returns at most one row, the caller's own
workspace, with counts for members, agents and crews.

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
			fmt.Fprintln(w, "NAME\tSLUG\tMEMBERS\tAGENTS\tCREWS\tCREATED")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\n",
					r.Name, r.Slug, r.MemberCount, r.AgentCount, r.CrewCount, r.CreatedAt)
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
