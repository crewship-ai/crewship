package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// costCmd is a high-density single-screen cost summary for the impatient.
//
// `crewship paymaster ...` exposes the underlying rollups in full fidelity;
// this is the command you reach for when you just want to know "how much
// am I spending and on what?" without remembering which subcommand to run.
// Composes top-spenders + by-crew + subscription-plans into one view.
var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Quick spend summary across the workspace",
	Long: `Show total spend, top spenders, and per-crew rollup in a single screen.

Examples:
  crewship cost                # last 24h
  crewship cost --range 7d
  crewship cost --range 30d --limit 10
  crewship cost --format json  # for scripts`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		rng, _ := cmd.Flags().GetString("range")
		limit, _ := cmd.Flags().GetInt("limit")

		topRows, err := fetchTopSpenders(client, rng, limit)
		if err != nil {
			return err
		}
		crewRows, err := fetchCrewSpend(client, rng)
		if err != nil {
			return err
		}
		subRows, _ := fetchSubscriptionUsage(client) // best-effort; not every workspace has plans

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(map[string]any{
				"range":         rng,
				"top":           topRows,
				"crews":         crewRows,
				"subscriptions": subRows,
			})
		}
		if f.Format == "yaml" {
			return f.YAML(map[string]any{
				"range":         rng,
				"top":           topRows,
				"crews":         crewRows,
				"subscriptions": subRows,
			})
		}
		printCostHeader(rng, crewRows)
		printCostTopSpenders(topRows)
		printCostByCrew(crewRows)
		printCostSubscriptions(subRows)
		return nil
	},
}

type topSpenderRow struct {
	ScopeKind string  `json:"scope_kind"`
	ScopeID   string  `json:"scope_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int64   `json:"call_count"`
}

type subUsageRow struct {
	Plan       string  `json:"subscription_plan"`
	Provider   string  `json:"provider"`
	CallCount  int64   `json:"call_count"`
	InTokens   int64   `json:"input_tokens"`
	OutTokens  int64   `json:"output_tokens"`
	LastUsedAt *string `json:"last_used_at"`
}

func fetchTopSpenders(c *cli.Client, rng string, limit int) ([]topSpenderRow, error) {
	q := url.Values{}
	if rng != "" {
		q.Set("range", rng)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	resp, err := c.Get("/api/v1/paymaster/top-spenders?" + q.Encode())
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var body struct {
		Rows []topSpenderRow `json:"rows"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return nil, err
	}
	return body.Rows, nil
}

func fetchCrewSpend(c *cli.Client, rng string) ([]crewSpendRow, error) {
	path := "/api/v1/paymaster/spend/by-crew"
	if rng != "" {
		path += "?range=" + url.QueryEscape(rng)
	}
	resp, err := c.Get(path)
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var body struct {
		Rows []crewSpendRow `json:"rows"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return nil, err
	}
	return body.Rows, nil
}

func fetchSubscriptionUsage(c *cli.Client) ([]subUsageRow, error) {
	resp, err := c.Get("/api/v1/paymaster/subscriptions")
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var body struct {
		Rows []subUsageRow `json:"rows"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return nil, err
	}
	return body.Rows, nil
}

func printCostHeader(rng string, crews []crewSpendRow) {
	if rng == "" {
		rng = "24h"
	}
	var total float64
	var calls int64
	for _, c := range crews {
		total += c.CostUSD
		calls += c.CallCount
	}
	fmt.Printf("%s%s%s  range=%s  total=%s$%.4f%s  calls=%d  crews=%d\n",
		cli.Bold, "Cost summary", cli.Reset,
		rng, cli.Yellow, total, cli.Reset, calls, len(crews))
	fmt.Println(strings.Repeat("─", 64))
}

func printCostTopSpenders(rows []topSpenderRow) {
	if len(rows) == 0 {
		return
	}
	fmt.Printf("\n%sTop spenders%s\n", cli.Bold, cli.Reset)
	for i, r := range rows {
		scope := fmt.Sprintf("%s/%s", r.ScopeKind, r.ScopeID)
		fmt.Printf("  %2d. %-44s  %s$%8.4f%s  %d calls\n",
			i+1, truncateString(scope, 44),
			cli.Yellow, r.CostUSD, cli.Reset, r.CallCount)
	}
}

func printCostByCrew(rows []crewSpendRow) {
	if len(rows) == 0 {
		return
	}
	// Sort descending by cost so the highest-spend crew is first regardless of
	// what order the backend returned. Without this the printed order has no
	// stable meaning when multiple crews tie or the server reorders for cache
	// reasons.
	sort.Slice(rows, func(i, j int) bool { return rows[i].CostUSD > rows[j].CostUSD })

	fmt.Printf("\n%sBy crew%s\n", cli.Bold, cli.Reset)
	for _, r := range rows {
		fmt.Printf("  %-30s  %s$%8.4f%s  %5d calls  %d tokens\n",
			truncateString(r.CrewID, 30), cli.Yellow, r.CostUSD, cli.Reset,
			r.CallCount, r.InTokens+r.OutTokens)
	}
}

func printCostSubscriptions(rows []subUsageRow) {
	if len(rows) == 0 {
		return
	}
	fmt.Printf("\n%sSubscription plans%s %s(no per-call $)%s\n",
		cli.Bold, cli.Reset, cli.Dim, cli.Reset)
	for _, r := range rows {
		last := ""
		if r.LastUsedAt != nil {
			last = " last=" + *r.LastUsedAt
		}
		fmt.Printf("  %s/%s  %d calls  %d tokens%s\n",
			r.Plan, r.Provider, r.CallCount, r.InTokens+r.OutTokens, last)
	}
}

// truncateString already exists elsewhere in cmd_paymaster — we reuse it.

func init() {
	costCmd.Flags().String("range", "24h", "Time window (1h, 24h, 7d, 30d)")
	costCmd.Flags().Int("limit", 5, "Number of top spenders to show")
	addWatchFlag(costCmd)
	costCmd.RunE = watchWrap(costCmd.RunE)
}
