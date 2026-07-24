package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// routineBacktestCmd is the "would this candidate version behave the
// same as HEAD did, on real recent traffic?" surface (issue #1421,
// routines DX audit 2026-07-24).
//
// It composes two primitives that already exist — it adds no new
// engine surface:
//
//  1. Pinned-version replay: POST .../pipelines/runs/{id}/replay with
//     {"pinned_version": N} re-invokes a prior run's CAPTURED inputs
//     against an immutable pipeline_versions row instead of HEAD (the
//     executor's RunInput.PinnedVersion — already used by scheduler /
//     webhook dispatch — is now reachable from an external caller via
//     this endpoint; see internal/api/pipeline_runs_replay.go).
//  2. Grading: the same COMPLETED/DEDUPED pass convention used by
//     `eval compare` / `routine bench` (isPassStatus), plus a literal
//     output-text comparison against the recorded original.
//
// The corpus is the workspace's own run history — GET
// .../pipelines/{slug}/run-records?status=completed — filtered
// client-side to the --last window. Nothing here creates a pipeline
// version, changes head_version, or otherwise touches which version
// live traffic resolves to: a backtest is a read-only evaluation of a
// candidate, never a promotion.
var routineBacktestCmd = &cobra.Command{
	Use:   "backtest <slug>",
	Short: "Replay recent successful runs against a candidate version and diff the outputs",
	Long: `Pulls a corpus of recently COMPLETED runs for a routine, replays each one
(with its ORIGINAL captured inputs) against a candidate pipeline version, and
grades the candidate's output against what was actually recorded — without
creating a new version or touching which version is live.

Verdict per run:
  MATCH      — candidate passed and its output is byte-identical to the original
  DIVERGED   — candidate passed but produced different output (behaviour changed)
  REGRESSED  — candidate did not pass (FAILED / CANCELLED / etc.)
  ERROR      — the replay call itself failed (transport / missing pinned version)

Aggregate verdict:
  NO_CORPUS            — no completed runs found in the window
  CLEAN                — every replayed run matched exactly
  DIVERGED_OUTPUTS     — no regressions, but at least one output changed
  REGRESSION_DETECTED  — at least one run regressed or errored (non-zero exit, CI-friendly)

Examples:
  # Backtest v9 against the last 7 days of successful runs
  crewship routine backtest support-triage --against 9 --last 7d

  # Narrower corpus + JSON for CI
  crewship routine backtest support-triage --against 9 --last 24h --limit 5 -f json`,
	Args: cobra.ExactArgs(1),
	RunE: runRoutineBacktest,
}

// backtestSourceRun is one corpus entry selected for replay — the
// recorded original a candidate run is graded against.
type backtestSourceRun struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Output    string `json:"output"`
	StartedAt string `json:"started_at"`
}

// backtestRunRow is one graded (source, candidate) pair.
type backtestRunRow struct {
	SourceRunID         string  `json:"source_run_id"`
	SourceStartedAt     string  `json:"source_started_at"`
	SourceOutput        string  `json:"source_output,omitempty"`
	CandidateRunID      string  `json:"candidate_run_id,omitempty"`
	CandidateStatus     string  `json:"candidate_status,omitempty"`
	CandidateOutput     string  `json:"candidate_output,omitempty"`
	CandidateCostUSD    float64 `json:"candidate_cost_usd,omitempty"`
	CandidateDurationMs int64   `json:"candidate_duration_ms,omitempty"`
	OutputChanged       bool    `json:"output_changed"`
	Error               string  `json:"error,omitempty"`
	Verdict             string  `json:"verdict"`
}

// backtestSummary is the top-level report — table / JSON / markdown
// via the shared formatter.
type backtestSummary struct {
	Slug           string           `json:"slug"`
	AgainstVersion int              `json:"against_version"`
	Since          string           `json:"since"`
	Runs           int              `json:"runs"`
	Matched        int              `json:"matched"`
	Diverged       int              `json:"diverged"`
	Regressed      int              `json:"regressed"`
	Errored        int              `json:"errored"`
	Rows           []backtestRunRow `json:"rows"`
	Verdict        string           `json:"verdict"`
	GeneratedAt    string           `json:"generated_at"`
}

