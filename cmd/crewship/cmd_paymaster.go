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
// crewSpendRow / agentSpendRow match the backend json tags exactly —
// named so the JSON decode structs, the printSpendTable type switch,
// and any future test fixture can't drift out of sync via copy-paste.
// CodeRabbit round 7 nitpick: prior code had the same anonymous struct
// declared three times and a single tag typo would have silently
// produced $0.0000 rows via the "unsupported rows type" default case.
type crewSpendRow struct {
	CrewID    string  `json:"crew_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int64   `json:"call_count"`
	InTokens  int64   `json:"input_tokens"`
	OutTokens int64   `json:"output_tokens"`
}

type agentSpendRow struct {
	AgentID   string  `json:"agent_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int64   `json:"call_count"`
	InTokens  int64   `json:"input_tokens"`
	OutTokens int64   `json:"output_tokens"`
}

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
			Rows []crewSpendRow `json:"rows"`
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
			Rows []agentSpendRow `json:"rows"`
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
		// Typed struct — a prior version used map[string]any + unchecked
		// assertions which silently rendered zeros whenever the backend
		// shape drifted. Matches the TopSpender json tags exactly so
		// missing / renamed fields surface as decode errors instead of
		// misleading $0.0000 rows.
		var body struct {
			Rows []struct {
				ScopeKind string  `json:"scope_kind"`
				ScopeID   string  `json:"scope_id"`
				CostUSD   float64 `json:"cost_usd"`
				CallCount int64   `json:"call_count"`
			} `json:"rows"`
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
			scope := fmt.Sprintf("%s/%s", row.ScopeKind, row.ScopeID)
			fmt.Printf("%2d. %s%-40s%s  %s$%8.4f%s  %d calls\n",
				i+1, cli.Bold, truncateString(scope, 40), cli.Reset,
				cli.Yellow, row.CostUSD, cli.Reset, row.CallCount)
		}
		return nil
	},
}

// printSpendTable renders any rollup slice whose elements have CostUSD,
// CallCount, TotalTokens, plus an identifying ID/Name pair. Uses
// reflection-free casting via any → the small duplication below is
// cheaper than a generic interface. Unsupported row types fall through
// to an error so call sites see a loud failure instead of silent
// empty output.
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
	case []crewSpendRow:
		for _, r := range typed {
			fmt.Printf("%-30s  %s$%8.4f%s  %6d  %12d\n",
				truncateString(r.CrewID, 30), cli.Yellow, r.CostUSD, cli.Reset, r.CallCount, r.InTokens+r.OutTokens)
		}
	case []agentSpendRow:
		for _, r := range typed {
			fmt.Printf("%-30s  %s$%8.4f%s  %6d  %12d\n",
				truncateString(r.AgentID, 30), cli.Yellow, r.CostUSD, cli.Reset, r.CallCount, r.InTokens+r.OutTokens)
		}
	default:
		return fmt.Errorf("printSpendTable: unsupported rows type %T", rows)
	}
	return nil
}

