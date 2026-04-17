package main

import (
	"testing"
)

func TestConsolidateCmdStructure(t *testing.T) {
	t.Parallel()

	if consolidateCmd.Use != "consolidate" {
		t.Errorf("consolidate Use: got %q want %q", consolidateCmd.Use, "consolidate")
	}
	have := map[string]bool{}
	for _, sub := range consolidateCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["run"] {
		t.Errorf("consolidate missing 'run' subcommand; have %v", have)
	}
}

func TestConsolidateRunFlags(t *testing.T) {
	t.Parallel()

	if consolidateRunCmd.Flags().Lookup("crew") == nil {
		t.Error("consolidate run missing --crew flag")
	}
	if consolidateRunCmd.Flags().Lookup("since") == nil {
		t.Error("consolidate run missing --since flag")
	}
}
