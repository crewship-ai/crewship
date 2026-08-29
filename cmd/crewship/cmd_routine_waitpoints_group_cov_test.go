package main

// routineWaitpointsCmd's children (list/show/approve/reject) already have
// deep coverage in cmd_routine_waitpoints_cov_test.go — table/JSON/empty/
// error paths for list, found/not-found for show, POST body assertions for
// approve/reject, and decideWaitpoint's own auth/workspace/server-error
// branches. That file never references the PARENT group var itself, which
// is why routineWaitpointsCmd still shows up in a name-based untested-
// command scan. This file closes just that gap.

import "testing"

func TestRoutineWaitpointsCmd_HasChildren(t *testing.T) {
	have := map[string]bool{}
	for _, c := range routineWaitpointsCmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"list", "show", "approve", "reject"} {
		if !have[want] {
			t.Errorf("routineWaitpointsCmd missing subcommand %q", want)
		}
	}
}
