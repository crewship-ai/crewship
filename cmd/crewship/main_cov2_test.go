package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain_RunsVersionCommand drives main() end-to-end with os.Args
// pointed at the purely-local `version` subcommand: slash commands get
// registered (empty temp HOME → none), rootCmd.Execute parses argv and
// dispatches, and main returns without hitting the os.Exit error path.
func TestMain_RunsVersionCommand(t *testing.T) {
	saveCLIState(t)
	t.Setenv("HOME", t.TempDir()) // no user slash commands
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))

	origArgs := os.Args
	os.Args = []string{"crewship", "version"}
	t.Cleanup(func() { os.Args = origArgs })

	out, err := captureStdout(t, func() error {
		main()
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "crewship "+version) {
		t.Errorf("version output: got %q", out)
	}
	if !strings.Contains(out, "os/arch:") {
		t.Errorf("version output missing os/arch line: %q", out)
	}
}
