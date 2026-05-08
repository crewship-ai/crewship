package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// evalBaselineCmd is the regression-detection surface for the eval
// suite. The pass-rate matrix from `eval scenarios` is great for a
// snapshot, but the real value is "did this PR / model swap / DSL
// refactor change my baseline behaviour?" — that requires storing
// the matrix and comparing future runs against it.
//
// Storage is intentionally file-local (~/.crewship/eval-baselines/),
// not server-side. Two reasons: (1) baselines are per-developer and
// per-CI-run, not workspace-shared state — multiple authors editing
// the same routine want independent baselines; (2) avoids a server-
// side migration + RBAC surface for what is fundamentally a tool-
// chain artefact, not core platform state.
//
// CI workflow this enables:
//
//	# On a known-good main commit
//	crewship eval baseline save main --scenarios ... --tiers fast,smart --runs 10
//
//	# On a PR branch CI step
//	crewship eval baseline diff main --tiers fast,smart --runs 10
//	# exit 1 if any cell's pass-rate dropped
var evalBaselineCmd = &cobra.Command{
	Use:   "baseline",
	Short: "Manage eval baselines (snapshot + regression diff against pass-rate matrix)",
	Long: `Save a snapshot of the eval matrix and diff future runs against it.

Examples:
  # Snapshot current behaviour as the regression baseline
  crewship eval baseline save main --tiers fast,smart --runs 5

  # In CI: diff a PR run against the main baseline; non-zero exit on regression
  crewship eval baseline diff main --tiers fast,smart --runs 5

  # Inspect what's stored
  crewship eval baseline list
  crewship eval baseline show main

  # Remove a stale baseline
  crewship eval baseline delete main`,
}

// baselineRecord is the persisted matrix snapshot. Keep the schema
// flat + JSON-tagged so a future migration can read old files
// without a converter.
type baselineRecord struct {
	Name        string                  `json:"name"`
	GeneratedAt string                  `json:"generated_at"`
	WorkspaceID string                  `json:"workspace_id"`
	Scenarios   []string                `json:"scenarios"`
	Tiers       []string                `json:"tiers"`
	RunsPerCell int                     `json:"runs_per_cell"`
	Cells       map[string]baselineCell `json:"cells"` // key = "<scenario>\x00<tier>"
}

type baselineCell struct {
	Pass    int     `json:"pass"`
	Total   int     `json:"total"`
	AvgCost float64 `json:"avg_cost"`
	AvgMs   float64 `json:"avg_ms"`
}

// baselineDir is the on-disk root for stored baselines. ~/.crewship
// is the standard location for CLI-side state (already used by
// cli-config.yaml etc). One file per baseline keeps listing /
// deletion trivial and makes the dir grep-friendly.
func baselineDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create baseline dir: %w", err)
	}
	return dir, nil
}

func baselinePath(name string) (string, error) {
	if !isValidBaselineName(name) {
		return "", fmt.Errorf("baseline name %q invalid (alpha-num + dash + underscore, 1-64 chars)", name)
	}
	dir, err := baselineDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

func isValidBaselineName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// ── save ──────────────────────────────────────────────────────────

var evalBaselineSaveCmd = &cobra.Command{
	Use:   "save <name>",
	Short: "Run the eval matrix and persist as a regression baseline",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		name := args[0]
		path, err := baselinePath(name)
		if err != nil {
			return err
		}
		// Reuse the eval scenarios sweep machinery so save and
		// diff produce strictly comparable matrices.
		matrix, scenarios, tiers, runs, err := executeBaselineSweep(cmd)
		if err != nil {
			return err
		}
		client := newAPIClient()
		rec := baselineRecord{
			Name:        name,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			WorkspaceID: client.GetWorkspaceID(),
			Scenarios:   scenarios,
			Tiers:       tiers,
			RunsPerCell: runs,
			Cells:       cellsToBaseline(matrix),
		}
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal baseline: %w", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStderr(), "\nSaved baseline %q → %s (%d cells)\n", name, path, len(rec.Cells))
		return nil
	},
}

// cellsToBaseline converts the eval scenarios matrix (which holds
// scenarioCell shape) into the on-disk baselineCell shape. They're
// near-identical today but kept distinct so the on-disk format can
// evolve (add timestamps, hash of routine def, etc) without
// breaking the in-process sweep type.
func cellsToBaseline(matrix map[string]scenarioCell) map[string]baselineCell {
	out := make(map[string]baselineCell, len(matrix))
	for k, v := range matrix {
		out[k] = baselineCell{Pass: v.Pass, Total: v.Total, AvgCost: v.AvgCost, AvgMs: v.AvgMs}
	}
	return out
}

