package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// consolidateCmd triggers the memory-consolidation worker — the
// background job that compacts agent/crew memory into summaries.
//
// Status: STUB. Backend endpoint POST /api/v1/consolidate is not yet
// live. Keeping the cobra skeleton wired so the CLI contract is stable
// before the worker ships.
var consolidateCmd = &cobra.Command{
	Use:   "consolidate",
	Short: "Force a memory consolidation run (stub — backend wiring pending)",
	Long: `Trigger the memory-consolidation worker — the background process that
compacts agent/crew long-running memory into summaries. Normally runs on
a schedule; this command forces an immediate run.

STATUS: stub. Backend endpoint pending:
  POST /api/v1/consolidate[?crew_id=...]

Planned usage:
  crewship consolidate run
  crewship consolidate run --crew backend-team`,
}

var consolidateRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Force an immediate consolidation run (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("consolidate run: endpoint not yet available — backend wiring pending")
	},
}

func init() {
	consolidateRunCmd.Flags().String("crew", "", "Limit consolidation to a single crew (slug or ID)")

	consolidateCmd.AddCommand(consolidateRunCmd)
}
