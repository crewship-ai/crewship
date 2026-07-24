package main

// Per-routine monthly budget meter CLI (#1422 item 3). The engine
// already enforces a per-run hard cost gate (DSL.max_cost_usd) and does
// cost-aware retry; this is the missing budget-vs-actual VIEW: an
// out-of-band monthly spend cap (independent of the DSL, never touched
// by `routine save`) compared against actual pipeline_runs.cost_usd for
// the current calendar month.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// budgetRow mirrors internal/api.budgetResponse / budgetSummaryRow —
// both shapes fit in one struct since budgetSummaryRow is a strict
// subset of budgetResponse's fields.
type budgetRow struct {
	Slug             string  `json:"slug"`
	HasBudget        bool    `json:"has_budget"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	Month            string  `json:"month,omitempty"`
	SpentUSD         float64 `json:"spent_usd"`
	PctUsed          float64 `json:"pct_used,omitempty"`
	OverBudget       bool    `json:"over_budget,omitempty"`
}

// budgetSummaryRowsResponse mirrors internal/api.budgetSummaryResponse.
type budgetSummaryRowsResponse struct {
	Month          string      `json:"month"`
	Routines       []budgetRow `json:"routines"`
	TotalBudgetUSD float64     `json:"total_budget_usd"`
	TotalSpentUSD  float64     `json:"total_spent_usd"`
}

var routineBudgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Per-routine monthly spend budget — view actuals or set a cap",
	Long: `A routine's monthly budget is an out-of-band spend cap (set here, not
in the DSL) compared against actual pipeline_runs.cost_usd for the
current calendar month. Distinct from a routine's DSL max_cost_usd,
which is a per-RUN hard gate authored into the definition itself and
enforced mid-run by the executor — this is a budget-vs-actual VIEW, plus
an optional monthly cap. A routine can have neither, either, or both.

Examples:
  crewship routine budget get my-routine
  crewship routine budget set my-routine --amount 50
  crewship routine budget set my-routine --amount 0   # clear the cap
  crewship routine budget summary
`,
}

var routineBudgetGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show a routine's monthly budget vs actual spend",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/budget", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row budgetRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			if !row.HasBudget {
				fmt.Printf("%s: no budget set (spent $%.2f this month, %s). Set one: crewship routine budget set %s --amount <N>\n",
					row.Slug, row.SpentUSD, row.Month, row.Slug)
				return
			}
			flag := ""
			if row.OverBudget {
				flag = "  OVER BUDGET"
			}
			fmt.Printf("%s (%s): %s\n", row.Slug, row.Month, budgetMeterBar(row.SpentUSD, row.MonthlyBudgetUSD))
			fmt.Printf("  $%.2f of $%.2f (%.0f%%)%s\n", row.SpentUSD, row.MonthlyBudgetUSD, row.PctUsed, flag)
		})
	},
}

var routineBudgetSetCmd = &cobra.Command{
	Use:   "set <slug>",
	Short: "Set (or clear) a routine's monthly budget cap",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("amount") {
			return fmt.Errorf("--amount <N> is required (0 clears the budget)")
		}
		amount, _ := cmd.Flags().GetFloat64("amount")
		if amount < 0 {
			return fmt.Errorf("--amount cannot be negative")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/budget", ws, args[0]),
			map[string]interface{}{"monthly_budget_usd": amount},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row budgetRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			if !row.HasBudget {
				fmt.Printf("Budget cleared for %s.\n", args[0])
				return
			}
			fmt.Printf("Budget set for %s: $%.2f/month\n", args[0], row.MonthlyBudgetUSD)
		})
	},
}

var routineBudgetSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Workspace roll-up of every routine with a budget set or spend this month",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/budget-summary", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out budgetSummaryRowsResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		f := resolvedFormatter(cmd)
		if f.Format == "json" {
			return f.JSON(out)
		}
		switch f.Format {
		case "yaml":
			return f.YAML(out)
		case "ndjson":
			return f.NDJSON(out.Routines)
		}
		if len(out.Routines) == 0 {
			fmt.Printf("No routines with a budget set or spend this month (%s).\n", out.Month)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ROUTINE\tBUDGET\tSPENT\tPCT\tSTATUS")
		for _, r := range out.Routines {
			budget := "—"
			pct := "—"
			status := ""
			if r.HasBudget {
				budget = fmt.Sprintf("$%.2f", r.MonthlyBudgetUSD)
				pct = fmt.Sprintf("%.0f%%", r.PctUsed)
				if r.OverBudget {
					status = "OVER"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t$%.2f\t%s\t%s\n", r.Slug, budget, r.SpentUSD, pct, status)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("\n%s total: $%.2f spent of $%.2f budgeted\n", out.Month, out.TotalSpentUSD, out.TotalBudgetUSD)
		return nil
	},
}

// budgetMeterBar renders a compact ASCII progress bar for the terminal —
// same 20-cell convention as other CLI meters in this codebase, filled
// proportionally to spent/cap (capped visually at 100% even when
// over_budget pushes the ratio past 1.0).
func budgetMeterBar(spent, budgetCap float64) string {
	const width = 20
	ratio := 0.0
	if budgetCap > 0 {
		ratio = spent / budgetCap
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * width)
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]"
}

func init() {
	routineBudgetSetCmd.Flags().Float64("amount", 0, "monthly budget cap in USD (0 clears an existing budget) — REQUIRED")

	routineBudgetCmd.AddCommand(routineBudgetGetCmd)
	routineBudgetCmd.AddCommand(routineBudgetSetCmd)
	routineBudgetCmd.AddCommand(routineBudgetSummaryCmd)
	pipelineCmd.AddCommand(routineBudgetCmd)
}
