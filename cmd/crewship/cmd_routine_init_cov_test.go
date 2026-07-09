package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// TestRoutineInit_DefaultSkeletonValidates is the core contract: the
// zero-arg scaffold must be a valid DSL that `routine validate` accepts.
func TestRoutineInit_DefaultSkeletonValidates(t *testing.T) {
	dsl, err := pipeline.Parse([]byte(routineSkeleton))
	if err != nil {
		t.Fatalf("skeleton does not parse: %v", err)
	}
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
		t.Fatalf("skeleton does not validate: %v", err)
	}
}

// TestRoutineInit_ScriptSkeletonValidates: the --script scaffold must be a
// valid DSL AND actually contain a first-class script step (the whole point).
func TestRoutineInit_ScriptSkeletonValidates(t *testing.T) {
	dsl, err := pipeline.Parse([]byte(routineScriptSkeleton))
	if err != nil {
		t.Fatalf("script skeleton does not parse: %v", err)
	}
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
		t.Fatalf("script skeleton does not validate: %v", err)
	}
	var hasScript bool
	for _, s := range dsl.Steps {
		if s.Type == pipeline.StepScript && s.Script != nil && s.Script.Path != "" {
			hasScript = true
		}
	}
	if !hasScript {
		t.Fatal("--script skeleton has no script step with a path")
	}
}

// TestRoutineInit_ScriptFlagEmitsScriptSkeleton: `routine init --script`
// writes the script-backed scaffold, and validate accepts it.
func TestRoutineInit_ScriptFlagEmitsScriptSkeleton(t *testing.T) {
	covSaveState(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "s.json")
	covSetFlagCli9(t, routineInitCmd, "script", "true")
	covSetFlagCli9(t, routineInitCmd, "output", out)
	if err := routineInitCmd.RunE(routineInitCmd, nil); err != nil {
		t.Fatalf("init --script: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type": "script"`) {
		t.Fatalf("expected a script step in --script output, got: %s", body)
	}
	if err := routineValidateCmd.RunE(routineValidateCmd, []string{out}); err != nil {
		t.Fatalf("validate on --script scaffold failed: %v", err)
	}
}

// TestRoutineInit_DefaultWritesFile checks the -o path writes the
// skeleton and validate passes on the written file.
func TestRoutineInit_DefaultWritesFile(t *testing.T) {
	covSaveState(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "new.json")
	covSetFlagCli9(t, routineInitCmd, "output", out)

	if err := routineInitCmd.RunE(routineInitCmd, nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := routineValidateCmd.RunE(routineValidateCmd, []string{out}); err != nil {
		t.Fatalf("validate on scaffolded file failed: %v", err)
	}
}

// TestRoutineInit_FromClonesAndValidates is the issue's acceptance:
// `routine init --from X -o new.json && routine validate new.json` passes.
func TestRoutineInit_FromClonesAndValidates(t *testing.T) {
	s := covStubCli9(t)
	// The export bundle carries the DSL under pipeline.definition; init
	// must extract exactly that (not the whole bundle).
	def := json.RawMessage(routineSkeleton)
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines/summarize-text/export",
		clitest.JSONResponse(200, map[string]any{
			"format":   "crewship-pipeline-bundle/v1",
			"pipeline": map[string]any{"slug": "summarize-text", "definition": def},
		}))

	dir := t.TempDir()
	out := filepath.Join(dir, "cloned.json")
	covSetFlagCli9(t, routineInitCmd, "from", "summarize-text")
	covSetFlagCli9(t, routineInitCmd, "output", out)

	if err := routineInitCmd.RunE(routineInitCmd, nil); err != nil {
		t.Fatalf("init --from: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The cloned file is a DSL, not a bundle — no bundle wrapper keys.
	if strings.Contains(string(body), "crewship-pipeline-bundle") {
		t.Fatalf("cloned file leaked the export bundle wrapper: %s", body)
	}
	if err := routineValidateCmd.RunE(routineValidateCmd, []string{out}); err != nil {
		t.Fatalf("validate on cloned file failed: %v", err)
	}
}

// TestRoutineInit_FromMissingDefinition errors clearly.
func TestRoutineInit_FromMissingDefinition(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines/empty/export",
		clitest.JSONResponse(200, map[string]any{"pipeline": map[string]any{"slug": "empty"}}))
	covSetFlagCli9(t, routineInitCmd, "from", "empty")
	if err := routineInitCmd.RunE(routineInitCmd, nil); err == nil || !strings.Contains(err.Error(), "no definition") {
		t.Fatalf("expected 'no definition' error, got %v", err)
	}
}

// TestRoutineInit_FromAuthGate: --from needs a logged-in session; the
// default skeleton path does not.
func TestRoutineInit_FromAuthGate(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli9(t, routineInitCmd, "from", "x")
	if err := routineInitCmd.RunE(routineInitCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("--from without auth should fail; got %v", err)
	}
}
