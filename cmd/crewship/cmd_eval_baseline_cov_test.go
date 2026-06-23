package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covHomeDir points os.UserHomeDir at an isolated temp dir so the
// baseline store never touches the real ~/.crewship.
func covHomeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func covWriteBaseline(t *testing.T, home string, rec baselineRecord) string {
	t.Helper()
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, rec.Name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ─── path plumbing ──────────────────────────────────────────────────────

func TestBaselineDirAndPath(t *testing.T) {
	home := covHomeDir(t)

	dir, err := baselineDir()
	if err != nil {
		t.Fatalf("baselineDir: %v", err)
	}
	want := filepath.Join(home, ".crewship", "eval-baselines")
	if dir != want {
		t.Errorf("dir: got %q want %q", dir, want)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("baseline dir not created: %v", err)
	}

	p, err := baselinePath("main")
	if err != nil {
		t.Fatalf("baselinePath: %v", err)
	}
	if p != filepath.Join(want, "main.json") {
		t.Errorf("path: got %q", p)
	}

	if _, err := baselinePath("../escape"); err == nil {
		t.Error("expected invalid-name error for path traversal attempt")
	}
	if _, err := baselinePath(""); err == nil {
		t.Error("expected invalid-name error for empty name")
	}
}

// ─── list ───────────────────────────────────────────────────────────────

func TestEvalBaselineListCmd(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		covHomeDir(t)
		saveCLIState(t)
		cliCfg = nil
		out := covCaptureStdoutCli3(t, func() {
			if err := evalBaselineListCmd.RunE(evalBaselineListCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "no baselines stored yet") {
			t.Errorf("expected empty hint: %q", out)
		}
	})

	t.Run("rows with junk files skipped", func(t *testing.T) {
		home := covHomeDir(t)
		saveCLIState(t)
		cliCfg = nil
		covWriteBaseline(t, home, baselineRecord{
			Name: "main", GeneratedAt: "2026-06-01T00:00:00Z",
			Scenarios: []string{"eval-a", "eval-b"}, Tiers: []string{"fast"}, RunsPerCell: 5,
		})
		dir := filepath.Join(home, ".crewship", "eval-baselines")
		// Junk that must be skipped: invalid JSON, non-.json, a directory.
		if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{nope"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
			t.Fatal(err)
		}

		out := covCaptureStdoutCli3(t, func() {
			if err := evalBaselineListCmd.RunE(evalBaselineListCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "main") || !strings.Contains(out, "2026-06-01T00:00:00Z") {
			t.Errorf("baseline row missing: %q", out)
		}
		if strings.Contains(out, "broken") || strings.Contains(out, "notes") {
			t.Errorf("junk files leaked into listing: %q", out)
		}
	})

	t.Run("json format", func(t *testing.T) {
		home := covHomeDir(t)
		covStub(t)
		cliCfg.Format = "json"
		covWriteBaseline(t, home, baselineRecord{Name: "m2", RunsPerCell: 3})
		out := covCaptureStdoutCli3(t, func() {
			if err := evalBaselineListCmd.RunE(evalBaselineListCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"runs_per_cell": 3`) {
			t.Errorf("json listing wrong: %q", out)
		}
	})
}

// ─── show ───────────────────────────────────────────────────────────────

func TestEvalBaselineShowCmd(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		covHomeDir(t)
		err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), `baseline "ghost" not found`) {
			t.Fatalf("expected not-found, got %v", err)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		covHomeDir(t)
		err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"bad name!"})
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("expected invalid-name error, got %v", err)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		home := covHomeDir(t)
		dir := filepath.Join(home, ".crewship", "eval-baselines")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{nope"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"bad"})
		if err == nil || !strings.Contains(err.Error(), "parse baseline") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("table view", func(t *testing.T) {
		home := covHomeDir(t)
		saveCLIState(t)
		cliCfg = nil
		covWriteBaseline(t, home, baselineRecord{
			Name: "main", GeneratedAt: "2026-06-01T00:00:00Z", WorkspaceID: covWSCli3,
			Scenarios: []string{"eval-a"}, Tiers: []string{"fast", ""}, RunsPerCell: 2,
			Cells: map[string]baselineCell{
				matrixKey("eval-a", "fast"): {Pass: 2, Total: 2, AvgCost: 0.0042},
				matrixKey("eval-a", ""):     {Pass: 1, Total: 2, AvgCost: 0.001},
			},
		})
		out := covCaptureStdoutCli3(t, func() {
			if err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"main"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		for _, want := range []string{"Baseline:    main", "eval-a", "2/2 $0.0042", "(authored)"} {
			if !strings.Contains(out, want) {
				t.Errorf("show output missing %q: %q", want, out)
			}
		}
	})

	t.Run("json view", func(t *testing.T) {
		home := covHomeDir(t)
		covStub(t)
		cliCfg.Format = "json"
		covWriteBaseline(t, home, baselineRecord{Name: "jmain", RunsPerCell: 7})
		out := covCaptureStdoutCli3(t, func() {
			if err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"jmain"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"runs_per_cell": 7`) {
			t.Errorf("json show wrong: %q", out)
		}
	})
}

// ─── delete ─────────────────────────────────────────────────────────────

func TestEvalBaselineDeleteCmd(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		home := covHomeDir(t)
		path := covWriteBaseline(t, home, baselineRecord{Name: "gone"})
		out := covCaptureStdoutCli3(t, func() {
			if err := evalBaselineDeleteCmd.RunE(evalBaselineDeleteCmd, []string{"gone"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `Deleted baseline "gone"`) {
			t.Errorf("delete message missing: %q", out)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("baseline file should be removed; stat err=%v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		covHomeDir(t)
		err := evalBaselineDeleteCmd.RunE(evalBaselineDeleteCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), `baseline "ghost" not found`) {
			t.Fatalf("expected not-found, got %v", err)
		}
	})
}

// ─── save (drives executeBaselineSweep) ─────────────────────────────────

func TestEvalBaselineSaveCmd_HappyPath(t *testing.T) {
	home := covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineSaveCmd)
	runPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/eval-a/run"
	stub.OnPost(runPath, clitest.JSONResponse(200, map[string]any{
		"run_id": "r1", "status": "COMPLETED", "duration_ms": 120, "cost_usd": 0.002,
	}))

	covSetFlags(t, evalBaselineSaveCmd, map[string]string{
		"scenarios": "eval-a", "tiers": "fast", "runs": "2", "inputs": `{"q":"x"}`,
	})
	if err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"main"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// Two runs fired, each carrying the inputs + tier override.
	calls := stub.CallsFor("POST", runPath)
	if len(calls) != 2 {
		t.Fatalf("expected 2 run POSTs, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["tier_override"] != "fast" {
		t.Errorf("tier_override missing: %v", body)
	}
	inputs, _ := body["inputs"].(map[string]any)
	if inputs["q"] != "x" {
		t.Errorf("inputs not forwarded: %v", body)
	}

	// Baseline persisted with the aggregated cell.
	data, err := os.ReadFile(filepath.Join(home, ".crewship", "eval-baselines", "main.json"))
	if err != nil {
		t.Fatalf("read saved baseline: %v", err)
	}
	var rec baselineRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse saved baseline: %v", err)
	}
	if rec.Name != "main" || rec.WorkspaceID != covWSCli3 || rec.RunsPerCell != 2 {
		t.Errorf("record meta wrong: %+v", rec)
	}
	cell := rec.Cells[matrixKey("eval-a", "fast")]
	if cell.Pass != 2 || cell.Total != 2 {
		t.Errorf("cell wrong: %+v", cell)
	}
}

func TestEvalBaselineSaveCmd_SweepValidation(t *testing.T) {
	t.Run("runs must be >= 1", func(t *testing.T) {
		covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineSaveCmd)
		covSetFlags(t, evalBaselineSaveCmd, map[string]string{"scenarios": "eval-a", "runs": "0"})
		err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"main"})
		if err == nil || !strings.Contains(err.Error(), "--runs must be >= 1") {
			t.Fatalf("expected runs error, got %v", err)
		}
	})

	t.Run("bad inputs JSON", func(t *testing.T) {
		covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineSaveCmd)
		covSetFlags(t, evalBaselineSaveCmd, map[string]string{
			"scenarios": "eval-a", "runs": "1", "inputs": "{bad",
		})
		err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"main"})
		if err == nil || !strings.Contains(err.Error(), "parse --inputs JSON") {
			t.Fatalf("expected inputs error, got %v", err)
		}
	})

	t.Run("no scenarios resolved", func(t *testing.T) {
		covHomeDir(t)
		stub := covStub(t)
		covResetFlags(t, evalBaselineSaveCmd)
		// No --scenarios → lists workspace routines; none start with eval-.
		stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/pipelines",
			clitest.JSONResponse(200, []map[string]any{{"slug": "regular-routine"}}))
		covSetFlags(t, evalBaselineSaveCmd, map[string]string{"runs": "1"})
		err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"main"})
		if err == nil || !strings.Contains(err.Error(), "no scenarios resolved") {
			t.Fatalf("expected no-scenarios error, got %v", err)
		}
	})

	t.Run("invalid baseline name", func(t *testing.T) {
		covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineSaveCmd)
		err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"bad/name"})
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("expected invalid-name error, got %v", err)
		}
	})
}

