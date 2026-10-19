package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// crewBudgetResponse mirrors GET/PUT/DELETE /api/v1/crews/{id}/budget.
type crewBudgetResponse struct {
	CrewID   string   `json:"crew_id" yaml:"crew_id"`
	Window   string   `json:"window" yaml:"window"`
	LimitUSD *float64 `json:"limit_usd" yaml:"limit_usd"`
	Mode     string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	SpentUSD float64  `json:"spent_usd" yaml:"spent_usd"`
	State    string   `json:"state" yaml:"state"`
}

func printCrewBudget(b crewBudgetResponse) error {
	limit := "no limit"
	if b.LimitUSD != nil {
		limit = fmt.Sprintf("$%.2f", *b.LimitUSD)
	}
	pairs := [][]string{
		{"Window", b.Window},
		{"Limit", limit},
		{"Mode", b.Mode},
		{"Spent", fmt.Sprintf("$%.2f", b.SpentUSD)},
		{"State", b.State},
	}
	return newFormatter().AutoDetail(b, pairs)
}

// runCrewBudget resolves the crew, makes the one call and prints the budget
// the server answers with.
func runCrewBudget(args []string, call func(c *cli.Client, path string) (*http.Response, error)) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	crewID, err := resolveCrewID(client, args[0])
	if err != nil {
		return err
	}
	resp, err := call(client, "/api/v1/crews/"+crewID+"/budget")
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var b crewBudgetResponse
	if err := cli.ReadJSON(resp, &b); err != nil {
		return err
	}
	return printCrewBudget(b)
}

var crewBudgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Show, set or clear a crew's monthly spend limit",
	Long: `A crew's monthly budget is the cap on what its agents may spend in a
calendar month (UTC). Below 80 % nothing happens; in the default tiered mode
a warning appears from 80 % and new runs stop at 100 %. Needs the Admin role.`,
}

var crewBudgetGetCmd = &cobra.Command{
	Use:   "get <slug-or-id>",
	Short: "Show the crew's monthly limit and this month's spend",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCrewBudget(args, func(c *cli.Client, path string) (*http.Response, error) {
			return c.Get(path)
		})
	},
}

var crewBudgetSetCmd = &cobra.Command{
	Use:   "set <slug-or-id> --limit <usd>",
	Short: "Set the crew's monthly limit",
	Long: `Set or replace the monthly limit in US dollars. --mode is tiered (the
default: warn at 80 %, stop at 100 %), soft (warn only) or hard (stop at 100 %).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, _ := cmd.Flags().GetString("limit")
		limit, perr := strconv.ParseFloat(raw, 64)
		if perr != nil {
			return fmt.Errorf("--limit must be a number of US dollars, got %q", raw)
		}
		if limit <= 0 {
			return fmt.Errorf("--limit must be greater than 0 (use 'crew budget clear' to remove the limit)")
		}
		body := map[string]any{"limit_usd": limit}
		if mode, _ := cmd.Flags().GetString("mode"); mode != "" {
			body["mode"] = mode
		}
		return runCrewBudget(args, func(c *cli.Client, path string) (*http.Response, error) {
			return c.Put(path, body)
		})
	},
}

var crewBudgetClearCmd = &cobra.Command{
	Use:   "clear <slug-or-id>",
	Short: "Remove the crew's monthly limit",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := confirmAction(cmd, fmt.Sprintf("Remove the monthly limit of crew %q?", args[0])); err != nil {
			return err
		}
		return runCrewBudget(args, func(c *cli.Client, path string) (*http.Response, error) {
			return c.Delete(path)
		})
	},
}

func init() {
	crewBudgetSetCmd.Flags().String("limit", "", "Monthly limit in US dollars (required)")
	crewBudgetSetCmd.Flags().String("mode", "", "tiered (default), soft or hard")
	_ = crewBudgetSetCmd.MarkFlagRequired("limit")
	crewBudgetClearCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	crewBudgetCmd.AddCommand(crewBudgetGetCmd, crewBudgetSetCmd, crewBudgetClearCmd)
	crewCmd.AddCommand(crewBudgetCmd)
}
