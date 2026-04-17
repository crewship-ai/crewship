package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// evalCmd is the CLI surface for mission replay and regression testing.
// Live against:
//
//	POST /api/v1/eval/replay       — replay a mission with a seed
//	POST /api/v1/eval/regression   — diff baseline vs candidate
//	GET  /api/v1/eval/runs         — list recent eval runs
//
// Both replay + regression return a queued run_id; the actual work
// happens asynchronously. Use `crewship eval runs` to poll completion.
var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Mission replay and regression evaluation",
	Long: `Evaluate agent behavior by replaying a mission with a fixed seed or
diffing a candidate mission against a baseline.

Examples:
  crewship eval replay MIS-42 --seed 42
  crewship eval regression MIS-41 MIS-42
  crewship eval runs --limit 20`,
}

var evalReplayCmd = &cobra.Command{
	Use:   "replay <mission-id>",
	Short: "Replay a mission deterministically",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		seed, _ := cmd.Flags().GetInt("seed")
		body := map[string]any{"mission_id": args[0]}
		if seed != 0 {
			body["seed"] = seed
		}
		resp, err := client.Post("/api/v1/eval/replay", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		fmt.Printf("Replay queued: run_id=%s status=%s\n", out.RunID, out.Status)
		return nil
	},
}

var evalRegressionCmd = &cobra.Command{
	Use:   "regression <baseline-id> <candidate-id>",
	Short: "Regression-diff two missions",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		body := map[string]any{
			"baseline_mission_id":  args[0],
			"candidate_mission_id": args[1],
		}
		resp, err := client.Post("/api/v1/eval/regression", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		fmt.Printf("Regression queued: run_id=%s status=%s\n", out.RunID, out.Status)
		return nil
	},
}

var evalRunsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List recent eval runs",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		limit, _ := cmd.Flags().GetInt("limit")
		path := "/api/v1/eval/runs"
		if limit > 0 {
			path += fmt.Sprintf("?limit=%d", limit)
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Rows []struct {
				ID         string `json:"id"`
				Kind       string `json:"kind"`
				MissionID  string `json:"mission_id"`
				BaselineID string `json:"baseline_mission_id"`
				Status     string `json:"status"`
				CreatedAt  string `json:"created_at"`
			} `json:"rows"`
			Count int `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body.Rows)
		}
		if f.Format == "yaml" {
			return f.YAML(body.Rows)
		}

		if len(body.Rows) == 0 {
			fmt.Println("(no eval runs recorded yet)")
			return nil
		}
		header := []string{"ID", "KIND", "MISSION", "BASELINE", "STATUS", "CREATED"}
		rows := make([][]string, 0, len(body.Rows))
		for _, r := range body.Rows {
			rows = append(rows, []string{r.ID, r.Kind, r.MissionID, r.BaselineID, r.Status, r.CreatedAt})
		}
		f.Table(header, rows)
		return nil
	},
}

func init() {
	evalReplayCmd.Flags().Int("seed", 0, "Deterministic seed for the replay (0 = server default)")
	evalRunsCmd.Flags().Int("limit", 50, "Max rows to fetch (1-500)")

	evalCmd.AddCommand(evalReplayCmd)
	evalCmd.AddCommand(evalRegressionCmd)
	evalCmd.AddCommand(evalRunsCmd)
}
