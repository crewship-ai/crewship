package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// hooksCmd is the CLI surface for the lifecycle-hook system: registered
// callbacks that fire on journal events, assignment transitions, and
// similar platform lifecycle moments.
//
// Status: STUB. The backend endpoints (GET/POST /api/v1/hooks,
// POST /api/v1/hooks/{id}/enable|disable) are not yet live. The
// subcommand skeletons exist so `crewship hooks --help` is informative
// and integration work can target a stable surface.
var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Lifecycle hooks registry (stub — backend wiring pending)",
	Long: `Manage the lifecycle-hook registry — scripts or webhooks that fire on
platform events (assignment.completed, journal.entry, keeper.decision, …).

STATUS: stub. Subcommands return "endpoint not yet available" until the
backend surfaces the hooks registry at:
  GET  /api/v1/hooks
  POST /api/v1/hooks
  POST /api/v1/hooks/{id}/enable
  POST /api/v1/hooks/{id}/disable

Planned usage:
  crewship hooks list
  crewship hooks register                 # config-file driven (path TBD)
  crewship hooks enable <id>
  crewship hooks disable <id>`,
}

var hooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered lifecycle hooks (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("hooks list: endpoint not yet available — backend wiring pending")
	},
}

var hooksEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable a registered hook (stub)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("hooks enable: endpoint not yet available — backend wiring pending")
	},
}

var hooksDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable a registered hook (stub)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("hooks disable: endpoint not yet available — backend wiring pending")
	},
}

var hooksRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a new hook from a config file (stub)",
	Long: `Register a new lifecycle hook. Planned to accept a path to a hook
definition file (YAML/JSON); the exact shape is TBD pending the backend
contract. Currently returns a stub error.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("hooks register: endpoint not yet available — backend wiring pending")
	},
}

func init() {
	hooksCmd.AddCommand(hooksListCmd)
	hooksCmd.AddCommand(hooksEnableCmd)
	hooksCmd.AddCommand(hooksDisableCmd)
	hooksCmd.AddCommand(hooksRegisterCmd)
}