func runRoutineBacktest(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	slug := args[0]
	against, _ := cmd.Flags().GetInt("against")
	lastRaw, _ := cmd.Flags().GetString("last")
	limit, _ := cmd.Flags().GetInt("limit")

	if against < 1 {
		return fmt.Errorf("--against required: candidate routine version to backtest (see `crewship routine versions %s`)", slug)
	}
	if limit < 1 {
		return fmt.Errorf("--limit must be >= 1")
	}

	since, err := parseSince(lastRaw)
	if err != nil {
		return fmt.Errorf("parse --last: %w", err)
	}

	client := newAPIClient()
	ws := client.GetWorkspaceID()
	out := cmd.OutOrStderr()

	corpus, err := selectBacktestCorpus(client, ws, slug, since, limit)
	if err != nil {
		return fmt.Errorf("select backtest corpus: %w", err)
	}

	fmt.Fprintf(out, "Backtesting %s against v%d — %d captured run(s) since %s\n",
		slug, against, len(corpus), since.UTC().Format(time.RFC3339))

	rows := make([]backtestRunRow, 0, len(corpus))
	for _, src := range corpus {
		row := replayBacktestRun(client, ws, against, src)
		rows = append(rows, row)
		printBacktestRow(out, row)
	}

	summary := summariseBacktest(slug, against, since, rows)
	return renderBacktestReport(cmd, summary)
}

// selectBacktestCorpus fetches the routine's completed run history
// (newest-first, per ListByPipeline) and keeps only the runs started
// at or after `since`, capped at `limit`. Over-fetches from the
// server (the run-records endpoint's own cap) so the client-side time
// filter has enough candidates to fill the window.
func selectBacktestCorpus(client *cli.Client, ws, slug string, since time.Time, limit int) ([]backtestSourceRun, error) {
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run-records?status=completed&limit=500", ws, slug))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var records []backtestSourceRun
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("decode run-records: %w", err)
	}
	return filterRunsSince(records, since, limit), nil
}

