package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestWorkflowCreate_HelpDoesNotPanic is the regression pin for the
// shorthand-collision panic surfaced in bug-hunt iter 5. Pre-fix,
// `workflowCreateCmd.Flags().StringP("file", "f", "", …)` collided
// with `rootCmd.PersistentFlags().StringVarP(&flagFormat, "format",
// "f", …)` and cobra panicked at any --help invocation under
// `workflow create`. The fix dropped the `-f` shorthand from the
// child flag. This test re-invokes Help directly so a future
// reintroduction of the shorthand surfaces as a t.Fatal rather than
// a runtime panic in production.
func TestWorkflowCreate_HelpDoesNotPanic(t *testing.T) {
	// Help triggers the cobra flag-merge path that originally
	// panicked. Capture output to avoid noise during tests, and
	// snapshot the prior writers so a later test in this package
	// doesn't inherit our buffer. workflowCreateCmd is package-
	// global cobra state — SetOut/SetErr without restore bleeds
	// across tests (CR finding on PR #585).
	var buf bytes.Buffer
	prevOut := workflowCreateCmd.OutOrStdout()
	prevErr := workflowCreateCmd.ErrOrStderr()
	workflowCreateCmd.SetOut(&buf)
	workflowCreateCmd.SetErr(&buf)
	t.Cleanup(func() {
		workflowCreateCmd.SetOut(prevOut)
		workflowCreateCmd.SetErr(prevErr)
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("workflowCreateCmd.Help() panicked: %v\n"+
				"this means a future change re-introduced a child-level "+
				"shorthand that collides with a rootCmd persistent flag — "+
				"check cmd_workflow.go init() for StringP/BoolP with a "+
				"shorthand letter already owned by main.go", r)
		}
	}()

	if err := workflowCreateCmd.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "--file") {
		t.Errorf("help output missing --file flag (got %d bytes)", len(out))
	}
	// Pin the "no shorthand" choice — if someone reverts to `-f`,
	// this assertion + the panic recovery above both fire.
	if strings.Contains(out, "-f, --file") {
		t.Error("--file flag must not declare a -f shorthand (collides with root --format -f)")
	}
}
