package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These tests lock the --format contract for the `eval` family (batch 3 of
// #964). Each migrated command used to honor only `--format json` (some also
// yaml) while silently degrading --format ndjson (and, for a couple, yaml)
// back to the hand-crafted human view. After the sweep the machine formats
// route through Formatter.AutoHuman; these guards prove the flip side — that
// the DEFAULT (no --format) still emits the human view byte-for-byte, so
// scripts and eyeballs relying on the tables/prose don't regress.

// mustWriteBaseline persists a baselineRecord under a temp $HOME so the
// file-local baseline commands (list/show/diff) read a known fixture.
func mustWriteBaseline(t *testing.T, home string, rec baselineRecord) {
	t.Helper()
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir baseline dir: %v", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, rec.Name+".json"), data, 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
}

// ── eval runs (cmd_eval.go) ────────────────────────────────────────────────

func TestEvalRunsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/eval/runs", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "er_1", "kind": "replay", "mission_id": "MIS-42", "baseline_mission_id": "", "status": "QUEUED", "created_at": "2026-07-13T00:00:00Z"},
		},
		"count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = evalRunsCmd.RunE(evalRunsCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human table (not JSON): uppercase column headers + the raw row cells.
	for _, want := range []string{"KIND", "er_1", "replay"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestEvalRunsRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// evalRunsCmd routes body.Rows (a slice) through f.AutoHuman now, so
// --format yaml/ndjson must carry the run rows instead of the table.
func TestEvalRunsRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/eval/runs", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "er_1", "kind": "replay", "mission_id": "MIS-42", "baseline_mission_id": "", "status": "QUEUED", "created_at": "2026-07-13T00:00:00Z"},
			{"id": "er_2", "kind": "compare", "mission_id": "MIS-43", "baseline_mission_id": "MIS-42", "status": "DONE", "created_at": "2026-07-13T00:01:00Z"},
		},
		"count": 2,
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = evalRunsCmd.RunE(evalRunsCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["kind"] != "replay" || yamlRows[1]["kind"] != "compare" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = evalRunsCmd.RunE(evalRunsCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per run row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["kind"] != "replay" || second["kind"] != "compare" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
	}
}

// ── eval baseline list / show / diff (cmd_eval_baseline.go) ─────────────────