// ─── diff ───────────────────────────────────────────────────────────────

func covSeedDiffBaseline(t *testing.T, home string, pass, total int) {
	t.Helper()
	covWriteBaseline(t, home, baselineRecord{
		Name: "main", GeneratedAt: "2026-06-01T00:00:00Z", WorkspaceID: covWSCli3,
		Scenarios: []string{"eval-a"}, Tiers: []string{"fast"}, RunsPerCell: total,
		Cells: map[string]baselineCell{
			matrixKey("eval-a", "fast"): {Pass: pass, Total: total, AvgCost: 0.002},
		},
	})
}

func TestEvalBaselineDiffCmd_NotFound(t *testing.T) {
	covHomeDir(t)
	covStub(t)
	covResetFlags(t, evalBaselineDiffCmd)
	err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), `baseline "ghost" not found`) {
		t.Fatalf("expected not-found, got %v", err)
	}
}

func TestEvalBaselineDiffCmd_WorkspaceMismatch(t *testing.T) {
	home := covHomeDir(t)
	covStub(t)
	covResetFlags(t, evalBaselineDiffCmd)
	covWriteBaseline(t, home, baselineRecord{
		Name: "main", WorkspaceID: "cother000000000000000000",
		Scenarios: []string{"eval-a"}, Tiers: []string{"fast"},
	})
	err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"})
	if err == nil || !strings.Contains(err.Error(), "diff aborted") {
		t.Fatalf("expected workspace mismatch error, got %v", err)
	}
}

