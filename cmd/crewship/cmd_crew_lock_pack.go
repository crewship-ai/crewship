package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

var crewLockPackCmd = &cobra.Command{
	Use:   "lock-pack <directory>",
	Short: "Package a local mise lock and auxiliary files as JSON (no server changes)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bundle, err := devcontainer.ReadMiseLockBundle(args[0])
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(bundle)
	},
}

func init() { crewProvisionCmd.AddCommand(crewLockPackCmd) }
