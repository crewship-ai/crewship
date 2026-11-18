package main

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/buildinfo"
)

// This internal, network-free preflight bypasses the human configuration hook.
// The provider invokes its canonical read-only mount, never agent PATH.
var agentRuntimeProbeCmd = &cobra.Command{
	Use:               "agent-runtime-probe",
	Short:             "Report the executing agent SDK artifact and available native commands",
	Hidden:            true,
	Args:              cobra.NoArgs,
	PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	RunE: func(cmd *cobra.Command, _ []string) error {
		probe, err := buildinfo.ProbeAgentRuntime(version, commit, date, agentSDKCommandPaths(rootCmd))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(probe)
	},
}

// The backend and clionly client independently inspect their actual command
// trees. A command is required/advertised only when this build really has it;
// e.g. a pre-Keeper SDK cannot claim the new agent Keeper commands.
func agentSDKCommandPaths(root *cobra.Command) []string {
	var paths []string
	for _, path := range []string{"version", "commands", "keeper request", "keeper execute", "keeper request-status"} {
		cmd, rest, err := root.Find(strings.Fields(path))
		if err == nil && len(rest) == 0 && cmd.Runnable() && strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ") == path {
			paths = append(paths, path)
		}
	}
	return paths
}

func init() { rootCmd.AddCommand(agentRuntimeProbeCmd) }
