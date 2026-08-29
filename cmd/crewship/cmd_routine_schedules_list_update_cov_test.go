package main

// routineSchedulesCmd's children already have deep coverage in
// cmd_routine_schedules_cov_test.go (list: empty/table/slug-filter/JSON;
// update: every mutually-exclusive combination but pin-version/unpin) and
// cmd_routine_schedules_format_test.go (-f json for now/enable-disable/
// create/delete) — but neither file references the PARENT group var, which
// is why routineSchedulesCmd still shows up in a name-based untested-command
// scan. This file closes that gap, plus the one update combination that
// genuinely has no coverage yet: --pin-version and --unpin together.

import (
	"strings"
	"testing"
)

func TestRoutineSchedulesCmd_HasEveryChild(t *testing.T) {
	have := map[string]bool{}
	for _, c := range routineSchedulesCmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"list", "create", "update", "enable", "disable", "now", "delete"} {
		if !have[want] {
			t.Errorf("routineSchedulesCmd missing subcommand %q", want)
		}
	}
}

func TestRoutineSchedulesUpdateRunE_PinAndUnpinAreExclusive(t *testing.T) {
	covSetupCli5(t)
	if err := routineSchedulesUpdateCmd.Flags().Set("unpin", "true"); err != nil {
		t.Fatal(err)
	}
	if err := routineSchedulesUpdateCmd.Flags().Set("pin-version", "3"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = routineSchedulesUpdateCmd.Flags().Set("unpin", "false")
		_ = routineSchedulesUpdateCmd.Flags().Set("pin-version", "0")
	})
	err := routineSchedulesUpdateCmd.RunE(routineSchedulesUpdateCmd, []string{covSchedID})
	if err == nil || !strings.Contains(err.Error(), "--pin-version and --unpin are mutually exclusive") {
		t.Errorf("want pin/unpin exclusivity error, got %v", err)
	}
}
