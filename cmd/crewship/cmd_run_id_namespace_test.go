package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// #1193: `crewship routine runs <slug>` prints RUN_IDs in the routine
// (pipeline) namespace — run_… / prn_… — but diff/inspect/explain read the
// agent-run namespace (msg_…, from `crewship history`). Both namespaces
// 404 with the byte-identical server message "run not found", so following
// the obvious path (copy a RUN_ID, paste it into inspect) dead-ended on a
// bare 404 that reads like a missing row rather than a category error.
//
// These assert the guard fires *before* any HTTP call — no stub server is
// registered, so a regression that lets the request through fails here
// with a connection error rather than passing by accident.
const pipelineRunID = "run_cmrm3xxzk0083de436e64"

func TestRunIDNamespace_RejectedBeforeHTTP(t *testing.T) {
	cases := []struct {
		name string
		args []string
		run  func(args []string) error
		// wantAlt is the command the hint must redirect the user to.
		wantAlt string
	}{
		{
			name:    "inspect",
			args:    []string{pipelineRunID},
			run:     func(a []string) error { return inspectCmd.RunE(inspectCmd, a) },
			wantAlt: "routine logs",
		},
		{
			name:    "explain",
			args:    []string{pipelineRunID},
			run:     func(a []string) error { return explainCmd.RunE(explainCmd, a) },
			wantAlt: "routine report",
		},
		{
			name:    "diff run-a",
			args:    []string{pipelineRunID, "msg_1783864822_0f1e2d3c"},
			run:     func(a []string) error { return diffCmd.RunE(diffCmd, a) },
			wantAlt: "routine report",
		},
		{
			// The second positional must be checked too — diff fetches
			// both sides and reports run-a's error first, so a bad run-b
			// would otherwise stay invisible until run-a was fixed.
			name:    "diff run-b",
			args:    []string{"msg_1783864822_0f1e2d3c", pipelineRunID},
			run:     func(a []string) error { return diffCmd.RunE(diffCmd, a) },
			wantAlt: "routine report",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(tc.args)
			if err == nil {
				t.Fatalf("%s: expected an error for a routine run id, got nil", tc.name)
			}
			msg := err.Error()
			if !strings.Contains(msg, "routine") {
				t.Errorf("%s: error should say the id is a routine run id, got: %v", tc.name, err)
			}
			if !strings.Contains(msg, tc.wantAlt) {
				t.Errorf("%s: error should redirect to %q, got: %v", tc.name, tc.wantAlt, err)
			}
			// The exit-code contract must not drift: these already
			// exit 3 for an unknown run.
			if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
				t.Errorf("%s: ExitCodeFor = %d, want %d (ExitNotFound)", tc.name, code, cli.ExitNotFound)
			}
		})
	}
}
