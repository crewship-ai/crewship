package main

// crewship spend — CLI counterpart to GET /api/v1/journal/spend
// (#1404). Deliberately a separate, journal-native rollup from
// `crewship cost`/`crewship paymaster ...` (which hit
// /api/v1/paymaster/*) — see docs/guides/crew-journal.mdx's Spend
// section for why two cost surfaces coexist.

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type spendByAgentRow struct {
	Date      string  `json:"date"`
	CrewID    string  `json:"crew_id"`
	AgentID   string  `json:"agent_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int     `json:"call_count"`
}

type spendByRoutineRow struct {
	Date         string  `json:"date"`
	PipelineID   string  `json:"pipeline_id"`
	PipelineSlug string  `json:"pipeline_slug"`
	CostUSD      float64 `json:"cost_usd"`
	RunCount     int     `json:"run_count"`
}

type spendTopRow struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	CostUSD float64 `json:"cost_usd"`
}

type spendResponse struct {
	Window       string              `json:"window"`
	TotalCostUSD float64             `json:"total_cost_usd"`
	ByAgent      []spendByAgentRow   `json:"by_agent"`
	ByRoutine    []spendByRoutineRow `json:"by_routine"`
	TopRoutines  []spendTopRow       `json:"top_routines"`
	TopRuns      []spendTopRow       `json:"top_runs"`
	Truncated    bool                `json:"truncated"`
}

var spendCmd = &cobra.Command{
	Use:   "spend",
	Short: "Cost rollup — spend by agent, by routine, and top spenders",
	Long: `Journal-native cost rollup (#1404): total spend, day×crew×agent
breakdown (from cost.incurred journal entries), day×routine breakdown
(from pipeline_runs), and the top-N most expensive routines and runs
in the window.

Examples:
  crewship spend
  crewship spend --window 7d
  crewship spend --window 30d --top 10
  crewship spend --format json | jq '.top_routines'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		window, _ := cmd.Flags().GetString("window")
		switch window {
		case "24h", "7d", "30d":
		default:
			return fmt.Errorf("bad --window %q: must be one of 24h, 7d, 30d", window)
		}
		top, _ := cmd.Flags().GetInt("top")

		client := newAPIClient()
		body, err := fetchSpend(client, window, top)
		if err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(body, func() {
			printSpendHeader(body)
			printSpendTop("Top routines", body.TopRoutines)
			printSpendTop("Top runs", body.TopRuns)
			printSpendByAgent(body.ByAgent)
		})
	},
}

// fetchSpend hits GET /api/v1/journal/spend?window=&top= and decodes
// the response. Split out from RunE so tests can drive it directly
// against an httptest server, same convention as fetchTopSpenders /
// fetchCrewSpend in cmd_cost.go.
func fetchSpend(c *cli.Client, window string, top int) (spendResponse, error) {
	resp, err := c.Get(fmt.Sprintf("/api/v1/journal/spend?window=%s&top=%d", window, top))
	if err != nil {
		return spendResponse{}, err
	}
	if err := cli.CheckError(resp); err != nil {
		return spendResponse{}, err
	}
	var body spendResponse
	if err := cli.ReadJSON(resp, &body); err != nil {
		return spendResponse{}, err
	}
	return body, nil
}

// printSpendHeader labels the total with its BASIS, not just its value.
//
// The total and the per-routine tables below are computed from two different
// sources and deliberately measure two different things (#1786):
//
//   - total / By agent — SUM of `cost.incurred` journal entries, which
//     internal/paymaster/ledger.go emits only for BillingMetered calls with a
//     non-zero cost. Subscription (flat-rate) work is excluded on purpose:
//     "sub already covers them".
//   - Top routines / Top runs — SUM of pipeline_runs.cost_usd, the modelled
//     cost of every run regardless of billing mode.
//
// So on an instance whose agents run on a subscription token, a single routine
// can legitimately show more than the stated total. Unlabelled, that reads as
// arithmetic nonsense on a money screen; labelled, both numbers are useful.
func printSpendHeader(b spendResponse) {
	fmt.Printf("%s%s%s  window=%s  metered total=%s$%.4f%s",
		cli.Bold, "Spend rollup", cli.Reset, b.Window, cli.Yellow, b.TotalCostUSD, cli.Reset)
	if b.Truncated {
		fmt.Printf("  %s(truncated — window has more rows than the aggregation cap)%s", cli.Dim, cli.Reset)
	}
	fmt.Println()
	fmt.Printf("%sbilled (metered calls only) — subscription-covered work is excluded%s\n", cli.Dim, cli.Reset)
	fmt.Println(strings.Repeat("─", 64))
}

func printSpendTop(title string, rows []spendTopRow) {
	if len(rows) == 0 {
		return
	}
	fmt.Printf("\n%s%s%s %s(modelled cost of every run, including subscription-covered)%s\n",
		cli.Bold, title, cli.Reset, cli.Dim, cli.Reset)
	for i, r := range rows {
		fmt.Printf("  %2d. %-40s  %s$%8.4f%s\n",
			i+1, truncateString(r.Label, 40), cli.Yellow, r.CostUSD, cli.Reset)
	}
}

func printSpendByAgent(rows []spendByAgentRow) {
	if len(rows) == 0 {
		return
	}
	fmt.Printf("\n%sBy agent%s %s(billed, reconciles with the total above)%s\n",
		cli.Bold, cli.Reset, cli.Dim, cli.Reset)
	for _, r := range rows {
		agent := r.AgentID
		if agent == "" {
			agent = "(unattributed)"
		}
		fmt.Printf("  %-12s  %-24s  %s$%8.4f%s  %5d calls\n",
			r.Date, truncateString(agent, 24), cli.Yellow, r.CostUSD, cli.Reset, r.CallCount)
	}
}

func init() {
	spendCmd.Flags().String("window", "24h", "Aggregation window: 24h, 7d, or 30d")
	spendCmd.Flags().Int("top", 5, "Number of top routines/runs to show (1-50)")
	rootCmd.AddCommand(spendCmd)
}
