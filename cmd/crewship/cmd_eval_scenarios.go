package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// evalScenariosCmd is the batch runner for the eval-* routine fleet.
// Spelled out as `crewship eval scenarios` (not `crewship eval run`)
// because the existing `eval replay` and `eval regression` are
// mission-level — scenarios is its own thing: a sweep across the
// pipeline-level eval suite that the seed populates.
//
// Default: list all `eval-*` routine slugs in the workspace and run
// each one once at the workspace's authored tier. With --tiers and
// --runs, the same N sweeps run on multiple tiers and the output
// summarises pass-rate per (scenario, tier).
//
// Cross-tier framing: this is the canonical "weak vs strong agent"
// comparison harness. Same DSL, same inputs, same gates — only the
// tier resolved by the executor differs. A scenario whose pass-rate
// matches across `fast` and `smart` is robust; a scenario with
// fast=4/10 / smart=9/10 reveals a worker-strength dependency that
// either the rubric needs to be tightened OR the worker step needs
// `complexity: smart` pinned.
var evalScenariosCmd = &cobra.Command{
	Use:   "scenarios",
	Short: "Batch-run eval-* routines across one or more tiers and report pass-rate per (scenario, tier)",
	Long: `Run the workspace's eval-* routines as a regression sweep.

Examples:
  # Run every eval-* routine once at the workspace's authored tier
  crewship eval scenarios

  # Run a specific subset N times on each of two tiers
  crewship eval scenarios --scenarios eval-extract-emails,eval-classify-sentiment \
                          --tiers fast,smart --runs 5

  # Machine-readable output for CI / spreadsheets
  crewship eval scenarios --runs 3 --tiers fast,smart -f json
  crewship eval scenarios --runs 3 --tiers fast,smart -f markdown`,
	RunE: runEvalScenarios,
}

// scenarioOutcome captures one single run's verdict. Stored per
// (scenario, tier, attempt) for aggregation. Cost + duration are
// surfaced even on failure — a scenario that passes 10/10 but
// burns 100x the budget is still a regression.
type scenarioOutcome struct {
	Scenario     string  `json:"scenario"`
	Tier         string  `json:"tier"`
	Attempt      int     `json:"attempt"`
	RunID        string  `json:"run_id"`
	Status       string  `json:"status"`
	DurationMs   int64   `json:"duration_ms"`
	CostUSD      float64 `json:"cost_usd"`
	FailedAtStep string  `json:"failed_at_step,omitempty"`
	ErrorMessage string  `json:"error_message,omitempty"`
}

// scenarioCell is the per-(scenario, tier) aggregate the matrix
// renders. Pass = Status == "COMPLETED". Idempotency dedupes
// (Status == "DEDUPED") count as a pass — a deduped run is the
// previous run's verdict, not a fresh failure.
type scenarioCell struct {
	Pass    int
	Total   int
	AvgCost float64
	AvgMs   float64
}