// executeBaselineSweep runs the same logic as `eval scenarios` and
// returns the matrix + axes. Implemented in this file (duplicating
// a few lines from cmd_eval_scenarios.go) rather than refactoring
// the existing command, because save + diff want the matrix in
// memory, not the CLI report rendered.
func executeBaselineSweep(cmd *cobra.Command) (matrix map[string]scenarioCell, scenarios, tiers []string, runs int, err error) {
	scenariosFlag, _ := cmd.Flags().GetString("scenarios")
	tiersFlag, _ := cmd.Flags().GetString("tiers")
	runs, _ = cmd.Flags().GetInt("runs")
	inputsRaw, _ := cmd.Flags().GetString("inputs")

	if runs < 1 {
		return nil, nil, nil, 0, fmt.Errorf("--runs must be >= 1")
	}

	tiers = splitCSV(tiersFlag)
	if len(tiers) == 0 {
		tiers = []string{""}
	}

	client := newAPIClient()
	ws := client.GetWorkspaceID()

	scenarios, err = resolveScenarioSlugs(client, ws, scenariosFlag)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	if len(scenarios) == 0 {
		return nil, nil, nil, 0, fmt.Errorf("no scenarios resolved (looked for %q + eval-* in workspace)", scenariosFlag)
	}

	var inputs map[string]any
	if inputsRaw != "" {
		if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
			return nil, nil, nil, 0, fmt.Errorf("parse --inputs JSON: %w", err)
		}
	}

	total := len(scenarios) * len(tiers) * runs
	fmt.Fprintf(cmd.OutOrStderr(), "Sweeping %d invocations: %d scenarios × %d tiers × %d runs\n",
		total, len(scenarios), len(tiers), runs)

	outcomes := make([]scenarioOutcome, 0, total)
	for _, slug := range scenarios {
		for _, tier := range tiers {
			for attempt := 1; attempt <= runs; attempt++ {
				out := executeOneScenario(client, ws, slug, tier, attempt, inputs)
				outcomes = append(outcomes, out)
				printOutcomeLine(cmd.OutOrStderr(), out)
			}
		}
	}
	matrix = aggregateMatrix(outcomes, scenarios, tiers)
	return matrix, scenarios, tiers, runs, nil
}

// ── list ──────────────────────────────────────────────────────────

var evalBaselineListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored eval baselines",
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := baselineDir()
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read baseline dir: %w", err)
		}
		type row struct {
			Name        string `json:"name"`
			GeneratedAt string `json:"generated_at"`
			Scenarios   int    `json:"scenarios"`
			Tiers       int    `json:"tiers"`
			RunsPerCell int    `json:"runs_per_cell"`
			Path        string `json:"path"`
		}
		rows := make([]row, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			var rec baselineRecord
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if json.Unmarshal(data, &rec) != nil {
				continue
			}
			rows = append(rows, row{
				Name:        rec.Name,
				GeneratedAt: rec.GeneratedAt,
				Scenarios:   len(rec.Scenarios),
				Tiers:       len(rec.Tiers),
				RunsPerCell: rec.RunsPerCell,
				Path:        path,
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(rows)
		}
		if len(rows) == 0 {
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "(no baselines stored yet)")
			fmt.Fprintln(out, "Save one with: crewship eval baseline save <name> --tiers fast,smart --runs 5")
			return nil
		}
		header := []string{"NAME", "GENERATED", "SCENARIOS", "TIERS", "RUNS/CELL"}
		tbl := make([][]string, 0, len(rows))
		for _, r := range rows {
			tbl = append(tbl, []string{r.Name, r.GeneratedAt, fmt.Sprint(r.Scenarios), fmt.Sprint(r.Tiers), fmt.Sprint(r.RunsPerCell)})
		}
		f.Table(header, tbl)
		return nil
	},
}

// ── show ──────────────────────────────────────────────────────────

var evalBaselineShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a stored baseline's matrix in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := baselinePath(args[0])
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("baseline %q not found (saved baselines: crewship eval baseline list)", args[0])
			}
			return fmt.Errorf("read baseline: %w", err)
		}
		var rec baselineRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("parse baseline: %w", err)
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(rec)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Baseline:    %s\nGenerated:   %s\nWorkspace:   %s\nRuns/cell:   %d\n\n",
			rec.Name, rec.GeneratedAt, rec.WorkspaceID, rec.RunsPerCell)
		header := append([]string{"SCENARIO"}, prettyTierNames(rec.Tiers)...)
		rows := make([][]string, 0, len(rec.Scenarios))
		for _, slug := range rec.Scenarios {
			row := []string{slug}
			for _, t := range rec.Tiers {
				c := rec.Cells[matrixKey(slug, t)]
				row = append(row, fmt.Sprintf("%d/%d $%.4f", c.Pass, c.Total, c.AvgCost))
			}
			rows = append(rows, row)
		}
		f.Table(header, rows)
		return nil
	},
}

