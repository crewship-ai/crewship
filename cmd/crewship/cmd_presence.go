package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// presenceCmd is the Watch Roster surface — a live view of who is
// online/busy/blocked/offline across a crew or workspace.
//
// Status: STUB. Backend endpoint GET /api/v1/presence/roster is not
// yet live; this subcommand is pre-wired so the CLI shape is stable
// ahead of the backend work.
var presenceCmd = &cobra.Command{
	Use:   "presence",
	Short: "Watch Roster — who is online/busy/blocked (stub — backend wiring pending)",
	Long: `Show the Watch Roster — the live presence board tracking agent status
(online, busy, blocked, offline) across a crew or the full workspace.

STATUS: stub. Backend endpoint pending:
  GET /api/v1/presence/roster[?crew_id=...]

Planned usage:
  crewship presence roster
  crewship presence roster --crew backend-team`,
}

var presenceRosterCmd = &cobra.Command{
	Use:   "roster",
	Short: "Show the current presence roster (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("presence roster: endpoint not yet available — backend wiring pending")
	},
}

func init() {
	presenceRosterCmd.Flags().String("crew", "", "Filter by crew slug or ID")

	presenceCmd.AddCommand(presenceRosterCmd)
}
