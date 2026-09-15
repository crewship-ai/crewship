package main

// Run-detail + schedule-board reads (#2460, CLI in #2577):
//
//   routine calendar              GET /api/v1/workspaces/{ws}/pipelines/calendar
//   routine executions <run_id>   GET /api/v1/workspaces/{ws}/pipeline-runs/{runId}/executions
//   routine artifacts <run_id>    GET /api/v1/workspaces/{ws}/pipeline-runs/{runId}/artifacts
//
// The three endpoints behind the routines workspace's Plan board, the run
// detail's step-execution list and its artifact list. Same family as
// 'routine logs/tree/metadata <run_id>' — a run sub-resource is a flat
// subcommand taking the run id, not a nested 'runs ...' group.
//
// Executions and artifacts answer in two shapes on one path: a page of rows
// (with `next_cursor` for `--after`), or — with --execution-id /
// --artifact-id — that one row's payload. --download on artifacts is the
// odd one out: raw bytes, not JSON, so it goes to a file (or stdout with
// --out -). The row types below mirror internal/api's
// pipeline_run_detail_types.go field for field; `-f json` is the wire body.

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// routineCalendarEvent is one row of `routine calendar`. `planned` rows come
// from schedules (schedule_id/timezone/pinned_version), `pending` rows from
// parked one-time or deferred starts, `run` rows from pipeline_runs
// (status/outcome). The inputs preview is display-only — the server scrubs
// it and it cannot replay a run.
type routineCalendarEvent struct {
	Inputs        *map[string]any `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	PinnedVersion *int            `json:"pinned_version,omitempty" yaml:"pinned_version,omitempty"`
	ID            string          `json:"id" yaml:"id"`
	Kind          string          `json:"kind" yaml:"kind"`
	At            string          `json:"at" yaml:"at"`
	Slug          string          `json:"slug" yaml:"slug"`
	Name          string          `json:"name" yaml:"name"`
	ScheduleID    string          `json:"schedule_id,omitempty" yaml:"schedule_id,omitempty"`
	Timezone      string          `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	Status        string          `json:"status,omitempty" yaml:"status,omitempty"`
	Outcome       string          `json:"outcome,omitempty" yaml:"outcome,omitempty"`
}

type routineCalendarResponse struct {
	Events    []routineCalendarEvent `json:"events" yaml:"events"`
	Truncated bool                   `json:"truncated" yaml:"truncated"`
}

