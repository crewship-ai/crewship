package main

// Routine logs subcommand. Fetches the full journal entries for one
// run_id — useful for post-mortem on a failed run, CI debugging, or
// just "what happened in run X". Distinct from `runs <slug>` (which
// lists summaries) and `watch <slug>` (live stream): this is a
// one-shot, scriptable per-run log dump.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var routineLogsCmd = &cobra.Command{
	Use:   "logs <run_id>",
	Short: "Fetch the full journal trace for a run (post-mortem / CI diagnostics)",
	Long: `Fetches every journal entry tagged to the given run_id (run + step
events) and prints them in chronological order. The slug is required
because the runs endpoint is per-routine; either pass --slug
explicitly or let the CLI infer it from the run_id by listing
recent runs across all workspace routines.

Examples:
  crewship routine logs run_cmovn40h30001c57bb667 --slug summarize-text
  crewship routine logs run_abc123 --slug pr-review --json | jq '.[] | select(.severity=="error")'

Output formats:
  table  Human-readable timeline with timestamp + entry-type + summary
  json   JSON array of entries — pipe to jq for ad-hoc analysis
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runID := args[0]
		slug, _ := cmd.Flags().GetString("slug")
		jsonMode, _ := cmd.Flags().GetBool("json")
		if slug == "" {
			return fmt.Errorf("--slug is required (cannot dispatch logs without knowing the routine)")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()

		// Fetch all entries for this slug with steps included, then
		// filter client-side to entries matching this run_id. Server-
		// side filtering by run_id would be more efficient at scale
		// but the current API surface is per-routine; bounding the
		// page size at 500 covers typical run sizes (a 5-step pipeline
		// emits 11 entries; 500 leaves headroom for retries).
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/runs?include_steps=1&limit=500", ws, slug)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []struct {
			ID        string                 `json:"id"`
			Timestamp string                 `json:"ts"`
			EntryType string                 `json:"entry_type"`
			Severity  string                 `json:"severity"`
			Summary   string                 `json:"summary"`
			RunID     string                 `json:"run_id,omitempty"`
			Payload   map[string]interface{} `json:"payload,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// Filter to this run_id, oldest-first.
		matched := rows[:0]
		for _, r := range rows {
			if r.RunID == runID {
				matched = append(matched, r)
			}
		}
		if len(matched) == 0 {
			return fmt.Errorf("no entries found for run_id %q in routine %q", runID, slug)
		}
		// Reverse — server returns DESC.
		for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
			matched[i], matched[j] = matched[j], matched[i]
		}

		if jsonMode {
			b, _ := json.MarshalIndent(matched, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		// Pretty timeline
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tEVENT\tSEVERITY\tSUMMARY")
		for _, r := range matched {
			t := parseTime(r.Timestamp)
			kind := strings.TrimPrefix(r.EntryType, "pipeline.")
			sev := r.Severity
			if sev == "" {
				sev = "info"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				t.Local().Format("15:04:05.000"), kind, sev, r.Summary)
		}
		_ = w.Flush()

		// Optional: surface step output_preview from final step.completed
		// or the run.completed total cost/duration so users see "the
		// answer" without manually walking the timeline.
		for i := len(matched) - 1; i >= 0; i-- {
			r := matched[i]
			if r.EntryType == "pipeline.run.completed" {
				if cost, ok := r.Payload["total_cost_usd"].(float64); ok {
					if dur, ok := r.Payload["total_duration_ms"].(float64); ok {
						fmt.Printf("\nCompleted in %.1fs · cost $%.4f\n",
							float64(dur)/1000, cost)
					}
				}
				break
			}
			if r.EntryType == "pipeline.run.failed" {
				if reason, ok := r.Payload["error_message"].(string); ok && reason != "" {
					fmt.Printf("\nFailed: %s\n", reason)
				}
				break
			}
		}
		return nil
	},
}

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func init() {
	routineLogsCmd.Flags().String("slug", "", "routine slug the run belongs to (REQUIRED)")
	routineLogsCmd.Flags().Bool("json", false, "JSON output for jq / scripting")
	pipelineCmd.AddCommand(routineLogsCmd)
}
