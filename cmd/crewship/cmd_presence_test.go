package main

import (
	"testing"
)

func TestPresenceCmdStructure(t *testing.T) {
	t.Parallel()

	if presenceCmd.Use != "presence" {
		t.Errorf("presence Use: got %q want %q", presenceCmd.Use, "presence")
	}
	have := map[string]bool{}
	for _, sub := range presenceCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["roster"] {
		t.Errorf("presence missing 'roster' subcommand; have %v", have)
	}
}

func TestPresenceRosterFlags(t *testing.T) {
	t.Parallel()

	if presenceRosterCmd.Flags().Lookup("crew") == nil {
		t.Error("presence roster missing --crew flag")
	}
}
