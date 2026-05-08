package main

// Routine records subcommand. Hits the v83 pipeline_runs-backed
// endpoint directly (column-typed, B-tree scan via
// idx_pipeline_runs_pipeline_started). Distinct from `routine runs`
// which scans journal_entries with json_extract — slower for large
// run counts but includes step-level events.
//
// Use `records` for the run-list view (status / cost / duration
// dashboard) and `runs --include-steps` (or `routine logs <run_id>`)
// when you need the per-step timeline.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type runRecordRow struct {
	ID               string  `json:"id"`
	PipelineID       string  `json:"pipeline_id"`
	PipelineSlug     string  `json:"pipeline_slug"`
	Status           string  `json:"status"`
	Mode             string  `json:"mode"`
	StartedAt        string  `json:"started_at"`
	EndedAt          string  `json:"ended_at,omitempty"`
	CurrentStepID    string  `json:"current_step_id,omitempty"`
	Output           string  `json:"output,omitempty"`
	CostUSD          float64 `json:"cost_usd"`
	DurationMs       int64   `json:"duration_ms"`
	ErrorMessage     string  `json:"error_message,omitempty"`
	FailedAtStep     string  `json:"failed_at_step,omitempty"`
	ErrorFingerprint string  `json:"error_fingerprint,omitempty"`
	TriggeredVia     string  `json:"triggered_via"`
	TriggeredByID    string  `json:"triggered_by_id,omitempty"`
	IdempotencyKey   string  `json:"idempotency_key,omitempty"`
}

var routineRecordsCmd = &cobra.Command{
	Use:   "records <slug>",
	Short: "List runs for a routine using the pipeline_runs projection (v83)",
	Long: `Hits the column-typed pipeline_runs endpoint directly — faster than
'routine runs' for large run counts because it skips the LIKE-scan
over journal_entries. No step-level events though; pair with
'routine logs <run_id>' or 'routine runs --include-steps' for the
per-step timeline.

Returns 503 + falls back to 'routine runs' when the server doesn't
have the v83 runStore wired (legacy deployment).

Examples:
  crewship routine records summarize-text
  crewship routine records summarize-text --status running
  crewship routine records summarize-text --status failed --limit 100
  crewship routine records summarize-text --json | jq '.[] | select(.cost_usd > 0.01)'
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		statusFilter, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")
		jsonMode, _ := cmd.Flags().GetBool("json")
		if limit <= 0 || limit > 500 {
			limit = 50
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run-records?limit=%d", ws, slug, limit)
		if statusFilter != "" {
			path += "&status=" + statusFilter
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// 503 = server doesn't have runStore wired (pre-v83 deployment).
		// Surface the legacy fallback hint as a non-zero error so CI
		// doesn't silently treat empty list as "no runs".
		if resp.StatusCode == http.StatusServiceUnavailable {
			return fmt.Errorf("run-records endpoint unavailable (server predates migration v83); fall back to 'crewship routine runs %s'", slug)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []runRecordRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if jsonMode {
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		if len(rows) == 0 {
			if statusFilter != "" {
				fmt.Printf("No %s runs for routine %q.\n", statusFilter, slug)
			} else {
				fmt.Printf("No runs yet for routine %q.\n", slug)
				fmt.Printf("Trigger one with: crewship routine run %s\n", slug)
			}
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "RUN ID\tSTATUS\tMODE\tTRIGGER\tDURATION\tCOST\tSTARTED")
		for _, r := range rows {
			dur := "—"
			if r.DurationMs > 0 {
				dur = formatDurMs(r.DurationMs)
			}
			cost := "—"
			if r.CostUSD > 0 {
				cost = fmt.Sprintf("$%.4f", r.CostUSD)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				truncIDForCLI(r.ID, 16), r.Status, r.Mode, r.TriggeredVia, dur, cost, formatTimestamp(r.StartedAt))
		}
		return w.Flush()
	},
}

// formatDurMs renders a millisecond count as a human duration.
// Mirrors what the orchestration UI shows for run cards: keep small
// values precise; switch to seconds/minutes for larger ones so the
// column doesn't widen unpredictably.
func formatDurMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	minutes := ms / 60_000
	seconds := (ms % 60_000) / 1000
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}

func init() {
	routineRecordsCmd.Flags().String("status", "", "filter by status: queued | running | completed | failed | cancelled | interrupted | dry_run")
	routineRecordsCmd.Flags().Int("limit", 50, "max number of records to return (1-500)")
	routineRecordsCmd.Flags().Bool("json", false, "output as JSON for scripting")
	pipelineCmd.AddCommand(routineRecordsCmd)
}