// ── delete ────────────────────────────────────────────────────────

var evalBaselineDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a stored baseline",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := baselinePath(args[0])
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("baseline %q not found", args[0])
			}
			return fmt.Errorf("delete baseline: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted baseline %q\n", args[0])
		return nil
	},
}

// ── diff ──────────────────────────────────────────────────────────

// regressionRow captures one cell's delta between baseline and
// current. Verdict is the headline label used by table + json
// outputs; we expose it directly so external tooling doesn't have
// to recompute.
type regressionRow struct {
	Scenario       string  `json:"scenario"`
	Tier           string  `json:"tier"`
	BaselinePass   int     `json:"baseline_pass"`
	BaselineTotal  int     `json:"baseline_total"`
	CurrentPass    int     `json:"current_pass"`
	CurrentTotal   int     `json:"current_total"`
	BaselineRate   float64 `json:"baseline_rate"`
	CurrentRate    float64 `json:"current_rate"`
	RateDelta      float64 `json:"rate_delta"`
	BaselineCostUS float64 `json:"baseline_cost_usd"`
	CurrentCostUS  float64 `json:"current_cost_usd"`
	Verdict        string  `json:"verdict"`
}

var evalBaselineDiffCmd = &cobra.Command{
	Use:   "diff <name>",
	Short: "Re-run the matrix and diff against a stored baseline (non-zero exit on regression)",
	Long: `Run the eval matrix again and compare to a stored baseline.

Verdict per (scenario, tier) cell:
  REGRESSION  — current pass-rate dropped by more than --tolerance vs baseline
  IMPROVED    — current pass-rate rose by more than --tolerance vs baseline
  STABLE      — within tolerance
  NEW         — cell exists in current run but not in baseline
  REMOVED     — cell exists in baseline but not in current run (warning only)

Exits non-zero (1) if any cell is REGRESSION. CI-friendly.

Examples:
  crewship eval baseline diff main --tiers fast,smart --runs 5
  crewship eval baseline diff main --tolerance 0.10 --runs 10 -f json`,
	Args: cobra.ExactArgs(1),
	RunE: runEvalBaselineDiff,
}

func runEvalBaselineDiff(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	tolerance, _ := cmd.Flags().GetFloat64("tolerance")

	path, err := baselinePath(args[0])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("baseline %q not found (saved baselines: crewship eval baseline list)", args[0])
		}
		return fmt.Errorf("read baseline: %w", err)
	}
	var baseline baselineRecord
	if err := json.Unmarshal(data, &baseline); err != nil {
		return fmt.Errorf("parse baseline: %w", err)
	}

	// Refuse cross-workspace diff. Baselines record their source
	// workspace at save time; reusing the same name across
	// workspaces is a footgun (CI would silently report "regression"
	// when really comparing two unrelated matrices). Surface the
	// mismatch loudly and tell the operator how to recover.
	currentWS := newAPIClient().GetWorkspaceID()
	if baseline.WorkspaceID != "" && currentWS != "" && baseline.WorkspaceID != currentWS {
		return fmt.Errorf("baseline %q was saved for workspace %s but you are in workspace %s — diff aborted (re-save the baseline in the current workspace, or switch workspaces with `crewship config workspace`)",
			baseline.Name, baseline.WorkspaceID, currentWS)
	}

	currentMatrix, currentScenarios, currentTiers, _, err := executeBaselineSweep(cmd)
	if err != nil {
		return err
	}

	rows := computeRegressionRows(baseline, currentMatrix, currentScenarios, currentTiers, tolerance)

	regressionCount := 0
	for _, r := range rows {
		if r.Verdict == "REGRESSION" {
			regressionCount++
		}
	}

	f := newFormatter()
	if f.Format == "json" {
		_ = f.JSON(map[string]any{
			"baseline_name":     baseline.Name,
			"baseline_at":       baseline.GeneratedAt,
			"tolerance":         tolerance,
			"regression_count":  regressionCount,
			"rows":              rows,
		})
	} else {
		printRegressionTable(cmd, baseline, rows, tolerance)
	}

	if regressionCount > 0 {
		return fmt.Errorf("%d cell(s) regressed beyond tolerance %.2f", regressionCount, tolerance)
	}
	return nil
}

