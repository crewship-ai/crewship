//go:build !clionly

package main

// `crewship admin memory-stats` — GET /api/v1/admin/memory/stats (#2147).
//
// Companion to `admin memory-config`: memory-config adjusts the retention
// policy, this reads what is actually being retained. Before this command
// the endpoint (added for the dashboard's "memory health" widget — see
// internal/api/memory_stats_handler.go) had no CLI client, so an operator
// without the web UI in front of them had no way to answer "how much memory
// data does this workspace have, and where is it".
//
// Server-backed, ADMIN+ (canRole "manage"), scoped to the CLI's current
// workspace via the same auth context every other `admin` HTTP-backed
// command uses.

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// memoryStatsTotalsRow mirrors internal/api.memoryStatsTotals.
type memoryStatsTotalsRow struct {
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
	Blobs    int    `json:"blobs"`
	OldestAt string `json:"oldest_at"`
	NewestAt string `json:"newest_at"`
}

// memoryStatsByTierRow mirrors internal/api.memoryStatsByTier.
type memoryStatsByTierRow struct {
	Tier     string `json:"tier"`
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
}

// memoryStatsByAgentRow mirrors internal/api.memoryStatsByAgent.
type memoryStatsByAgentRow struct {
	AgentSlug string `json:"agent_slug"`
	Versions  int    `json:"versions"`
	Bytes     int64  `json:"bytes"`
	NewestAt  string `json:"newest_at"`
}

// adminMemoryStatsResult mirrors internal/api.memoryStatsResponse.
type adminMemoryStatsResult struct {
	WorkspaceID string                  `json:"workspace_id"`
	Totals      memoryStatsTotalsRow    `json:"totals"`
	ByTier      []memoryStatsByTierRow  `json:"by_tier"`
	ByAgent     []memoryStatsByAgentRow `json:"by_agent"`
}

var adminMemoryStatsCmd = &cobra.Command{
	Use:   "memory-stats",
	Short: "Show memory_versions counts and byte totals for this workspace (admin)",
	Long: `GET /api/v1/admin/memory/stats — how much memory data the current
workspace is carrying: total versions/bytes/distinct blobs, a breakdown by
tier (agent/crew/workspace), and a breakdown by agent slug.

The companion read to "admin memory-config": memory-config adjusts the
retention window, this shows what is actually being retained under it.

Requires OWNER or ADMIN (canRole "manage").

Examples:
  crewship admin memory-stats
  crewship admin memory-stats --format json | jq '.totals.bytes'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		resp, err := client.Get("/api/v1/admin/memory/stats")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result adminMemoryStatsResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(result, func() {
			t := result.Totals
			fmt.Printf("Workspace %s\n", result.WorkspaceID)
			fmt.Printf("  %d version(s), %s across %d distinct blob(s)\n",
				t.Versions, humanBytes(t.Bytes), t.Blobs)
			if t.OldestAt != "" || t.NewestAt != "" {
				fmt.Printf("  oldest: %s   newest: %s\n", orDash(t.OldestAt), orDash(t.NewestAt))
			}

			if len(result.ByTier) > 0 {
				fmt.Println("\nBy tier:")
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "TIER\tVERSIONS\tBYTES")
				for _, r := range result.ByTier {
					fmt.Fprintf(w, "%s\t%d\t%s\n", r.Tier, r.Versions, humanBytes(r.Bytes))
				}
				_ = w.Flush()
			}

			if len(result.ByAgent) > 0 {
				fmt.Println("\nBy agent:")
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "AGENT\tVERSIONS\tBYTES\tNEWEST")
				for _, r := range result.ByAgent {
					agent := r.AgentSlug
					if agent == "" {
						agent = "(shared: crew/workspace tier)"
					}
					fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", agent, r.Versions, humanBytes(r.Bytes), orDash(r.NewestAt))
				}
				_ = w.Flush()
			}

			if t.Versions == 0 {
				fmt.Println("\n(no memory_versions rows for this workspace)")
			}
		})
	},
}

func init() {
	adminCmd.AddCommand(adminMemoryStatsCmd)
}
