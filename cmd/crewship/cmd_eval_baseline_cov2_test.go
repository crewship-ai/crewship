package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestEvalBaselineCmds_NoAuth(t *testing.T) {
	covHomeDir(t)
	covRunNoAuth(t, []covCmdCase{
		{name: "save", cmd: evalBaselineSaveCmd, args: []string{"main"}},
		{name: "diff", cmd: evalBaselineDiffCmd, args: []string{"main"}},
	})
}

func TestEvalBaselineCmds_NoWorkspace(t *testing.T) {
	covHomeDir(t)
	covRunNoWorkspace(t, []covCmdCase{
		{name: "save", cmd: evalBaselineSaveCmd, args: []string{"main"}},
		{name: "diff", cmd: evalBaselineDiffCmd, args: []string{"main"}},
	})
}

func TestMergeUnique_DuplicatesInBothSlices(t *testing.T) {
	got := mergeUnique([]string{"b", "a", "b"}, []string{"a", "c", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeUnique: got %v want %v", got, want)
	}
}

// baselineDir failure: a regular FILE squatting on the directory path
// makes MkdirAll fail — every command that needs the store must surface
// the error rather than wedging.
func TestBaselineDirBlockedByFile(t *testing.T) {
	home := covHomeDir(t)
	if err := os.MkdirAll(filepath.Join(home, ".crewship"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".crewship", "eval-baselines"), []byte("squatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := baselineDir(); err == nil || !strings.Contains(err.Error(), "create baseline dir") {
		t.Fatalf("expected dir error, got %v", err)
	}
	if _, err := baselinePath("main"); err == nil {
		t.Fatal("baselinePath should propagate dir error")
	}
	if err := evalBaselineListCmd.RunE(evalBaselineListCmd, nil); err == nil {
		t.Fatal("list should propagate dir error")
	}
	if err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"main"}); err == nil {
		t.Fatal("show should propagate dir error")
	}
	if err := evalBaselineDeleteCmd.RunE(evalBaselineDeleteCmd, []string{"main"}); err == nil {
		t.Fatal("delete should propagate dir error")
	}
}

func TestEvalBaselineListCmd_SortsAndSkipsUnreadable(t *testing.T) {
	home := covHomeDir(t)
	saveCLIState(t)
	cliCfg = nil
	covWriteBaseline(t, home, baselineRecord{Name: "zeta", RunsPerCell: 1})
	covWriteBaseline(t, home, baselineRecord{Name: "alpha", RunsPerCell: 2})
	// Dangling symlink ending in .json → os.ReadFile error → skipped.
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), filepath.Join(dir, "dangling.json")); err != nil {
		t.Fatal(err)
	}

	out := covCaptureStdoutCli3(t, func() {
		if err := evalBaselineListCmd.RunE(evalBaselineListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	ia, iz := strings.Index(out, "alpha"), strings.Index(out, "zeta")
	if ia < 0 || iz < 0 || ia > iz {
		t.Errorf("expected alpha sorted before zeta: %q", out)
	}
	if strings.Contains(out, "dangling") {
		t.Errorf("unreadable file leaked into listing: %q", out)
	}
}

func TestEvalBaselineShowCmd_ReadErrorNotMissing(t *testing.T) {
	home := covHomeDir(t)
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	if err := os.MkdirAll(filepath.Join(dir, "weird.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := evalBaselineShowCmd.RunE(evalBaselineShowCmd, []string{"weird"})
	if err == nil || !strings.Contains(err.Error(), "read baseline") {
		t.Fatalf("expected read error (not not-found), got %v", err)
	}
}

func TestEvalBaselineDeleteCmd_RemoveErrorNotMissing(t *testing.T) {
	home := covHomeDir(t)
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	// Non-empty directory named <name>.json → os.Remove fails with
	// ENOTEMPTY, which must NOT be reported as "not found".
	if err := os.MkdirAll(filepath.Join(dir, "stuck.json", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := evalBaselineDeleteCmd.RunE(evalBaselineDeleteCmd, []string{"stuck"})
	if err == nil || !strings.Contains(err.Error(), "delete baseline") {
		t.Fatalf("expected delete error, got %v", err)
	}
}

func TestEvalBaselineDiffCmd_ErrorPaths(t *testing.T) {
	t.Run("invalid name", func(t *testing.T) {
		covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineDiffCmd)
		err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"bad name!"})
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("expected invalid-name error, got %v", err)
		}
	})

	t.Run("read error not missing", func(t *testing.T) {
		home := covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineDiffCmd)
		dir := filepath.Join(home, ".crewship", "eval-baselines")
		if err := os.MkdirAll(filepath.Join(dir, "main.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"})
		if err == nil || !strings.Contains(err.Error(), "read baseline") {
			t.Fatalf("expected read error, got %v", err)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		home := covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineDiffCmd)
		dir := filepath.Join(home, ".crewship", "eval-baselines")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte("{nope"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"})
		if err == nil || !strings.Contains(err.Error(), "parse baseline") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("sweep error propagates", func(t *testing.T) {
		home := covHomeDir(t)
		covStub(t)
		covResetFlags(t, evalBaselineDiffCmd)
		covSeedDiffBaseline(t, home, 1, 1)
		covSetFlags(t, evalBaselineDiffCmd, map[string]string{"scenarios": "eval-a", "runs": "0"})
		err := evalBaselineDiffCmd.RunE(evalBaselineDiffCmd, []string{"main"})
		if err == nil || !strings.Contains(err.Error(), "--runs must be >= 1") {
			t.Fatalf("expected sweep error, got %v", err)
		}
	})
}

func TestExecuteBaselineSweep_RoutineListHTTPError(t *testing.T) {
	covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineSaveCmd)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/pipelines",
		clitest.ErrorResponse(500, "router down"))
	covSetFlags(t, evalBaselineSaveCmd, map[string]string{"runs": "1"})
	err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"main"})
	if err == nil || !strings.Contains(err.Error(), "list routines") {
		t.Fatalf("expected list-routines error, got %v", err)
	}
}

func TestEvalBaselineSaveCmd_WriteError(t *testing.T) {
	home := covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineSaveCmd)
	stub.OnPost("/api/v1/workspaces/"+covWSCli3+"/pipelines/eval-a/run",
		clitest.JSONResponse(200, map[string]any{"run_id": "r1", "status": "COMPLETED"}))

	// Read-only store dir → WriteFile fails after the sweep ran.
	dir := filepath.Join(home, ".crewship", "eval-baselines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if os.Getuid() == 0 {
		t.Skip("running as root — read-only dir does not block writes")
	}

	covSetFlags(t, evalBaselineSaveCmd, map[string]string{
		"scenarios": "eval-a", "tiers": "fast", "runs": "1",
	})
	err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{"main"})
	if err == nil || !strings.Contains(err.Error(), "write baseline") {
		t.Fatalf("expected write error, got %v", err)
	}
}