// filterRunsSince walks `records` (assumed newest-first) and keeps the
// first `limit` entries whose started_at falls at or after `since`.
// A row whose started_at doesn't parse is skipped rather than
// aborting the whole corpus — one malformed timestamp shouldn't sink
// an otherwise-usable backtest run.
func filterRunsSince(records []backtestSourceRun, since time.Time, limit int) []backtestSourceRun {
	out := make([]backtestSourceRun, 0, limit)
	for _, r := range records {
		ts, err := time.Parse(time.RFC3339Nano, r.StartedAt)
		if err != nil {
			continue
		}
		if ts.Before(since) {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// replayBacktestRun replays one source run pinned to the candidate
// version and grades the result. Errors (transport, non-2xx, decode)
// are absorbed into the row as Verdict="ERROR" rather than aborting
// the batch — one bad replay (e.g. concurrency limit) shouldn't sink
// the whole backtest.
func replayBacktestRun(client *cli.Client, ws string, against int, src backtestSourceRun) backtestRunRow {
	row := backtestRunRow{
		SourceRunID:     src.RunID,
		SourceStartedAt: src.StartedAt,
		SourceOutput:    src.Output,
	}
	// Synchronous /replay blocks until the run finishes — same
	// generous cap the eval/bench/compare surfaces use for a live
	// worker invocation.
	resp, err := client.WithTimeout(evalRunTimeout).Post(
		fmt.Sprintf("/api/v1/workspaces/%s/pipelines/runs/%s/replay", ws, src.RunID),
		map[string]any{"pinned_version": against},
	)
	if err != nil {
		row.Error = err.Error()
		row.Verdict = "ERROR"
		return row
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		row.Error = err.Error()
		row.Verdict = "ERROR"
		return row
	}
	var result struct {
		RunID        string  `json:"run_id"`
		Status       string  `json:"status"`
		Output       string  `json:"output"`
		DurationMs   int64   `json:"duration_ms"`
		CostUSD      float64 `json:"cost_usd"`
		ErrorMessage string  `json:"error_message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		row.Error = fmt.Sprintf("decode replay response: %v", err)
		row.Verdict = "ERROR"
		return row
	}
	row.CandidateRunID = result.RunID
	row.CandidateStatus = result.Status
	row.CandidateOutput = result.Output
	row.CandidateDurationMs = result.DurationMs
	row.CandidateCostUSD = result.CostUSD
	row.OutputChanged = result.Output != src.Output
	row.Verdict = backtestVerdict(src.Output, result.Status, result.Output)
	if row.Verdict == "REGRESSED" && result.ErrorMessage != "" {
		row.Error = result.ErrorMessage
	}
	return row
}

// backtestVerdict grades one candidate replay against its recorded
// original. The corpus is filtered to originally-COMPLETED runs, so
// the only question is whether the candidate reproduces that pass —
// isPassStatus is the same COMPLETED/DEDUPED convention `eval compare`
// and `routine bench` use elsewhere in the eval surface.
func backtestVerdict(sourceOutput, candidateStatus, candidateOutput string) string {
	if !isPassStatus(candidateStatus) {
		return "REGRESSED"
	}
	if candidateOutput != sourceOutput {
		return "DIVERGED"
	}
	return "MATCH"
}

// summariseBacktest collapses per-run rows into the headline verdict.
// A replay ERROR counts the same as a REGRESSED run for the aggregate
// verdict — an unreplayable run is not evidence the candidate is
// clean, so it must not be silently dropped from the count.
func summariseBacktest(slug string, against int, since time.Time, rows []backtestRunRow) backtestSummary {
	s := backtestSummary{
		Slug:           slug,
		AgainstVersion: against,
		Since:          since.UTC().Format(time.RFC3339),
		Runs:           len(rows),
		Rows:           rows,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	for _, r := range rows {
		switch r.Verdict {
		case "MATCH":
			s.Matched++
		case "DIVERGED":
			s.Diverged++
		case "REGRESSED":
			s.Regressed++
		case "ERROR":
			s.Errored++
		}
	}
	switch {
	case s.Runs == 0:
		s.Verdict = "NO_CORPUS"
	case s.Regressed > 0 || s.Errored > 0:
		s.Verdict = "REGRESSION_DETECTED"
	case s.Diverged > 0:
		s.Verdict = "DIVERGED_OUTPUTS"
	default:
		s.Verdict = "CLEAN"
	}
	return s
}

func printBacktestRow(w interface{ Write([]byte) (int, error) }, r backtestRunRow) {
	tail := ""
	if r.Error != "" {
		tail = "  (" + truncEvalLine(r.Error, 120) + ")"
	}
	fmt.Fprintf(asWriter(w), "  %s → %-9s  %-9s%s\n",
		shortBacktestID(r.SourceRunID), r.CandidateStatus, r.Verdict, tail)
}

func shortBacktestID(id string) string {
	if len(id) > 16 {
		return id[:16] + "…"
	}
	return id
}

func renderBacktestReport(cmd *cobra.Command, s backtestSummary) error {
	f := newFormatter()
	if err := f.AutoHuman(s, func() {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "\n────── backtest %s against v%d (since %s) ──────\n\n", s.Slug, s.AgainstVersion, s.Since)
		fmt.Fprintf(out, "  Runs:       %d\n", s.Runs)
		fmt.Fprintf(out, "  Matched:    %d\n", s.Matched)
		fmt.Fprintf(out, "  Diverged:   %d\n", s.Diverged)
		fmt.Fprintf(out, "  Regressed:  %d\n", s.Regressed)
		fmt.Fprintf(out, "  Errored:    %d\n", s.Errored)

		if len(s.Rows) > 0 {
			fmt.Fprintln(out)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SOURCE_RUN\tSOURCE_STARTED\tCANDIDATE_STATUS\tVERDICT")
			for _, r := range s.Rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", shortBacktestID(r.SourceRunID), r.SourceStartedAt, r.CandidateStatus, r.Verdict)
			}
			_ = tw.Flush()
		}

		fmt.Fprintf(out, "\n  Verdict:    %s\n", s.Verdict)
	}); err != nil {
		return fmt.Errorf("emit backtest report: %w", err)
	}

	if s.Verdict == "REGRESSION_DETECTED" {
		return fmt.Errorf("backtest found %d regression(s) and %d error(s) replaying %s against v%d — see rows above",
			s.Regressed, s.Errored, s.Slug, s.AgainstVersion)
	}
	return nil
}

func init() {
	routineBacktestCmd.Flags().Int("against", 0, "candidate routine version to backtest against (REQUIRED; see `routine versions <slug>`)")
	routineBacktestCmd.Flags().String("last", "7d", "how far back to pull the corpus of successful runs (e.g. 1h, 24h, 7d)")
	routineBacktestCmd.Flags().Int("limit", 20, "max number of captured runs to replay")

	pipelineCmd.AddCommand(routineBacktestCmd)
}