func runEvalScenarios(cmd *cobra.Command, _ []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	scenariosFlag, _ := cmd.Flags().GetString("scenarios")
	tiersFlag, _ := cmd.Flags().GetString("tiers")
	runs, _ := cmd.Flags().GetInt("runs")
	inputsRaw, _ := cmd.Flags().GetString("inputs")
	failFast, _ := cmd.Flags().GetBool("fail-fast")

	if runs < 1 {
		return fmt.Errorf("--runs must be >= 1")
	}

	tiers := splitCSV(tiersFlag)
	if len(tiers) == 0 {
		// Empty tiers slice means "no override" — runs each scenario
		// at whatever its authored complexity resolves to. We still
		// model it as a tier to make the matrix render sensibly.
		tiers = []string{""}
	}

	client := newAPIClient()
	ws := client.GetWorkspaceID()

	scenarios, err := resolveScenarioSlugs(client, ws, scenariosFlag)
	if err != nil {
		return err
	}
	if len(scenarios) == 0 {
		return fmt.Errorf("no scenarios resolved (looked for %q + eval-* in workspace)", scenariosFlag)
	}

	// Inputs payload is shared across every (scenario, tier, attempt)
	// so the cross-tier comparison is apples-to-apples — different
	// inputs would change what the gate is asking of the worker.
	var inputs map[string]any
	if inputsRaw != "" {
		if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
			return fmt.Errorf("parse --inputs JSON: %w", err)
		}
	}

	total := len(scenarios) * len(tiers) * runs
	fmt.Fprintf(cmd.OutOrStderr(), "Running %d invocations: %d scenarios × %d tiers × %d runs\n",
		total, len(scenarios), len(tiers), runs)

	outcomes := make([]scenarioOutcome, 0, total)
	for _, slug := range scenarios {
		for _, tier := range tiers {
			for attempt := 1; attempt <= runs; attempt++ {
				out := executeOneScenario(client, ws, slug, tier, attempt, inputs)
				outcomes = append(outcomes, out)
				printOutcomeLine(cmd.OutOrStderr(), out)
				if failFast && out.Status != "COMPLETED" && out.Status != "DEDUPED" {
					fmt.Fprintln(cmd.OutOrStderr(), "fail-fast: aborting after first failure")
					return renderEvalReport(cmd, outcomes, scenarios, tiers)
				}
			}
		}
	}

	return renderEvalReport(cmd, outcomes, scenarios, tiers)
}

// resolveScenarioSlugs returns the list of routine slugs the run
// targets. With --scenarios=foo,bar it short-circuits to the
// caller-supplied list (no remote validation; the run itself will
// 404 a typo). With no flag, it lists workspace routines and keeps
// only those starting with "eval-" — matching the seed convention.
func resolveScenarioSlugs(client *cli.Client, ws, supplied string) ([]string, error) {
	if supplied != "" {
		out := splitCSV(supplied)
		sort.Strings(out)
		return out, nil
	}
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines", ws))
	if err != nil {
		return nil, fmt.Errorf("list routines: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list routines: HTTP %d", resp.StatusCode)
	}
	var rows []struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode routine list: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if strings.HasPrefix(r.Slug, "eval-") {
			out = append(out, r.Slug)
		}
	}
	sort.Strings(out)
	return out, nil
}

// evalRunTimeout is the per-request cap for a synchronous scenario /run. It is
// generous because a graded scenario blocks on a worker run plus a grader loop
// (and tier escalation), which easily outlasts the 30s default and otherwise
// surfaces as a spurious "context deadline exceeded" even though the server-side
// run completes.
const evalRunTimeout = 10 * time.Minute

