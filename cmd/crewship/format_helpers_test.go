package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// newJSONFlagCmd builds a bare command carrying the legacy --json bool,
// mirroring the commands that predate the global --format flag.
func newJSONFlagCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "x", Run: func(*cobra.Command, []string) {}}
	cmd.Flags().Bool("json", false, "")
	return cmd
}

func TestResolvedFormat_GlobalFormatFlagWins(t *testing.T) {
	oldFormat, oldCfg := flagFormat, cliCfg
	defer func() { flagFormat, cliCfg = oldFormat, oldCfg }()

	flagFormat = "ndjson"
	cliCfg = &cli.CLIConfig{}

	if got := resolvedFormat(newJSONFlagCmd()); got != "ndjson" {
		t.Errorf("resolvedFormat = %q, want ndjson from global flag", got)
	}
}

func TestResolvedFormat_LegacyJSONBoolFoldsToJSON(t *testing.T) {
	oldFormat, oldCfg := flagFormat, cliCfg
	defer func() { flagFormat, cliCfg = oldFormat, oldCfg }()

	flagFormat = ""
	cliCfg = &cli.CLIConfig{}

	cmd := newJSONFlagCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if got := resolvedFormat(cmd); got != "json" {
		t.Errorf("resolvedFormat = %q, want json from legacy --json", got)
	}
}

func TestResolvedFormat_DefaultsToTable(t *testing.T) {
	oldFormat, oldCfg := flagFormat, cliCfg
	defer func() { flagFormat, cliCfg = oldFormat, oldCfg }()

	flagFormat = ""
	cliCfg = &cli.CLIConfig{}

	// Works for commands with and without the legacy flag.
	if got := resolvedFormat(newJSONFlagCmd()); got != "table" {
		t.Errorf("resolvedFormat = %q, want table default", got)
	}
	bare := &cobra.Command{Use: "y", Run: func(*cobra.Command, []string) {}}
	if got := resolvedFormat(bare); got != "table" {
		t.Errorf("resolvedFormat(no --json flag) = %q, want table", got)
	}
}

func TestResolvedFormat_ConfigFormatRespected(t *testing.T) {
	oldFormat, oldCfg := flagFormat, cliCfg
	defer func() { flagFormat, cliCfg = oldFormat, oldCfg }()

	flagFormat = ""
	cliCfg = &cli.CLIConfig{Format: "yaml"}

	if got := resolvedFormat(newJSONFlagCmd()); got != "yaml" {
		t.Errorf("resolvedFormat = %q, want yaml from config", got)
	}
}
