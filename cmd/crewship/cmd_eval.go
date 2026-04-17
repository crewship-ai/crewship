package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// evalCmd is a CLI surface for mission replay and regression testing.
//
// Status: STUB. The backend endpoints this command exists to call
// (POST /api/v1/eval/replay, POST /api/v1/eval/regression) are not yet
// implemented. The cobra skeleton is pre-wired so the surface area is
// discoverable via `crewship eval --help` and downstream consumers can
// plan integrations against a stable CLI shape.
var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Mission replay and regression evaluation (stub — backend wiring pending)",
	Long: `Evaluate agent behavior by replaying a mission with a fixed seed or
diffing a candidate mission against a baseline.

STATUS: stub. Subcommands return "endpoint not yet available" until the
backend surfaces:
  POST /api/v1/eval/replay       — replay a mission with a seed
  POST /api/v1/eval/regression   — diff baseline vs candidate

Planned usage once live:
  crewship eval replay <mission-id> --seed 42
  crewship eval regression <baseline-id> <candidate-id>`,
}

var evalReplayCmd = &cobra.Command{
	Use:   "replay <mission-id>",
	Short: "Replay a mission deterministically (stub)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("eval replay: endpoint not yet available — backend wiring pending")
	},
}

var evalRegressionCmd = &cobra.Command{
	Use:   "regression <baseline-id> <candidate-id>",
	Short: "Regression-diff two missions (stub)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("eval regression: endpoint not yet available — backend wiring pending")
	},
}

func init() {
	evalReplayCmd.Flags().Int("seed", 0, "Deterministic seed for the replay (0 = server default)")

	evalCmd.AddCommand(evalReplayCmd)
	evalCmd.AddCommand(evalRegressionCmd)
}
