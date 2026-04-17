package main

import (
	"strings"
	"testing"
)

func TestPresenceCmdStructure(t *testing.T) {
	t.Parallel()

	if presenceCmd.Use != "presence" {
		t.Errorf("presence Use: got %q want %q", presenceCmd.Use, "presence")
	}
	if !strings.Contains(strings.ToLower(presenceCmd.Long), "not yet") &&
		!strings.Contains(strings.ToLower(presenceCmd.Long), "stub") {
		t.Errorf("presence Long should document stub status; got %q", presenceCmd.Long)
	}
	have := map[string]bool{}
	for _, sub := range presenceCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["roster"] {
		t.Errorf("presence missing 'roster' subcommand; have %v", have)
	}
}

func TestPresenceRosterRunE_Stub(t *testing.T) {
	t.Parallel()

	err := presenceRosterCmd.RunE(presenceRosterCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("expected stub error; got %v", err)
	}
}

func TestPresenceRosterFlags(t *testing.T) {
	t.Parallel()

	if presenceRosterCmd.Flags().Lookup("crew") == nil {
		t.Error("presence roster missing --crew flag")
	}
}
