package main

import (
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/tui"
)

// tuiCmd opens the Bubble Tea dashboard. Lightweight wiring file —
// all model/view code lives in internal/cli/tui.
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Real-time TUI dashboard (Mission Control)",
	Long: `Open a real-time dashboard showing running missions/runs, pending
approvals, and a live journal stream.

Keys:
  q / Ctrl-C  quit
  r           force refresh
  Tab         cycle panel focus
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		server := cli.ResolveServer(flagServer, cliCfg)
		return tui.Run(cmd.Context(), client, server)
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
