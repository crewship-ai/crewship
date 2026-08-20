package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/manifest"
)

// A failed apply must never print a line that reads as success.
//
// Apply is fail-fast: the first exec error aborts the loop and every
// plan item behind it is left untouched. The summary printed the
// counters anyway — "Applied: 7 created, 3 updated, 0 unchanged, 0
// deleted." — with the error following on a later line, which is how a
// production run of a file-heavy manifest was read as done when not one
// of its ten crew files had been written. The stale script stayed in
// place and the routine ran with the bug it was being fixed for.
//
// Two properties make that unreadable-as-success:
//
//   - the word "Applied:" is reserved for a run that completed;
//   - the run names what it did NOT do, because "which of the ten
//     landed?" is the only question an operator has at that moment,
//     and the counters cannot answer it.

// applyTwoCrewsManifest yields a multi-item plan: crew, then agent. The
// crew create is stubbed to fail, so the agent behind it is never
// attempted — the shape of the production failure.
const applyTwoCrewsManifest = `
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Cov Crew
  slug: cov-crew
spec:
  agents:
    - slug: cov-agent
      name: Cov Agent
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      prompt: hello
`

func TestRunApply_FailedRunNeverPrintsApplied(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))
	// The 409 the crew-file writer returns against a stopped container —
	// the exact refusal that hid behind a success summary.
	stub.OnPost("/api/v1/crews", clitest.ErrorResponse(409,
		"file is owned by the crew runtime; it can only be overwritten while the crew container is running"))

	path := writeCovManifest(t, applyTwoCrewsManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
	if err == nil {
		t.Fatal("want apply error")
	}
	if strings.Contains(out, "Applied:") {
		t.Errorf("a failed run printed a success summary:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("failed run does not say so:\n%s", out)
	}
}

func TestRunApply_FailedRunNamesWhatWasNotApplied(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))
	stub.OnPost("/api/v1/crews", clitest.ErrorResponse(409, "owned by the crew runtime"))

	path := writeCovManifest(t, applyTwoCrewsManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "yes": "true"})

	out, _ := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })

	if !strings.Contains(out, "NOT APPLIED") {
		t.Fatalf("failed run does not list what it skipped:\n%s", out)
	}
	// The item that blew up, named.
	if !strings.Contains(out, "cov-crew") {
		t.Errorf("the failing item is not named:\n%s", out)
	}
	// And the one behind it that never got a request, named too — the
	// counters alone would have left this invisible.
	if !strings.Contains(out, "cov-agent") {
		t.Errorf("the never-attempted item is not named:\n%s", out)
	}
	if got := len(stub.CallsFor("POST", "/api/v1/agents")); got != 0 {
		t.Errorf("agent create was attempted %d times after a fatal error", got)
	}
}

// printSummary is the single place that decides how a run reads. Drive
// it directly for the cases the stub server cannot reach.
func TestPrintSummary_FailureNeverReadsAsSuccess(t *testing.T) {
	plan := &manifest.Plan{Items: []manifest.PlanItem{
		{Action: manifest.ActionCreate, Kind: "crew", Description: "uctarna"},
	}}

	t.Run("error with partial counts", func(t *testing.T) {
		out, err := covCaptureStdoutCli4(t, func() error {
			printSummary(plan, &manifest.Result{Created: 7, Updated: 3}, errBoom)
			return nil
		})
		if err != nil {
			t.Fatalf("capture: %v", err)
		}
		if strings.Contains(out, "Applied:") {
			t.Errorf("failed run printed Applied:\n%s", out)
		}
		if !strings.Contains(out, "7 created") || !strings.Contains(out, "3 updated") {
			t.Errorf("partial counts lost — they are still the operator's best evidence:\n%s", out)
		}
	})

	t.Run("error before anything ran", func(t *testing.T) {
		out, _ := covCaptureStdoutCli4(t, func() error {
			printSummary(plan, nil, errBoom)
			return nil
		})
		if strings.Contains(out, "Plan:") || strings.Contains(out, "Applied:") {
			t.Errorf("aborted run must not print a plan or applied summary:\n%s", out)
		}
	})

	t.Run("success still says Applied", func(t *testing.T) {
		out, _ := covCaptureStdoutCli4(t, func() error {
			printSummary(plan, &manifest.Result{Created: 1}, nil)
			return nil
		})
		if !strings.Contains(out, "Applied: 1 created") {
			t.Errorf("successful run lost its summary:\n%s", out)
		}
	})
}

// --- rule zero: an apply that would delete something must be stoppable ---
//
// Deleting an agent takes its memory and its Composio OAuth binding with
// it, and the binding is a browser consent no manifest can replay. The
// standing rule for production applies is "every run ends 0 deleted",
// which until now was enforced by a human reading a dry-run plan
// carefully enough. --no-delete makes it the machine's job: the run
// refuses before it mutates anything.

func TestRunApply_NoDeleteRefusesDestructivePlanBeforeMutating(t *testing.T) {
	stub := covSetupCli4(t)
	// An existing crew that the manifest no longer declares → sync mode
	// plans a delete for it.
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli4, "workspace_id": covWorkspaceIDCli4, "name": "Cov Crew", "slug": "cov-crew"},
	}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"file": path, "replace": "true", "yes": "true", "no-delete": "true",
	})

	out, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) })
	if err == nil {
		t.Fatalf("--no-delete let a destructive plan through:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no-delete") {
		t.Errorf("refusal does not name the flag that caused it: %v", err)
	}
	// --yes must not override it: the whole point is a guard that
	// survives the flag people paste into every automated run.
	for _, call := range stub.Calls() {
		if call.Method == "DELETE" {
			t.Errorf("--no-delete issued %s %s", call.Method, call.Path)
		}
	}
}

func TestRunApply_NoDeleteAllowsNonDestructivePlan(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{"file": path, "no-delete": "true", "dry-run": "true"})

	if _, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) }); err != nil {
		t.Fatalf("--no-delete blocked a plan with no deletes: %v", err)
	}
	_ = stub
}

// A dry run with --no-delete has to report the refusal too, or the
// rehearsal disagrees with the performance.
func TestRunApply_NoDeleteFailsDryRunToo(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli4, "workspace_id": covWorkspaceIDCli4, "name": "Cov Crew", "slug": "cov-crew"},
	}))
	stub.SetFallback(clitest.JSONResponse(200, []map[string]any{}))

	path := writeCovManifest(t, covApplyManifest)
	c := covFreshCmd(applyCmd, declareApplyFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"file": path, "replace": "true", "dry-run": "true", "no-delete": "true",
	})

	if _, err := covCaptureStdoutCli4(t, func() error { return runApply(c, nil) }); err == nil {
		t.Error("--no-delete dry run reported success on a destructive plan")
	}
}

var errBoom = errors.New("boom")
