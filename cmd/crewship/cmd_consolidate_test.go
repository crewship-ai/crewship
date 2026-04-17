package main

import (
	"strings"
	"testing"
)

func TestConsolidateCmdStructure(t *testing.T) {
	t.Parallel()

	if consolidateCmd.Use != "consolidate" {
		t.Errorf("consolidate Use: got %q want %q", consolidateCmd.Use, "consolidate")
	}
	if !strings.Contains(strings.ToLower(consolidateCmd.Long), "not yet") &&
		!strings.Contains(strings.ToLower(consolidateCmd.Long), "stub") {
		t.Errorf("consolidate Long should document stub status; got %q", consolidateCmd.Long)
	}
	have := map[string]bool{}
	for _, sub := range consolidateCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["run"] {
		t.Errorf("consolidate missing 'run' subcommand; have %v", have)
	}
}

func TestConsolidateRunRunE_Stub(t *testing.T) {
	t.Parallel()

	err := consolidateRunCmd.RunE(consolidateRunCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("expected stub error; got %v", err)
	}
}

func TestConsolidateRunFlags(t *testing.T) {
	t.Parallel()

	if consolidateRunCmd.Flags().Lookup("crew") == nil {
		t.Error("consolidate run missing --crew flag")
	}
}
