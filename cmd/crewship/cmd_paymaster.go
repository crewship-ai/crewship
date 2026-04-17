package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// paymasterCmd surfaces cost reports from the command line so operators
// can spot runaway missions without opening the web UI. Narrow scope —
// three subcommands that each map to one rollup endpoint.
var paymasterCmd = &cobra.Command{
	Use:   "paymaster",
	Short: "Cost and budget reports across the workspace",
	Long: `View LLM spend rolled up by crew, agent, or mission. Reads from the
cost_ledger — the canonical per-call billing record populated by the
LLM middleware. Windows default to the last 7 days; pass --range to
change.

Examples:
  crewship paymaster by-crew
  crewship paymaster by-crew --range 24h
  crewship paymaster by-agent backend-team
  crewship paymaster top --limit 5 --range 30d`,
}

var paymasterByCrewCmd = &cobra.Command{
	Use:   "by-crew",
	Short: "Spend rolled up per crew",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		rangeFlag, _ := cmd.Flags().GetString("range")
		path := "/api/v1/paymaster/spend/by-crew"
		if rangeFlag != "" {
			path += "?range=" + url.QueryEscape(rangeFlag)
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
				CrewID      string  `json:"crew_id"`
				CrewName    string  `json:"crew_name"`
				CostUSD     float64 `json:"cost_usd"`
				CallCount   int64   `json:"call_count"`
				TotalTokens int64   `json:"total_tokens"`
			} `json:"rows"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		return printSpendTable("Crew", body.Rows)
	},
}

var paymasterByAgentCmd = &cobra.Command{
	Use:   "by-agent [crew]",
	Short: "Spend rolled up per agent within a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
		rangeFlag, _ := cmd.Flags().GetString("range")
		path := "/api/v1/paymaster/spend/by-agent/" + url.PathEscape(crewID)
		if rangeFlag != "" {
			path += "?range=" + url.QueryEscape(rangeFlag)
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
				AgentID     string  `json:"agent_id"`
				AgentName   string  `json:"agent_name"`
				CostUSD     float64 `json:"cost_usd"`
				CallCount   int64   `json:"call_count"`
				TotalTokens int64   `json:"total_tokens"`
			} `json:"rows"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		return printSpendTable("Agent", body.Rows)
	},
}

var paymasterTopCmd = &cobra.Command{
	Use:   "top",
	Short: "Highest-cost scopes in the window",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		q := url.Values{}
		if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
			q.Set("limit", fmt.Sprintf("%d", v))
		}
		if v, _ := cmd.Flags().GetString("range"); v != "" {
			q.Set("range", v)
		}
		path := "/api/v1/paymaster/top-spenders?" + q.Encode()
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Rows []map[string]any `json:"rows"`
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
		for i, row := range body.Rows {
			scope := fmt.Sprintf("%v/%v", row["scope_kind"], row["scope_id"])
			cost, _ := row["cost_usd"].(float64)
			calls, _ := row["call_count"].(float64)
			fmt.Printf("%2d. %s%-40s%s  %s$%8.4f%s  %d calls\n",
				i+1, cli.Bold, truncateString(scope, 40), cli.Reset,
				cli.Yellow, cost, cli.Reset, int(calls))
		}
		return nil
	},
}

// printSpendTable renders any rollup slice whose elements have CostUSD,
// CallCount, TotalTokens, plus an identifying ID/Name pair. Uses
// reflection-free casting via any → the small duplication below is
// cheaper than a generic interface.
func printSpendTable(scopeLabel string, rows any) error {
	f := newFormatter()
	if f.Format == "json" {
		return f.JSON(rows)
	}
	if f.Format == "yaml" {
		return f.YAML(rows)
	}
	fmt.Printf("%s%-30s  %10s  %6s  %12s%s\n",
		cli.Bold, scopeLabel, "Cost (USD)", "Calls", "Tokens", cli.Reset)
	fmt.Println(strings.Repeat("─", 64))
	switch typed := rows.(type) {
	case []struct {
		CrewID      string  `json:"crew_id"`
		CrewName    string  `json:"crew_name"`
		CostUSD     float64 `json:"cost_usd"`
		CallCount   int64   `json:"call_count"`
		TotalTokens int64   `json:"total_tokens"`
	}:
		for _, r := range typed {
			name := r.CrewName
			if name == "" {
				name = r.CrewID
			}
			fmt.Printf("%-30s  %s$%8.4f%s  %6d  %12d\n",
				truncateString(name, 30), cli.Yellow, r.CostUSD, cli.Reset, r.CallCount, r.TotalTokens)
		}
	case []struct {
		AgentID     string  `json:"agent_id"`
		AgentName   string  `json:"agent_name"`
		CostUSD     float64 `json:"cost_usd"`
		CallCount   int64   `json:"call_count"`
		TotalTokens int64   `json:"total_tokens"`
	}:
		for _, r := range typed {
			name := r.AgentName
			if name == "" {
				name = r.AgentID
			}
			fmt.Printf("%-30s  %s$%8.4f%s  %6d  %12d\n",
				truncateString(name, 30), cli.Yellow, r.CostUSD, cli.Reset, r.CallCount, r.TotalTokens)
		}
	}
	return nil
}

func init() {
	paymasterByCrewCmd.Flags().String("range", "7d", "Time window (1h, 24h, 7d, 30d)")
	paymasterByAgentCmd.Flags().String("range", "7d", "Time window (1h, 24h, 7d, 30d)")
	paymasterTopCmd.Flags().Int("limit", 10, "Top N spenders")
	paymasterTopCmd.Flags().String("range", "7d", "Time window")

	paymasterCmd.AddCommand(paymasterByCrewCmd)
	paymasterCmd.AddCommand(paymasterByAgentCmd)
	paymasterCmd.AddCommand(paymasterTopCmd)
}
