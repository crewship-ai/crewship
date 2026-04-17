package main

import (
	"testing"
)

// Structure-only tests — the RunE handlers require auth + a live server,
// which is exercised by the handler tests in internal/api/hooks_handler_test.go.
// Here we just check the cobra tree is shaped right.

func TestHooksCmdStructure(t *testing.T) {
	t.Parallel()

	if hooksCmd.Use != "hooks" {
		t.Errorf("hooks Use: got %q want %q", hooksCmd.Use, "hooks")
	}
	have := map[string]bool{}
	for _, sub := range hooksCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "enable", "disable"} {
		if !have[want] {
			t.Errorf("hooks missing subcommand %q; have %v", want, have)
		}
	}
}

func TestHooksEnableArgsValidation(t *testing.T) {
	t.Parallel()

	if err := hooksEnableCmd.Args(hooksEnableCmd, []string{}); err == nil {
		t.Error("enable with no args should error")
	}
	if err := hooksDisableCmd.Args(hooksDisableCmd, []string{}); err == nil {
		t.Error("disable with no args should error")
	}
	if err := hooksEnableCmd.Args(hooksEnableCmd, []string{"h-1"}); err != nil {
		t.Errorf("enable with one arg should pass; got %v", err)
	}
}