func TestEvalBaselineListRunE_DefaultStaysHuman(t *testing.T) {
	covSetupCli5(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteBaseline(t, home, baselineRecord{
		Name:        "mainbase",
		GeneratedAt: "2026-07-13T00:00:00Z",
		Scenarios:   []string{"eval-x"},
		Tiers:       []string{"fast"},
		RunsPerCell: 5,
	})

	var err error
	out := covCaptureStdoutCli5(t, func() { err = evalBaselineListCmd.RunE(evalBaselineListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"NAME", "GENERATED", "mainbase"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestEvalBaselineListRunE_YAMLAndNDJSON is the flip side of
// DefaultStaysHuman: evalBaselineListCmd routes its summary rows (a slice)
// through f.AutoHuman now, so --format yaml/ndjson must carry the baseline
// summaries instead of the table.
func TestEvalBaselineListRunE_YAMLAndNDJSON(t *testing.T) {
	covSetupCli5(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteBaseline(t, home, baselineRecord{
		Name:        "mainbase",
		GeneratedAt: "2026-07-13T00:00:00Z",
		Scenarios:   []string{"eval-x"},
		Tiers:       []string{"fast"},
		RunsPerCell: 5,
	})
	mustWriteBaseline(t, home, baselineRecord{
		Name:        "secondbase",
		GeneratedAt: "2026-07-12T00:00:00Z",
		Scenarios:   []string{"eval-y"},
		Tiers:       []string{"smart"},
		RunsPerCell: 3,
	})

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = evalBaselineListCmd.RunE(evalBaselineListCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 baseline rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["name"] != "mainbase" || yamlRows[1]["name"] != "secondbase" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = evalBaselineListCmd.RunE(evalBaselineListCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per baseline row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["name"] != "mainbase" || second["name"] != "secondbase" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
	}
}

func TestEvalBaselineShowRunE_DefaultStaysHuman(t *testing.T) {
	covSetupCli5(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteBaseline(t, home, baselineRecord{
		Name:        "mainbase",
		GeneratedAt: "2026-07-13T00:00:00Z",
		Scenarios:   []string{"eval-x"},
		Tiers:       []string{"fast"},
		RunsPerCell: 5,
	})

	var err error
	out := covCaptureStdoutCli5(t, func() { err = evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"mainbase"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human detail prose (not JSON): the `Baseline:` label + slug + matrix row.
	for _, want := range []string{"Baseline:", "mainbase", "eval-x"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestEvalBaselineDiffRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Baseline with one cell for (eval-x, authored-tier) at 1/1 pass, and an
	// empty WorkspaceID so the cross-workspace guard is skipped.
	mustWriteBaseline(t, home, baselineRecord{
		Name:        "mainbase",
		GeneratedAt: "2026-07-13T00:00:00Z",
		WorkspaceID: "",
		Scenarios:   []string{"eval-x"},
		Tiers:       []string{""},
		RunsPerCell: 1,
		Cells:       map[string]baselineCell{matrixKey("eval-x", ""): {Pass: 1, Total: 1}},
	})
	// The diff re-runs the matrix: list eval-* routines, then one /run per cell.
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines",
		clitest.JSONResponse(200, []map[string]any{{"slug": "eval-x"}}))
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/eval-x/run",
		clitest.JSONResponse(200, map[string]any{"run_id": "r1", "status": "COMPLETED", "duration_ms": 5, "cost_usd": 0.01}))
	covSetFlagCli5(t, evalBaselineDiffCmd, "runs", "1")

	var err error
	out := covCaptureStdoutCli5(t, func() { err = runEvalBaselineDiff(evalBaselineDiffCmd, []string{"mainbase"}) })
	if err != nil {
		t.Fatalf("RunE (no regression expected): %v", err)
	}
	// Human regression table (not JSON): the header prose + STABLE verdict.
	for _, want := range []string{"Baseline:", "SCENARIO", "VERDICT", "STABLE"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ── eval compare (cmd_eval_compare.go) ──────────────────────────────────────

func TestEvalCompareRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	// tier-a=fast / tier-b=smart both POST the same /run path (differ only in
	// the request body's tier_override), so one stub serves both sides.
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/eval-x/run",
		clitest.JSONResponse(200, map[string]any{
			"run_id": "run_a", "status": "COMPLETED", "output": "hello world",
			"duration_ms": 10, "cost_usd": 0.02,
		}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = runEvalCompare(evalCompareCmd, []string{"eval-x"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human head-to-head (not JSON): the Scenario/Verdict prose + verdict label.
	for _, want := range []string{"Scenario:", "Verdict:", "AGREE-PASS", "eval-x"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestEvalCompareRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// runEvalCompare routes its {scenario, side_a, side_b, agreement} payload
// through f.AutoHuman now, so --format yaml/ndjson must carry it instead of
// the table/markdown renderers.
func TestEvalCompareRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/eval-x/run",
		clitest.JSONResponse(200, map[string]any{
			"run_id": "run_a", "status": "COMPLETED", "output": "hello world",
			"duration_ms": 10, "cost_usd": 0.02,
		}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = runEvalCompare(evalCompareCmd, []string{"eval-x"}) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	if yamlPayload["scenario"] != "eval-x" {
		t.Errorf("yaml payload missing scenario=eval-x: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = runEvalCompare(evalCompareCmd, []string{"eval-x"}) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The compare payload is a single map (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the compare object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["scenario"] != "eval-x" {
		t.Errorf("ndjson payload missing scenario=eval-x: %+v", ndjsonPayload)
	}
}

// ── eval scenarios report (cmd_eval_scenarios.go) ───────────────────────────

func TestRenderEvalReport_DefaultStaysHuman(t *testing.T) {
	covSetupCli5(t) // resets flagFormat to "" (default human view)
	outcomes := []scenarioOutcome{
		{Scenario: "eval-x", Tier: "", Attempt: 1, Status: "COMPLETED", DurationMs: 5, CostUSD: 0.01},
	}

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = renderEvalReport(evalScenariosCmd, outcomes, []string{"eval-x"}, []string{""})
	})
	if err != nil {
		t.Fatalf("renderEvalReport: %v", err)
	}
	// Human matrix table (not JSON/YAML): SCENARIO header + slug + pass ratio.
	for _, want := range []string{"SCENARIO", "eval-x", "1/1"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestRenderEvalReportRunE_YAMLAndNDJSON is the flip side of
// DefaultStaysHuman: renderEvalReport routes its {scenarios, tiers, matrix,
// outcomes, generated} payload through f.AutoHuman now, so --format
// yaml/ndjson must carry it. This also pins the intentional schema fix
// flagged in review: --format yaml previously built its own map literal
// WITHOUT a `generated` field (only json had it); folding into one
// AutoHuman payload means yaml now includes `generated` too.
func TestRenderEvalReportRunE_YAMLAndNDJSON(t *testing.T) {
	covSetupCli5(t)
	outcomes := []scenarioOutcome{
		{Scenario: "eval-x", Tier: "", Attempt: 1, Status: "COMPLETED", DurationMs: 5, CostUSD: 0.01},
	}

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = renderEvalReport(evalScenariosCmd, outcomes, []string{"eval-x"}, []string{""})
	})
	if err != nil {
		t.Fatalf("yaml renderEvalReport: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	if _, ok := yamlPayload["scenarios"]; !ok {
		t.Errorf("yaml payload missing scenarios: %+v", yamlPayload)
	}
	// The schema-fix pin: `generated` used to be json-only.
	if _, ok := yamlPayload["generated"]; !ok {
		t.Errorf("yaml payload missing `generated` key (schema fix regressed); got: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() {
		err = renderEvalReport(evalScenariosCmd, outcomes, []string{"eval-x"}, []string{""})
	})
	if err != nil {
		t.Fatalf("ndjson renderEvalReport: %v", err)
	}
	// The report is a single map (not a top-level slice), so NDJSON emits
	// exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the report object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if _, ok := ndjsonPayload["generated"]; !ok {
		t.Errorf("ndjson payload missing `generated` key: %+v", ndjsonPayload)
	}
}