// paymasterByMissionCmd hits the per-mission rollup. The server-side
// route (router_orchestration.go:180) enforces workspace isolation —
// missions from other tenants return 404 with the same shape as
// "not found" so callers can't enumerate IDs. Output renders both the
// row JSON (cost, calls, tokens) and the mission ID echo for scripting.
//
// Why no --range: SpendByMission has no time-window parameter on the
// server (it returns the mission's full cost history). Adding a CLI
// --range would silently do nothing.
var paymasterByMissionCmd = &cobra.Command{
	Use:   "by-mission <missionId>",
	Short: "Spend rolled up for a single mission",
	Long: `Return the total cost ledger for a single mission (cost, call count,
input/output tokens). Mission is workspace-scoped; foreign mission IDs
return 404.

Example:
  crewship paymaster by-mission mis_abc123
  crewship paymaster by-mission mis_abc123 --format json | jq .row.cost_usd`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/paymaster/spend/by-mission/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Row struct {
				MissionID string  `json:"mission_id"`
				CostUSD   float64 `json:"cost_usd"`
				CallCount int64   `json:"call_count"`
				InTokens  int64   `json:"input_tokens"`
				OutTokens int64   `json:"output_tokens"`
			} `json:"row"`
			MissionID string `json:"mission_id"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body)
		}
		if f.Format == "yaml" {
			return f.YAML(body)
		}
		fmt.Printf("%sMission %s%s\n", cli.Bold, body.MissionID, cli.Reset)
		fmt.Printf("  Cost:         %s$%.4f%s\n", cli.Yellow, body.Row.CostUSD, cli.Reset)
		fmt.Printf("  Calls:        %d\n", body.Row.CallCount)
		fmt.Printf("  In tokens:    %d\n", body.Row.InTokens)
		fmt.Printf("  Out tokens:   %d\n", body.Row.OutTokens)
		fmt.Printf("  Total tokens: %d\n", body.Row.InTokens+body.Row.OutTokens)
		return nil
	},
}

// paymasterSubscriptionsCmd hits /paymaster/subscriptions — the
// flat-rate plan rollup. Cost is always $0 by construction (subscription
// credentials have no per-call ledger), so the table renders call
// counts + tokens + last-used instead.
var paymasterSubscriptionsCmd = &cobra.Command{
	Use:   "subscriptions",
	Short: "Subscription-plan usage rollup (flat-rate credentials)",
	Long: `Show flat-rate subscription usage — the API counterpart to the
"Subscription plans" panel on the Paymaster dashboard. No $-figures
because flat-rate cost is always $0 by construction; the row shape is
plan + provider + call_count + token totals + last_used.

Examples:
  crewship paymaster subscriptions
  crewship paymaster subscriptions --range 30d
  crewship paymaster subscriptions --format json | jq '.rows[].calls'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		q := url.Values{}
		if v, _ := cmd.Flags().GetString("range"); v != "" {
			q.Set("range", v)
		}
		if v, _ := cmd.Flags().GetString("since"); v != "" {
			q.Set("since", v)
		}
		if v, _ := cmd.Flags().GetString("until"); v != "" {
			q.Set("until", v)
		}
		path := "/api/v1/paymaster/subscriptions"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
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
				Plan       string `json:"plan"`
				Provider   string `json:"provider"`
				CallCount  int64  `json:"call_count"`
				InTokens   int64  `json:"input_tokens"`
				OutTokens  int64  `json:"output_tokens"`
				LastUsedAt string `json:"last_used_at"`
			} `json:"rows"`
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
		fmt.Printf("%s%-20s  %-12s  %6s  %12s  %s%s\n",
			cli.Bold, "Plan", "Provider", "Calls", "Tokens", "Last used", cli.Reset)
		fmt.Println(strings.Repeat("─", 80))
		for _, r := range body.Rows {
			fmt.Printf("%-20s  %-12s  %6d  %12d  %s\n",
				truncateString(r.Plan, 20),
				truncateString(r.Provider, 12),
				r.CallCount,
				r.InTokens+r.OutTokens,
				r.LastUsedAt)
		}
		if len(body.Rows) == 0 {
			fmt.Printf("\n%s(no subscription credentials configured in this workspace)%s\n", cli.Dim, cli.Reset)
		}
		return nil
	},
}

func init() {
	paymasterByCrewCmd.Flags().String("range", "7d", "Time window (1h, 24h, 7d, 30d)")
	paymasterByAgentCmd.Flags().String("range", "7d", "Time window (1h, 24h, 7d, 30d)")
	paymasterTopCmd.Flags().Int("limit", 10, "Top N spenders")
	paymasterTopCmd.Flags().String("range", "7d", "Time window")

	paymasterSubscriptionsCmd.Flags().String("range", "7d", "Time window (1h, 24h, 7d, 30d)")
	paymasterSubscriptionsCmd.Flags().String("since", "", "Lower bound (RFC3339)")
	paymasterSubscriptionsCmd.Flags().String("until", "", "Upper bound (RFC3339)")

	paymasterCmd.AddCommand(paymasterByCrewCmd)
	paymasterCmd.AddCommand(paymasterByAgentCmd)
	paymasterCmd.AddCommand(paymasterByMissionCmd)
	paymasterCmd.AddCommand(paymasterTopCmd)
	paymasterCmd.AddCommand(paymasterSubscriptionsCmd)
}
