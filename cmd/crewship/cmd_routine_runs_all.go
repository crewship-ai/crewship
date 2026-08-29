package main

// Workspace-wide routine run list — GET /api/v1/workspaces/{ws}/pipeline-runs.
//
// Every other run sub-resource (logs, tree, metadata, signal) already has a
// CLI command; this is the run LIST itself, unscoped to any one routine.
// Distinct from 'routine active' (in-flight only, no filters) and
// 'routine runs <slug>' / 'routine records <slug>' (one routine's journal /
// pipeline_runs history) — this is the cross-pipeline feed behind the
// Activity dock's Runs sub-tab (PipelineHandler.ListWorkspaceRuns,
// internal/api/pipeline_runs.go), filterable server-side by status/since,
// newest first.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// workspaceRunRow is one row of `routine runs-all`. Field set mirrors
// PipelineHandler.ListWorkspaceRuns's response map exactly.
type workspaceRunRow struct {
	ID              string  `json:"id" yaml:"id"`
	PipelineID      string  `json:"pipeline_id" yaml:"pipeline_id"`
	PipelineSlug    string  `json:"pipeline_slug" yaml:"pipeline_slug"`
	PipelineName    string  `json:"pipeline_name" yaml:"pipeline_name"`
	Status          string  `json:"status" yaml:"status"`
	Mode            string  `json:"mode" yaml:"mode"`
	StartedAt       string  `json:"started_at" yaml:"started_at"`
	EndedAt         string  `json:"ended_at" yaml:"ended_at"`
	CurrentStepID   string  `json:"current_step_id" yaml:"current_step_id"`
	CostUSD         float64 `json:"cost_usd" yaml:"cost_usd"`
	DurationMs      int64   `json:"duration_ms" yaml:"duration_ms"`
	TriggeredVia    string  `json:"triggered_via" yaml:"triggered_via"`
	TriggeredByID   string  `json:"triggered_by_id" yaml:"triggered_by_id"`
	InvokingCrewID  string  `json:"invoking_crew_id" yaml:"invoking_crew_id"`
	InvokingAgentID string  `json:"invoking_agent_id" yaml:"invoking_agent_id"`
	InvokingUserID  string  `json:"invoking_user_id" yaml:"invoking_user_id"`
	ErrorMessage    string  `json:"error_message" yaml:"error_message"`
	FailedAtStep    string  `json:"failed_at_step" yaml:"failed_at_step"`
	IssueIdentifier string  `json:"issue_identifier" yaml:"issue_identifier"`
}

var routineRunsAllCmd = &cobra.Command{
	Use:   "runs-all",
	Short: "List runs across every routine in the workspace",
	Long: `Workspace-wide run feed — every routine, not just one slug. Backs
the Activity dock's Runs sub-tab.

Distinct from 'routine runs <slug>' / 'routine records <slug>' (one
routine's history) and 'routine active' (in-flight only, no filters):
this lists runs of any status across the whole workspace, filterable
server-side by --status/--since, newest first (started_at DESC).

Examples:
  crewship routine runs-all
  crewship routine runs-all --status failed --limit 100
  crewship routine runs-all --since 2026-08-01T00:00:00Z
  crewship routine runs-all --status active   # running+queued+paused+waiting
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		status, _ := cmd.Flags().GetString("status")
		since, _ := cmd.Flags().GetString("since")
		limit, _ := cmd.Flags().GetInt("limit")
		limitStr := ""
		if limit > 0 {
			limitStr = strconv.Itoa(limit)
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs%s", ws,
			queryString("status", status, "since", since, "limit", limitStr))
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Rows  []workspaceRunRow `json:"rows"`
			Count int               `json:"count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if body.Rows == nil {
			body.Rows = []workspaceRunRow{}
		}
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(body.Rows, func() {
			if len(body.Rows) == 0 {
				fmt.Println("No runs.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RUN_ID\tSLUG\tSTATUS\tMODE\tSTARTED\tDURATION\tCOST\tTRIGGER")
			for _, r := range body.Rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					truncIDForCLI(r.ID, 24), r.PipelineSlug, r.Status, r.Mode, r.StartedAt,
					formatRunDuration(r.DurationMs), formatRunCost(r.CostUSD), r.TriggeredVia)
			}
			flush.of(w)
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// formatRunDuration renders a run's duration_ms for the runs-all table.
// Kept separate from formatPayloadDuration (cmd_routine_extra.go), which
// parses a journal step-completed payload map — a different input shape
// for a similar-looking number.
func formatRunDuration(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// formatRunCost renders a run's cost_usd for the runs-all table. See
// formatRunDuration for why this doesn't reuse formatPayloadCost.
func formatRunCost(usd float64) string {
	if usd <= 0 {
		return "—"
	}
	return fmt.Sprintf("$%.4f", usd)
}

func init() {
	routineRunsAllCmd.Flags().String("status", "", "filter by status: queued|running|completed|failed|cancelled|interrupted|dry_run|waiting|paused, or 'active' (running+queued+paused+waiting)")
	routineRunsAllCmd.Flags().String("since", "", "RFC3339 lower bound on created_at")
	routineRunsAllCmd.Flags().Int("limit", 50, "max rows to return (server cap 200)")
	pipelineCmd.AddCommand(routineRunsAllCmd)
}