func TestEvalBaselineDiffCmd_StableTable(t *testing.T) {
	home := covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineDiffCmd)
	covSeedDiffBaseline(t, home, 1, 1)
	stub.OnPost("/api/v1/workspaces/"+covWSCli3+"/pipelines/eval-a/run",
		clitest.JSONResponse(200, map[string]any{"run_id": "r1", "status": "COMPLETED"}))

	covSetFlags(t, evalBaselineDiffCmd, map[string]string{
		"scenarios": "eval-a", "tiers": "fast", "runs": "1",
	})
	out := covCaptureStdoutCli3(t, func() {
		if err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "STABLE") {
		t.Errorf("expected STABLE verdict in table: %q", out)
	}
	if !strings.Contains(out, "Baseline:    main") {
		t.Errorf("table header missing: %q", out)
	}
}

func TestEvalBaselineDiffCmd_RegressionFailsCommand(t *testing.T) {
	home := covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineDiffCmd)
	covSeedDiffBaseline(t, home, 1, 1) // baseline 100% pass
	// Current run fails → 0% pass → regression beyond default 0.10 tolerance.
	stub.OnPost("/api/v1/workspaces/"+covWSCli3+"/pipelines/eval-a/run",
		clitest.JSONResponse(200, map[string]any{"run_id": "r2", "status": "FAILED", "error_message": "step exploded"}))

	covSetFlags(t, evalBaselineDiffCmd, map[string]string{
		"scenarios": "eval-a", "tiers": "fast", "runs": "1",
	})
	var err error
	out := covCaptureStdoutCli3(t, func() {
		err = evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"})
	})
	if err == nil || !strings.Contains(err.Error(), "regressed beyond tolerance") {
		t.Fatalf("expected regression error, got %v", err)
	}
	if !strings.Contains(out, "REGRESSION") {
		t.Errorf("expected REGRESSION verdict: %q", out)
	}
}

func TestEvalBaselineDiffCmd_NewAndRemovedJSON(t *testing.T) {
	home := covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineDiffCmd)
	cliCfg.Format = "json"
	// Baseline knows only eval-old; current run targets only eval-new.
	covWriteBaseline(t, home, baselineRecord{
		Name: "main", WorkspaceID: covWSCli3,
		Scenarios: []string{"eval-old"}, Tiers: []string{"fast"},
		Cells: map[string]baselineCell{
			matrixKey("eval-old", "fast"): {Pass: 1, Total: 1},
		},
	})
	stub.OnPost("/api/v1/workspaces/"+covWSCli3+"/pipelines/eval-new/run",
		clitest.JSONResponse(200, map[string]any{"run_id": "r3", "status": "COMPLETED"}))

	covSetFlags(t, evalBaselineDiffCmd, map[string]string{
		"scenarios": "eval-new", "tiers": "fast", "runs": "1",
	})
	var err error
	out := covCaptureStdoutCli3(t, func() {
		err = evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"})
	})
	if err != nil {
		t.Fatalf("NEW/REMOVED must not fail the diff: %v", err)
	}
	if !strings.Contains(out, `"NEW"`) || !strings.Contains(out, `"REMOVED"`) {
		t.Errorf("expected NEW + REMOVED verdicts in json: %q", out)
	}
	if !strings.Contains(out, `"regression_count": 0`) {
		t.Errorf("expected zero regressions: %q", out)
	}
}

// printRegressionTable's NEW/REMOVED dash-rendering, exercised directly
// since the command path above uses json format.
func TestPrintRegressionTable_DashesForNewAndRemoved(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	rows := []regressionRow{
		{Scenario: "eval-n", Tier: "", Verdict: "NEW", CurrentPass: 1, CurrentTotal: 1, CurrentRate: 1},
		{Scenario: "eval-r", Tier: "fast", Verdict: "REMOVED", BaselinePass: 1, BaselineTotal: 1, BaselineRate: 1},
	}
	c := evalBaselineDiffCmd
	out := covCaptureStdoutCli3(t, func() {
		printRegressionTable(c, baselineRecord{Name: "main", GeneratedAt: "g"}, rows, 0.1)
	})
	if !strings.Contains(out, "NEW") || !strings.Contains(out, "REMOVED") {
		t.Errorf("verdicts missing: %q", out)
	}
	if !strings.Contains(out, "(authored)") {
		t.Errorf("empty tier should render as (authored): %q", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("dash placeholders missing: %q", out)
	}
}
