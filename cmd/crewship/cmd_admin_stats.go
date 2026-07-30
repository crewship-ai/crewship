//go:build !clionly

package main

// `crewship admin stats` — the workspace's counts, read against the licensed
// ceilings rather than as bare numbers.
//
// GET /api/v1/admin/stats had no CLI command (CLAUDE.md rule 3). The numbers
// on their own answer little: "8 agents" is not a fact anyone acts on, while
// "3 of 15 crews" is.

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type adminStatsRow struct {
	Workspaces int `json:"workspaces"`
	Users      int `json:"users"`
	Crews      int `json:"crews"`
	Agents     int `json:"agents"`
	Running    int `json:"running"`
}

type adminLicenseRow struct {
	Edition          string `json:"edition"`
	MaxCrews         int    `json:"max_crews"`
	MaxAgentsPerCrew int    `json:"max_agents_per_crew"`
	MaxMembers       int    `json:"max_members"`
}

// against renders "3 of 15" when a ceiling applies, and the bare count when
// the edition does not cap that dimension (0 = unlimited).
func against(used, limit int) string {
	if limit <= 0 {
		return fmt.Sprintf("%d", used)
	}
	return fmt.Sprintf("%d of %d", used, limit)
}

var adminStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show workspace counts against the licensed limits (admin)",
	Long: `Counts for the current workspace: crews, agents, members and runs in flight,
each shown against the ceiling the licence imposes on it.

Examples:
  crewship admin stats
  crewship admin stats --format json | jq '.crews'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		resp, err := client.Get("/api/v1/admin/stats")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row adminStatsRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		// The licence is a separate, non-admin endpoint; a failure to read it
		// costs the ceilings, not the counts, so it never fails the command.
		var lic adminLicenseRow
		if lresp, lerr := client.Get("/api/v1/system/license"); lerr == nil {
			defer lresp.Body.Close()
			_ = json.NewDecoder(lresp.Body).Decode(&lic)
		}

		return resolvedFormatter(cmd).AutoHuman(row, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "COUNT\tVALUE")
			fmt.Fprintf(w, "Crews\t%s\n", against(row.Crews, lic.MaxCrews))
			fmt.Fprintf(w, "Agents\t%d\n", row.Agents)
			fmt.Fprintf(w, "Members\t%s\n", against(row.Users, lic.MaxMembers))
			fmt.Fprintf(w, "Running now\t%d\n", row.Running)
			if lic.Edition != "" {
				fmt.Fprintf(w, "Edition\t%s\n", lic.Edition)
			}
			_ = w.Flush()
		})
	},
}

func init() {
	adminCmd.AddCommand(adminStatsCmd)
}
