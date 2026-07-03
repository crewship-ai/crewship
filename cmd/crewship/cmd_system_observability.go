package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// `crewship system log-level` and `crewship system health` drive the admin-gated
// observability API (runtime log-level toggle + disk/health read). CLI parity
// for /api/v1/admin/log-level and /api/v1/admin/health.

var systemLogLevelCmd = &cobra.Command{
	Use:   "log-level",
	Short: "Show the live log level (use 'set' to change it at runtime)",
	Long: `Show or change the server's log level at runtime — no restart needed.

Flip a misbehaving instance to debug, catch the repro in the logs, and let
it auto-revert with --ttl so a forgotten debug switch doesn't firehose the
logs (itself a disk-fill risk).

  crewship system log-level                       # current level
  crewship system log-level set --level debug --ttl 15m
  crewship system log-level set --level info      # revert now`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/admin/log-level", &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

var systemLogLevelSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Change the log level at runtime (debug|info|warn|error)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		level, _ := cmd.Flags().GetString("level")
		if level == "" {
			return fmt.Errorf("--level is required (debug|info|warn|error)")
		}
		ttl, _ := cmd.Flags().GetDuration("ttl")
		reqBody := map[string]any{"level": level}
		if ttl > 0 {
			reqBody["ttl_seconds"] = int(ttl.Seconds())
		}
		var body any
		if err := putJSON(client, "/api/v1/admin/log-level", reqBody, &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

var systemHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show server health: uptime, log level, and disk headroom",
	Long: `Report the running server's uptime, current log level, and disk usage
for the data-dir volume — the signal that flags a filling disk before it
hits 100%.

  crewship system health
  crewship system health -f json | jq '.disk.used_pct'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/admin/health", &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

func init() {
	systemLogLevelSetCmd.Flags().String("level", "", "log level: debug|info|warn|error (required)")
	systemLogLevelSetCmd.Flags().Duration("ttl", 0, "auto-revert to the baseline after this duration (e.g. 15m; 0 = until next change)")
	systemLogLevelCmd.AddCommand(systemLogLevelSetCmd)

	systemCmd.AddCommand(systemLogLevelCmd)
	systemCmd.AddCommand(systemHealthCmd)
}