// executeOneScenario fires one /run request and shapes the response
// into a scenarioOutcome. Errors are absorbed into the outcome so
// the batch loop never crashes on a single bad run — that would
// defeat the regression-sweep use case. The scenario is recorded
// as Status="ERROR" with the transport-level error in ErrorMessage.
func executeOneScenario(client *cli.Client, ws, slug, tier string, attempt int, inputs map[string]any) scenarioOutcome {
	body := map[string]any{}
	if inputs != nil {
		body["inputs"] = inputs
	} else {
		body["inputs"] = map[string]any{}
	}
	if tier != "" {
		body["tier_override"] = tier
	}
	// Synchronous /run blocks until worker + grader loop finish — lift the
	// per-call timeout above the 30s default for just this request.
	resp, err := client.WithTimeout(evalRunTimeout).Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run", ws, slug), body)
	if err != nil {
		return scenarioOutcome{
			Scenario:     slug,
			Tier:         tier,
			Attempt:      attempt,
			Status:       "ERROR",
			ErrorMessage: err.Error(),
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes := readBodyBytes(resp.Body)
		return scenarioOutcome{
			Scenario:     slug,
			Tier:         tier,
			Attempt:      attempt,
			Status:       fmt.Sprintf("HTTP_%d", resp.StatusCode),
			ErrorMessage: truncEvalLine(string(bodyBytes), 240),
		}
	}
	var result struct {
		RunID        string  `json:"run_id"`
		Status       string  `json:"status"`
		DurationMs   int64   `json:"duration_ms"`
		CostUSD      float64 `json:"cost_usd"`
		FailedAtStep string  `json:"failed_at_step"`
		ErrorMessage string  `json:"error_message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return scenarioOutcome{
			Scenario:     slug,
			Tier:         tier,
			Attempt:      attempt,
			Status:       "DECODE_ERROR",
			ErrorMessage: err.Error(),
		}
	}
	return scenarioOutcome{
		Scenario:     slug,
		Tier:         tier,
		Attempt:      attempt,
		RunID:        result.RunID,
		Status:       result.Status,
		DurationMs:   result.DurationMs,
		CostUSD:      result.CostUSD,
		FailedAtStep: result.FailedAtStep,
		ErrorMessage: result.ErrorMessage,
	}
}

// printOutcomeLine emits a one-line progress update per run so the
// operator sees forward motion on a long sweep instead of staring
// at a silent terminal. Format intentionally short: scenario, tier,
// attempt, status, latency, cost. Failure reason goes on the next
// line at slight indent so eyes track the row→reason pairing.
func printOutcomeLine(w interface{ Write([]byte) (int, error) }, o scenarioOutcome) {
	tier := o.Tier
	if tier == "" {
		tier = "(authored)"
	}
	fmt.Fprintf(asWriter(w), "  %-32s %-10s #%d  %-9s  %5dms  $%.4f\n",
		o.Scenario, tier, o.Attempt, o.Status, o.DurationMs, o.CostUSD)
	if o.Status != "COMPLETED" && o.Status != "DEDUPED" && o.ErrorMessage != "" {
		fmt.Fprintf(asWriter(w), "    fail @ %s: %s\n", o.FailedAtStep, truncEvalLine(o.ErrorMessage, 200))
	}
}

// renderEvalReport materialises the (scenario, tier) matrix and
// hands it to the active formatter. Three formats supported: table
// (the default human view), json (one document with both the
// per-cell summary and the full outcomes for downstream tooling),
// and markdown (pasteable into PR descriptions or eval reports).
func renderEvalReport(cmd *cobra.Command, outcomes []scenarioOutcome, scenarios, tiers []string) error {
	matrix := aggregateMatrix(outcomes, scenarios, tiers)

	f := newFormatter()
	if f.Format == "json" {
		return f.JSON(map[string]any{
			"scenarios": scenarios,
			"tiers":     tiers,
			"matrix":    matrix,
			"outcomes":  outcomes,
			"generated": time.Now().UTC().Format(time.RFC3339),
		})
	}
	if f.Format == "yaml" {
		return f.YAML(map[string]any{
			"scenarios": scenarios,
			"tiers":     tiers,
			"matrix":    matrix,
			"outcomes":  outcomes,
		})
	}

	if f.Format == "markdown" {
		printMarkdownReport(cmd, scenarios, tiers, matrix)
		return nil
	}

	// Default table view.
	header := append([]string{"SCENARIO"}, prettyTierNames(tiers)...)
	rows := make([][]string, 0, len(scenarios))
	for _, slug := range scenarios {
		row := []string{slug}
		for _, tier := range tiers {
			cell := matrix[matrixKey(slug, tier)]
			row = append(row, fmt.Sprintf("%d/%d  $%.4f", cell.Pass, cell.Total, cell.AvgCost))
		}
		rows = append(rows, row)
	}
	f.Table(header, rows)
	return nil
}

// aggregateMatrix turns the flat outcomes slice into a (slug,
// tier) → cell map. DEDUPED counts as a pass (semantic: the
// dedupe path returned a previously-completed run); ERROR / non-200
// HTTP statuses count as a fail.
func aggregateMatrix(outcomes []scenarioOutcome, scenarios, tiers []string) map[string]scenarioCell {
	matrix := make(map[string]scenarioCell, len(scenarios)*len(tiers))
	totals := make(map[string]struct {
		cost float64
		ms   int64
		n    int
	}, len(scenarios)*len(tiers))
	for _, o := range outcomes {
		key := matrixKey(o.Scenario, o.Tier)
		cell := matrix[key]
		cell.Total++
		if o.Status == "COMPLETED" || o.Status == "DEDUPED" {
			cell.Pass++
		}
		matrix[key] = cell
		t := totals[key]
		t.cost += o.CostUSD
		t.ms += o.DurationMs
		t.n++
		totals[key] = t
	}
	for k, t := range totals {
		if t.n == 0 {
			continue
		}
		cell := matrix[k]
		cell.AvgCost = t.cost / float64(t.n)
		cell.AvgMs = float64(t.ms) / float64(t.n)
		matrix[k] = cell
	}
	return matrix
}

// printMarkdownReport emits a GitHub-flavoured markdown table of
// the cross-tier matrix. Useful for pasting into eval-suite PR
// descriptions or weekly review docs. Pass-rate is rendered as
// `pass/total`; cost is averaged across runs in that cell.
func printMarkdownReport(cmd *cobra.Command, scenarios, tiers []string, matrix map[string]scenarioCell) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "# Eval scenarios — cross-tier matrix")
	fmt.Fprintln(out)

	// Header row
	fmt.Fprint(out, "| Scenario |")
	for _, t := range tiers {
		name := t
		if name == "" {
			name = "(authored)"
		}
		fmt.Fprintf(out, " %s |", name)
	}
	fmt.Fprintln(out)

	// Separator
	fmt.Fprint(out, "| --- |")
	for range tiers {
		fmt.Fprint(out, " --- |")
	}
	fmt.Fprintln(out)

	// Rows
	for _, slug := range scenarios {
		fmt.Fprintf(out, "| `%s` |", slug)
		for _, tier := range tiers {
			cell := matrix[matrixKey(slug, tier)]
			fmt.Fprintf(out, " %d/%d (avg $%.4f) |", cell.Pass, cell.Total, cell.AvgCost)
		}
		fmt.Fprintln(out)
	}
}

func matrixKey(slug, tier string) string {
	return slug + "\x00" + tier
}

func prettyTierNames(tiers []string) []string {
	out := make([]string, len(tiers))
	for i, t := range tiers {
		if t == "" {
			out[i] = "(authored)"
			continue
		}
		out[i] = t
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncEvalLine(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// readBodyBytes is a tiny helper that avoids a dep on io.ReadAll
// being imported in this file specifically. Returns up to 8KiB so
// a misbehaving handler doesn't accidentally pull a megabyte of
// HTML into our error blurb.
func readBodyBytes(r interface{ Read([]byte) (int, error) }) []byte {
	const cap = 8 * 1024
	buf := make([]byte, cap)
	n, _ := r.Read(buf)
	return buf[:n]
}

// asWriter is a tiny adapter so cobra.OutOrStderr() (which returns
// io.Writer) can pass through this file's narrower interface
// constraint without dragging the io package in for a single
// import line.
func asWriter(w interface{ Write([]byte) (int, error) }) interface{ Write([]byte) (int, error) } {
	return w
}

func init() {
	evalScenariosCmd.Flags().String("scenarios", "", "comma-separated routine slugs (default: every eval-* routine in the workspace)")
	evalScenariosCmd.Flags().String("tiers", "", "comma-separated tier overrides (trivial|fast|moderate|smart). Empty = no override (use authored complexity).")
	evalScenariosCmd.Flags().Int("runs", 1, "number of runs per (scenario, tier) cell")
	evalScenariosCmd.Flags().String("inputs", "", "JSON inputs forwarded to every run (default: each routine's authored defaults)")
	evalScenariosCmd.Flags().Bool("fail-fast", false, "abort the sweep on the first non-pass instead of running every cell")

	evalCmd.AddCommand(evalScenariosCmd)
}