var routineCalendarCmd = &cobra.Command{
	Use:   "calendar",
	Short: "Show the schedule board: planned occurrences, pending starts and past runs in a window",
	Long: `The routines workspace's Plan board as a list. One window, three kinds
of event: 'planned' occurrences computed from enabled schedules, 'pending'
one-time or deferred starts already parked, and 'run' rows that executed.
Past entries are real runs, never yesterday's cron extrapolated backwards.

The window is at most 32 days; the default is the coming week. A window
holding more than the server returns is reported as truncated — narrow it
rather than paging.

Examples:
  crewship routine calendar
  crewship routine calendar --from 2026-10-01T00:00:00Z --to 2026-10-08T00:00:00Z
  crewship routine calendar --to 2026-09-20T00:00:00Z -f json
`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		// Defaults are computed here, not on the server: both bounds are
		// required on the wire, and "the coming week" is a CLI convenience.
		now := time.Now().UTC()
		if from == "" {
			from = now.Format(time.RFC3339)
		}
		fromT, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return fmt.Errorf("--from must be RFC3339 (e.g. %s): %w", now.Format(time.RFC3339), err)
		}
		if to == "" {
			to = fromT.Add(7 * 24 * time.Hour).Format(time.RFC3339)
		}
		if _, err := time.Parse(time.RFC3339, to); err != nil {
			return fmt.Errorf("--to must be RFC3339 (e.g. %s): %w", now.Format(time.RFC3339), err)
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/calendar%s", ws,
			queryString("from", from, "to", to)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body routineCalendarResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if body.Events == nil {
			body.Events = []routineCalendarEvent{}
		}
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(body, func() {
			if len(body.Events) == 0 {
				fmt.Printf("No events between %s and %s.\n", from, to)
				return
			}
			// The wire order is by kind (planned, pending, run); a board
			// reads by time. JSON keeps the server's order.
			events := append([]routineCalendarEvent(nil), body.Events...)
			sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "AT\tKIND\tROUTINE\tRESULT\tVERSION\tINPUTS\tID")
			for _, e := range events {
				result := e.Status
				if e.Outcome != "" {
					result += "/" + e.Outcome
				}
				if result == "" {
					result = "—"
				}
				version := "—"
				if e.PinnedVersion != nil {
					version = fmt.Sprintf("v%d", *e.PinnedVersion)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					e.At, e.Kind, e.Slug, result, version, formatCalendarInputs(e.Inputs), truncIDForCLI(e.ID, 40))
			}
			flush.of(w)
			if body.Truncated {
				cli.PrintWarning("window truncated by the server — narrow --from/--to to see everything")
			}
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// formatCalendarInputs renders the scrubbed inputs preview as compact JSON
// for the table. "—" for a run row (no preview) and "{}" for an empty preset
// are different facts, so they render differently.
func formatCalendarInputs(inputs *map[string]any) string {
	if inputs == nil {
		return "—"
	}
	raw, err := json.Marshal(*inputs)
	if err != nil {
		return "?"
	}
	return truncIDForCLI(string(raw), 48)
}

// routineExecutionRow is one row of `routine executions`. output_bytes is a
// size, never the transcript — that is a second call with --execution-id.
type routineExecutionRow struct {
	ID                string `json:"id" yaml:"id"`
	ParentExecutionID string `json:"parent_execution_id" yaml:"parent_execution_id"`
	StepID            string `json:"step_id" yaml:"step_id"`
	ExecutionPath     string `json:"execution_path" yaml:"execution_path"`
	Attempt           int    `json:"attempt" yaml:"attempt"`
	Kind              string `json:"kind" yaml:"kind"`
	Status            string `json:"status" yaml:"status"`
	AgentSlug         string `json:"agent_slug" yaml:"agent_slug"`
	Model             string `json:"model" yaml:"model"`
	StartedAt         string `json:"started_at" yaml:"started_at"`
	EndedAt           string `json:"ended_at" yaml:"ended_at"`
	Error             string `json:"error" yaml:"error"`
	OutputBytes       int    `json:"output_bytes" yaml:"output_bytes"`
}

type routineExecutionPage struct {
	Rows       []routineExecutionRow `json:"rows" yaml:"rows"`
	NextCursor *string               `json:"next_cursor" yaml:"next_cursor"`
}

var routineExecutionsCmd = &cobra.Command{
	Use:   "executions <run_id>",
	Short: "List a run's step executions (every attempt, nested items included), or print one execution's output",
	Long: `Every step invocation a run made — retries, foreach items and nested
call_pipeline steps each get their own row — with status, agent, model and
the size of the output. Pages of 100; pass the printed cursor back as
--after for the next page.

An execution's output is deliberately not in the list (a transcript can be
megabytes). Fetch it with --execution-id, which prints the output alone.

Distinct from 'routine logs <run_id>' (the journal trace) and 'routine tree
<run_id>' (child RUNS, not steps).

Examples:
  crewship routine executions run_abc123
  crewship routine executions run_abc123 --after 100
  crewship routine executions run_abc123 --execution-id exec_9f...
  crewship routine executions run_abc123 -f json
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		after, _ := cmd.Flags().GetString("after")
		executionID, _ := cmd.Flags().GetString("execution-id")
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs/%s/executions%s", ws, args[0],
			queryString("execution_id", executionID, "after", after))
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		if executionID != "" {
			var one struct {
				ID     string `json:"id" yaml:"id"`
				Output string `json:"output" yaml:"output"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&one); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			// Human output is the output itself, verbatim — pipe it into
			// jq or a file without stripping a table around it.
			return resolvedFormatter(cmd).AutoHuman(one, func() {
				fmt.Println(one.Output)
			})
		}
		var page routineExecutionPage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if page.Rows == nil {
			page.Rows = []routineExecutionRow{}
		}
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(page, func() {
			if len(page.Rows) == 0 {
				fmt.Println("No step executions recorded for this run.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "EXECUTION_ID\tPATH\tATTEMPT\tKIND\tSTATUS\tAGENT\tMODEL\tSTARTED\tENDED\tOUTPUT\tERROR")
			for _, r := range page.Rows {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					r.ID, r.ExecutionPath, r.Attempt, r.Kind, r.Status, orDash(r.AgentSlug), orDash(r.Model),
					r.StartedAt, orDash(r.EndedAt), formatByteCount(r.OutputBytes), truncIDForCLI(r.Error, 60))
			}
			flush.of(w)
			if page.NextCursor != nil {
				fmt.Printf("More: crewship routine executions %s --after %s\n", args[0], *page.NextCursor)
			}
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// routineArtifactRow is one row of `routine artifacts`. content_bytes sizes
// an inline (text/json) artifact; a file artifact has a sha256 and is
// fetched with --download instead.
type routineArtifactRow struct {
	ID              string `json:"id" yaml:"id"`
	StepExecutionID string `json:"step_execution_id" yaml:"step_execution_id"`
	Kind            string `json:"kind" yaml:"kind"`
	Label           string `json:"label" yaml:"label"`
	State           string `json:"state" yaml:"state"`
	MediaType       string `json:"media_type" yaml:"media_type"`
	SHA256          string `json:"sha256" yaml:"sha256"`
	ContentBytes    int    `json:"content_bytes" yaml:"content_bytes"`
	Source          string `json:"source" yaml:"source"`
	Error           string `json:"error" yaml:"error"`
	CreatedAt       string `json:"created_at" yaml:"created_at"`
	ExecutionPath   string `json:"execution_path" yaml:"execution_path"`
	Attempt         int    `json:"attempt" yaml:"attempt"`
}

type routineArtifactPage struct {
	Artifacts  []routineArtifactRow `json:"artifacts" yaml:"artifacts"`
	Truncated  bool                 `json:"truncated" yaml:"truncated"`
	NextCursor *string              `json:"next_cursor" yaml:"next_cursor"`
}

var routineArtifactsCmd = &cobra.Command{
	Use:   "artifacts <run_id>",
	Short: "List the artifacts a run declared, print one's content, or download a file artifact",
	Long: `Artifacts are the outputs a step explicitly declared (an artifacts
envelope in its JSON output, or /crew/shared paths in its handoff) —
promoted, immutable, and tied to the step execution that produced them.
Pages of 50; pass the printed cursor back as --after.

Three shapes on one endpoint:
  (none)          the page of rows: kind, label, state, media type, size
  --artifact-id   that artifact's inline content (text/json kinds)
  --download      a file artifact's stored bytes, written to --out
                  (default: the artifact's label; '-' for stdout)

Distinct from 'routine result <run_id>', which lists files that CHANGED on
the crew during the run — this is what the run said it produced.

Examples:
  crewship routine artifacts run_abc123
  crewship routine artifacts run_abc123 --artifact-id art_7c...
  crewship routine artifacts run_abc123 --download art_7c... --out report.pdf
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		after, _ := cmd.Flags().GetString("after")
		artifactID, _ := cmd.Flags().GetString("artifact-id")
		download, _ := cmd.Flags().GetString("download")
		if artifactID != "" && download != "" {
			return fmt.Errorf("--artifact-id and --download are mutually exclusive")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs/%s/artifacts%s", ws, args[0],
			queryString("artifact_id", artifactID, "download", download, "after", after))
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		if download != "" {
			out, _ := cmd.Flags().GetString("out")
			return saveArtifactDownload(resp.Body, resp.Header.Get("Content-Disposition"), download, out)
		}
		if artifactID != "" {
			var one struct {
				ID      string `json:"id" yaml:"id"`
				Content string `json:"content" yaml:"content"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&one); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return resolvedFormatter(cmd).AutoHuman(one, func() {
				fmt.Println(one.Content)
			})
		}
		var page routineArtifactPage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if page.Artifacts == nil {
			page.Artifacts = []routineArtifactRow{}
		}
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(page, func() {
			if len(page.Artifacts) == 0 {
				fmt.Println("No artifacts declared by this run.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ARTIFACT_ID\tKIND\tLABEL\tSTATE\tMEDIA_TYPE\tSIZE\tPATH\tSOURCE\tERROR")
			for _, a := range page.Artifacts {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					a.ID, a.Kind, a.Label, a.State, orDash(a.MediaType), formatByteCount(a.ContentBytes),
					a.ExecutionPath, orDash(a.Source), truncIDForCLI(a.Error, 60))
			}
			flush.of(w)
			if page.NextCursor != nil {
				fmt.Printf("More: crewship routine artifacts %s --after %s\n", args[0], *page.NextCursor)
			}
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// saveArtifactDownload streams a --download body to disk (or stdout with
// --out -). The default file name is the label the server put in
// Content-Disposition, falling back to the artifact id; never a path the
// server chose — filepath.Base strips any directory it might carry.
func saveArtifactDownload(body io.Reader, disposition, artifactID, out string) error {
	if out == "" {
		out = artifactID
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if base := filepath.Base(params["filename"]); base != "." && base != "/" && base != "" {
				out = base
			}
		}
	}
	if out == "-" {
		_, err := io.Copy(os.Stdout, body)
		return err
	}
	// Atomic like downloadAgentFile: a transport hiccup mid-copy must not
	// leave a truncated file under the real name.
	af, err := cli.NewAtomicFile(out)
	if err != nil {
		return err
	}
	defer af.Close()
	n, err := io.Copy(af, body)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := af.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s[saved %d bytes → %s]%s\n", cli.Dim, n, out, cli.Reset)
	return nil
}

// formatByteCount renders a size column: "—" for nothing stored, otherwise
// a compact human unit. Kept local so a future change to how agent files
// print sizes does not silently restyle run-detail tables.
func formatByteCount(n int) string {
	switch {
	case n <= 0:
		return "—"
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

func init() {
	routineCalendarCmd.Flags().String("from", "", "RFC3339 start of the window (default: now)")
	routineCalendarCmd.Flags().String("to", "", "RFC3339 end of the window, at most 32 days after --from (default: --from + 7 days)")
	routineExecutionsCmd.Flags().String("after", "", "cursor from the previous page's next_cursor")
	routineExecutionsCmd.Flags().String("execution-id", "", "print this execution's output instead of the list")
	routineArtifactsCmd.Flags().String("after", "", "cursor from the previous page's next_cursor")
	routineArtifactsCmd.Flags().String("artifact-id", "", "print this artifact's inline content instead of the list")
	routineArtifactsCmd.Flags().String("download", "", "download this file artifact's stored bytes (see --out)")
	routineArtifactsCmd.Flags().String("out", "", "where --download writes (default: the artifact's file name; '-' for stdout)")
	pipelineCmd.AddCommand(routineCalendarCmd)
	pipelineCmd.AddCommand(routineExecutionsCmd)
	pipelineCmd.AddCommand(routineArtifactsCmd)
}
