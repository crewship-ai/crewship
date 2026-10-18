package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// `crewship routine health` mirrors GET /workspaces/{ws}/pipeline-health
// (ODCH-RT-07): the Routines Health table from the terminal.

type routineHealthRowJSON struct {
	Slug         string   `json:"slug" yaml:"slug"`
	LastOutcomes []string `json:"last_outcomes" yaml:"last_outcomes"`
	Finished     int      `json:"finished" yaml:"finished"`
	Succeeded    int      `json:"succeeded" yaml:"succeeded"`
	SuccessPct   *int     `json:"success_pct,omitempty" yaml:"success_pct,omitempty"`
	Runs         int      `json:"runs" yaml:"runs"`
	SpendUSD     float64  `json:"spend_usd" yaml:"spend_usd"`
}

type routineHealthJSON struct {
	WindowDays int                    `json:"window_days" yaml:"window_days"`
	Routines   []routineHealthRowJSON `json:"routines" yaml:"routines"`
}

// outcomeGlyph is one character per run, oldest first, for the human table.
var outcomeGlyph = map[string]string{"completed": "+", "failed": "x", "stopped": "-", "waiting": "?", "running": "~"}

var routineHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Per-routine health: last 20 outcomes, success rate, runs and spend in a window",
	Long: `Show, for every routine that has run, its last 20 outcomes (oldest first),
the success rate over the finished ones, and the runs and spend of the last
--days days (default 30). Test and dry runs are not counted.

Outcome key: + completed, x failed, - stopped, ? waiting for a person, ~ running.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		days, _ := cmd.Flags().GetInt("days")
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipeline-health", client.GetWorkspaceID())
		if cmd.Flags().Changed("days") {
			path += fmt.Sprintf("?days=%d", days)
		}
		var out routineHealthJSON
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if len(out.Routines) == 0 {
				fmt.Println("No routine has run yet.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "ROUTINE\tLAST RUNS\tSUCCESS\tRUNS %dD\tSPEND %dD\n", out.WindowDays, out.WindowDays)
			for _, r := range out.Routines {
				var bars strings.Builder
				for _, o := range r.LastOutcomes {
					bars.WriteString(outcomeGlyph[o])
				}
				success := "-"
				if r.SuccessPct != nil {
					success = fmt.Sprintf("%d%% (%d of %d)", *r.SuccessPct, r.Succeeded, r.Finished)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t$%.2f\n", r.Slug, bars.String(), success, r.Runs, r.SpendUSD)
			}
			_ = w.Flush()
		})
	},
}

func init() {
	routineHealthCmd.Flags().Int("days", 30, "window for runs and spend (1-366)")
	pipelineCmd.AddCommand(routineHealthCmd)
}
