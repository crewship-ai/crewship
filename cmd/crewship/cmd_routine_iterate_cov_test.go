package main

// Coverage for `routine iterate` (cmd_routine_iterate.go, routineIterateCmd).
//
// cmd_routine_iterate_test.go already unit-tests the pure helpers
// (parseGraderScore, extractDefinitionJSON, validateNoNewCapabilities, ...)
// but never invokes routineIterateCmd/runRoutineIterate itself, which is why
// its name is absent from the suite despite the helpers being well covered.
// This file closes that: the command-level argument contract (every
// required flag, the rounds/target bounds) plus one test that drives
// runRoutineIterate far enough to prove auth, flag parsing, rubric handling
// and crew/agent resolution all wire together correctly before the first
// network call that would need a live WS/agent stack.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// resetIterateFlags restores every routineIterateCmd flag to its default
// after a test — these are package-level cobra flags shared across the
// whole suite.
func resetIterateFlags(t *testing.T) {
	t.Helper()
	defaults := map[string]string{
		"rounds": "3", "target": "90", "inputs": "", "rubric": "",
		"grader": "", "optimizer": "", "author-crew": "", "yes": "false",
	}
	for name, def := range defaults {
		def := def
		name := name
		t.Cleanup(func() { _ = routineIterateCmd.Flags().Set(name, def) })
	}
}

func setIterateFlags(t *testing.T, kv map[string]string) {
	t.Helper()
	resetIterateFlags(t)
	for k, v := range kv {
		if err := routineIterateCmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s=%q: %v", k, v, err)
		}
	}
}

func TestRoutineIterateRunE_RequiresRubric(t *testing.T) {
	covSetupCli5(t)
	setIterateFlags(t, map[string]string{"grader": "reviewer", "author-crew": "eng"})
	err := runRoutineIterate(routineIterateCmd, []string{"summarize"})
	if err == nil || !strings.Contains(err.Error(), "--rubric required") {
		t.Errorf("want rubric-required error, got %v", err)
	}
}

func TestRoutineIterateRunE_RequiresGrader(t *testing.T) {
	covSetupCli5(t)
	setIterateFlags(t, map[string]string{"rubric": "be concise", "author-crew": "eng"})
	err := runRoutineIterate(routineIterateCmd, []string{"summarize"})
	if err == nil || !strings.Contains(err.Error(), "--grader required") {
		t.Errorf("want grader-required error, got %v", err)
	}
}

func TestRoutineIterateRunE_RequiresAuthorCrew(t *testing.T) {
	covSetupCli5(t)
	setIterateFlags(t, map[string]string{"rubric": "be concise", "grader": "reviewer"})
	err := runRoutineIterate(routineIterateCmd, []string{"summarize"})
	if err == nil || !strings.Contains(err.Error(), "--author-crew required") {
		t.Errorf("want author-crew-required error, got %v", err)
	}
}

func TestRoutineIterateRunE_RoundsOutOfRange(t *testing.T) {
	for _, rounds := range []string{"0", "11"} {
		t.Run(rounds, func(t *testing.T) {
			covSetupCli5(t)
			setIterateFlags(t, map[string]string{
				"rubric": "be concise", "grader": "reviewer", "author-crew": "eng", "rounds": rounds,
			})
			err := runRoutineIterate(routineIterateCmd, []string{"summarize"})
			if err == nil || !strings.Contains(err.Error(), "--rounds must be 1-10") {
				t.Errorf("rounds=%s: want range error, got %v", rounds, err)
			}
		})
	}
}

func TestRoutineIterateRunE_TargetOutOfRange(t *testing.T) {
	for _, target := range []string{"0", "101"} {
		t.Run(target, func(t *testing.T) {
			covSetupCli5(t)
			setIterateFlags(t, map[string]string{
				"rubric": "be concise", "grader": "reviewer", "author-crew": "eng", "target": target,
			})
			err := runRoutineIterate(routineIterateCmd, []string{"summarize"})
			if err == nil || !strings.Contains(err.Error(), "--target must be 1-100") {
				t.Errorf("target=%s: want range error, got %v", target, err)
			}
		})
	}
}

func TestRoutineIterateRunE_RubricLooksLikeAPathButUnreadable(t *testing.T) {
	covSetupCli5(t)
	setIterateFlags(t, map[string]string{
		"rubric": "./missing-rubric.md", "grader": "reviewer", "author-crew": "eng",
	})
	err := runRoutineIterate(routineIterateCmd, []string{"summarize"})
	if err == nil || !strings.Contains(err.Error(), "looks like a file path but cannot be read") {
		t.Errorf("want unreadable-path error, got %v", err)
	}
}

// Drives runRoutineIterate through auth, flag validation, rubric handling,
// and author-crew/grader resolution (both CUID-shaped, so clitest's
// verify-GET fallback answers "exists" without explicit stubs) up to the
// first real network call it cannot make offline: running the routine. That
// call is stubbed to fail, which both proves everything upstream wired
// together AND that the loop's round-1 error is reported with round context.
func TestRoutineIterateRunE_ReachesFirstRunRound(t *testing.T) {
	const authorCrewID = "ccrew0123456789abcdefghi"
	const graderID = "cagent0123456789abcdefgh"
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": graderID, "slug": "reviewer"},
	}))
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/summarize/run",
		clitest.ErrorResponse(503, "sidecar not ready"))

	setIterateFlags(t, map[string]string{
		"rubric": "be concise", "grader": graderID, "author-crew": authorCrewID,
	})

	var runErr error
	covCaptureStdoutCli5(t, func() {
		runErr = runRoutineIterate(routineIterateCmd, []string{"summarize"})
	})
	if runErr == nil {
		t.Fatal("want an error once the run call fails")
	}
	if !strings.Contains(runErr.Error(), "round 1 run") {
		t.Errorf("error should carry round context, got: %v", runErr)
	}
	if !strings.Contains(runErr.Error(), "sidecar not ready") {
		t.Errorf("error should surface the server's message, got: %v", runErr)
	}
	// Confirms the run call actually reached the routine's run endpoint with
	// the resolved workspace — i.e. auth/workspace/flags/rubric/resolve all
	// ran without touching it.
	calls := stub.CallsFor("POST", "/api/v1/workspaces/"+covWSCli5+"/pipelines/summarize/run")
	if len(calls) != 1 {
		t.Fatalf("want 1 run call, got %d", len(calls))
	}
}
