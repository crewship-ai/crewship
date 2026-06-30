package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// governancePath builds the workspace-scoped governance route the CLI
// posts to for a given slug + verb.
func governancePath(slug, action string) string {
	return fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/%s", covWorkspaceID, slug, action)
}

// TestRoutineGovernanceRunE drives the four maker-checker/airbag verbs
// (approve/reject/disable/enable) through the real cobra commands against
// a clitest stub, asserting each hits POST on the correct path and decodes
// the response. The disable case additionally asserts cancelled_runs is
// parsed and surfaced.
func TestRoutineGovernanceRunE(t *testing.T) {
	cases := []struct {
		name     string
		cmd      *cobra.Command
		action   string
		slug     string
		respBody map[string]any
		wantOut  []string
	}{
		{
			name:     "approve",
			cmd:      routineApproveCmd,
			action:   "approve",
			slug:     "email-fetch",
			respBody: map[string]any{"status": "live"},
			wantOut:  []string{"Routine email-fetch → live"},
		},
		{
			name:     "reject",
			cmd:      routineRejectCmd,
			action:   "reject",
			slug:     "email-fetch",
			respBody: map[string]any{"status": "rejected"},
			wantOut:  []string{"Routine email-fetch → rejected"},
		},
		{
			name:     "disable surfaces cancelled_runs",
			cmd:      routineDisableCmd,
			action:   "disable",
			slug:     "nightly-sweep",
			respBody: map[string]any{"status": "disabled", "cancelled_runs": 3},
			wantOut:  []string{"Routine nightly-sweep → disabled", "cancelled 3 in-flight run(s)"},
		},
		{
			name:     "enable",
			cmd:      routineEnableCmd,
			action:   "enable",
			slug:     "nightly-sweep",
			respBody: map[string]any{"status": "enabled"},
			wantOut:  []string{"Routine nightly-sweep → enabled"},
		},
		{
			// status omitted by server → CLI falls back to the action verb.
			name:     "status fallback to action",
			cmd:      routineApproveCmd,
			action:   "approve",
			slug:     "no-status",
			respBody: map[string]any{},
			wantOut:  []string{"Routine no-status → approve"},
		},
		{
			// cancelled_runs == 0 must NOT print the cancellation line.
			name:     "disable with zero cancelled runs is silent on count",
			cmd:      routineDisableCmd,
			action:   "disable",
			slug:     "idle-routine",
			respBody: map[string]any{"status": "disabled", "cancelled_runs": 0},
			wantOut:  []string{"Routine idle-routine → disabled"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			setupStubCLICov(t, stub)
			path := governancePath(tc.slug, tc.action)
			stub.OnPost(path, clitest.JSONResponse(200, tc.respBody))

			out, err := captureStdoutCov(t, func() error {
				return tc.cmd.RunE(tc.cmd, []string{tc.slug})
			})
			if err != nil {
				t.Fatalf("RunE: %v", err)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("stdout missing %q; got:\n%s", want, out)
				}
			}
			if tc.name == "disable with zero cancelled runs is silent on count" &&
				strings.Contains(out, "cancelled") {
				t.Errorf("must not print cancellation line when cancelled_runs==0; got:\n%s", out)
			}

			calls := stub.CallsFor("POST", path)
			if len(calls) != 1 {
				t.Fatalf("expected exactly one POST to %s, got %d", path, len(calls))
			}
		})
	}
}

// TestRoutineGovernanceRunE_ServerError pins the error path: a non-2xx
// response from the governance endpoint is surfaced to the caller (this is
// the 409 "awaiting approval"/"disabled" + 403 role-gate behaviour the CLI
// relays via cli.CheckError).
func TestRoutineGovernanceRunE_ServerError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	path := governancePath("email-fetch", "approve")
	stub.OnPost(path, clitest.ErrorResponse(403, "requires MANAGER role"))

	err := routineApproveCmd.RunE(routineApproveCmd, []string{"email-fetch"})
	if err == nil || !strings.Contains(err.Error(), "requires MANAGER role") {
		t.Fatalf("want 403 surfaced, got %v", err)
	}
}

// TestRoutineGovernanceRunE_DecodeError covers the malformed-response branch.
func TestRoutineGovernanceRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	path := governancePath("email-fetch", "disable")
	stub.OnPost(path, clitest.TextResponse(200, "not json"))

	err := routineDisableCmd.RunE(routineDisableCmd, []string{"email-fetch"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("want decode error, got %v", err)
	}
}

// TestRoutineGovernanceRunE_AuthGates pins requireAuth/requireWorkspace on
// each governance verb.
func TestRoutineGovernanceRunE_AuthGates(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"approve", routineApproveCmd},
		{"reject", routineRejectCmd},
		{"disable", routineDisableCmd},
		{"enable", routineEnableCmd},
	}
	for _, tc := range cases {
		t.Run(tc.name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.cmd.RunE(tc.cmd, []string{"s"}); err == nil ||
				!strings.Contains(err.Error(), "not logged in") {
				t.Fatalf("want not logged in, got %v", err)
			}
		})
		t.Run(tc.name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{Token: "fake-token"}
			flagWorkspace = ""
			t.Setenv("CREWSHIP_WORKSPACE", "")
			if err := tc.cmd.RunE(tc.cmd, []string{"s"}); err == nil ||
				!strings.Contains(err.Error(), "workspace") {
				t.Fatalf("want workspace error, got %v", err)
			}
		})
	}
}
