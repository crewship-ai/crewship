package main

// eval get subcommand. Closes #1191: `eval replay` / `eval regression`
// queue a job (er_... id) and reach status "completed", but until this
// command existed there was no way to read back what the run actually
// found. `eval runs` only ever rendered id/kind/mission/baseline/status/
// created — dropping the result (verdict text), signature, and token/cost
// totals the API was already returning. `eval get <id>` re-fetches one
// run by id and prints all of it; GET /api/v1/eval/runs/{id} is the
// backing endpoint (mirrors the runs/{id} shape of GET .../runs).

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// evalRunDetail mirrors quartermaster.RunRecord's JSON shape. Decoded
// locally (rather than importing the server-side quartermaster package)
// to keep the CLI decoupled from server internals — same convention
// evalRunsCmd already uses for the list endpoint.
type evalRunDetail struct {
	ID                 string  `json:"id"`
	WorkspaceID        string  `json:"workspace_id"`
	Kind               string  `json:"kind"`
	MissionID          string  `json:"mission_id,omitempty"`
	BaselineMissionID  string  `json:"baseline_mission_id,omitempty"`
	CandidateMissionID string  `json:"candidate_mission_id,omitempty"`
	Status             string  `json:"status"`
	Result             string  `json:"result,omitempty"`
	Seed               int64   `json:"seed"`
	Signature          string  `json:"signature,omitempty"`
	TotalTokens        int64   `json:"total_tokens"`
	TotalCostUSD       float64 `json:"total_cost_usd"`
	Regressed          bool    `json:"regressed"`
	CreatedBy          string  `json:"created_by,omitempty"`
	CreatedAt          string  `json:"created_at"`
	CompletedAt        string  `json:"completed_at,omitempty"`
}

var evalGetCmd = &cobra.Command{
	Use:     "get <run-id>",
	Aliases: []string{"show"},
	Short:   "Show a single eval run's result (verdict, diff summary, metrics)",
	Long: `Re-fetches one replay/regression run by id and prints what it found —
the result/verdict text, regression flag, seed signature, and token/cost
totals that 'eval runs' only ever showed a status line for.

  crewship eval get er_a1b2c3d4e5f60718
  crewship eval get er_a1b2c3d4e5f60718 --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		resp, err := client.Get("/api/v1/eval/runs/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var run evalRunDetail
		if err := cli.ReadJSON(resp, &run); err != nil {
			return err
		}

		f := resolvedFormatter(cmd)
		switch f.Format {
		case "json":
			return f.JSON(run)
		case "yaml":
			return f.YAML(run)
		case "ndjson":
			return f.NDJSON(run)
		}

		fmt.Printf("Run %s: %s (%s)\n", run.ID, strings.ToUpper(run.Status), run.Kind)
		switch run.Kind {
		case "regression":
			fmt.Printf("Baseline: %s  Candidate: %s\n", run.BaselineMissionID, run.CandidateMissionID)
			fmt.Printf("Regressed: %t\n", run.Regressed)
		default:
			if run.MissionID != "" {
				fmt.Printf("Mission: %s\n", run.MissionID)
			}
			if run.Seed != 0 {
				fmt.Printf("Seed: %d\n", run.Seed)
			}
			if run.Signature != "" {
				fmt.Printf("Signature: %s\n", run.Signature)
			}
		}
		if run.TotalTokens > 0 || run.TotalCostUSD > 0 {
			fmt.Printf("Tokens: %d  Cost: $%.4f\n", run.TotalTokens, run.TotalCostUSD)
		}
		fmt.Printf("Created: %s", run.CreatedAt)
		if run.CompletedAt != "" {
			fmt.Printf("  Completed: %s", run.CompletedAt)
		}
		fmt.Println()

		if run.Result != "" {
			fmt.Println("\nResult:")
			fmt.Println(indent(run.Result, "  "))
		} else if run.Status == "completed" || run.Status == "failed" {
			fmt.Println("\n(no result recorded for this run)")
		} else {
			fmt.Printf("\n(run is %s — no result yet)\n", run.Status)
		}
		return nil
	},
}

func init() {
	evalCmd.AddCommand(evalGetCmd)
}