// computeRegressionRows walks the union of baseline + current cells
// and produces a regressionRow per (scenario, tier). The union
// handles the NEW / REMOVED cases — adding a scenario or tier
// since the baseline shouldn't crash the diff, and it's worth
// flagging so the operator knows their baseline is stale.
func computeRegressionRows(baseline baselineRecord, currentMatrix map[string]scenarioCell, currentScenarios, currentTiers []string, tolerance float64) []regressionRow {
	// Build the union of axes so REMOVED cells (in baseline but
	// not in current run) get a row.
	allScenarios := mergeUnique(baseline.Scenarios, currentScenarios)
	allTiers := mergeUnique(baseline.Tiers, currentTiers)

	rows := make([]regressionRow, 0, len(allScenarios)*len(allTiers))
	for _, slug := range allScenarios {
		for _, tier := range allTiers {
			key := matrixKey(slug, tier)
			b, hasB := baseline.Cells[key]
			c, hasC := currentMatrix[key]

			r := regressionRow{Scenario: slug, Tier: tier}

			switch {
			case !hasB && hasC:
				r.CurrentPass = c.Pass
				r.CurrentTotal = c.Total
				r.CurrentRate = passRate(c.Pass, c.Total)
				r.CurrentCostUS = c.AvgCost
				r.Verdict = "NEW"
			case hasB && !hasC:
				r.BaselinePass = b.Pass
				r.BaselineTotal = b.Total
				r.BaselineRate = passRate(b.Pass, b.Total)
				r.BaselineCostUS = b.AvgCost
				r.Verdict = "REMOVED"
			default:
				r.BaselinePass = b.Pass
				r.BaselineTotal = b.Total
				r.BaselineRate = passRate(b.Pass, b.Total)
				r.BaselineCostUS = b.AvgCost
				r.CurrentPass = c.Pass
				r.CurrentTotal = c.Total
				r.CurrentRate = passRate(c.Pass, c.Total)
				r.CurrentCostUS = c.AvgCost
				r.RateDelta = r.CurrentRate - r.BaselineRate
				switch {
				case r.RateDelta < -tolerance:
					r.Verdict = "REGRESSION"
				case r.RateDelta > tolerance:
					r.Verdict = "IMPROVED"
				default:
					r.Verdict = "STABLE"
				}
			}
			rows = append(rows, r)
		}
	}
	return rows
}

func passRate(pass, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(pass) / float64(total)
}

func mergeUnique(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func printRegressionTable(cmd *cobra.Command, baseline baselineRecord, rows []regressionRow, tolerance float64) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\nBaseline:    %s (saved %s)\nTolerance:   %.2f\n\n", baseline.Name, baseline.GeneratedAt, tolerance)

	header := []string{"SCENARIO", "TIER", "BASELINE", "CURRENT", "Δ", "VERDICT"}
	tbl := make([][]string, 0, len(rows))
	for _, r := range rows {
		tier := r.Tier
		if tier == "" {
			tier = "(authored)"
		}
		baseStr := fmt.Sprintf("%d/%d (%.0f%%)", r.BaselinePass, r.BaselineTotal, r.BaselineRate*100)
		curStr := fmt.Sprintf("%d/%d (%.0f%%)", r.CurrentPass, r.CurrentTotal, r.CurrentRate*100)
		if r.Verdict == "NEW" {
			baseStr = "—"
		}
		if r.Verdict == "REMOVED" {
			curStr = "—"
		}
		delta := fmt.Sprintf("%+.0f%%", r.RateDelta*100)
		if r.Verdict == "NEW" || r.Verdict == "REMOVED" {
			delta = "—"
		}
		tbl = append(tbl, []string{r.Scenario, tier, baseStr, curStr, delta, r.Verdict})
	}
	newFormatter().Table(header, tbl)
}

func init() {
	// Sweep flags shared by save + diff. Defining on each is clearer
	// than inheritance — both are full commands, both want the same
	// scenario / tier / runs knobs.
	for _, c := range []*cobra.Command{evalBaselineSaveCmd, evalBaselineDiffCmd} {
		c.Flags().String("scenarios", "", "comma-separated routine slugs (default: every eval-* routine)")
		c.Flags().String("tiers", "", "comma-separated tier overrides (default: no override)")
		c.Flags().Int("runs", 5, "runs per (scenario, tier) cell")
		c.Flags().String("inputs", "", "JSON inputs forwarded to every run")
	}
	evalBaselineDiffCmd.Flags().Float64("tolerance", 0.10, "pass-rate delta tolerance for the STABLE verdict (e.g. 0.10 = ±10pp)")

	evalBaselineCmd.AddCommand(evalBaselineSaveCmd)
	evalBaselineCmd.AddCommand(evalBaselineListCmd)
	evalBaselineCmd.AddCommand(evalBaselineShowCmd)
	evalBaselineCmd.AddCommand(evalBaselineDeleteCmd)
	evalBaselineCmd.AddCommand(evalBaselineDiffCmd)
	evalCmd.AddCommand(evalBaselineCmd)
}
