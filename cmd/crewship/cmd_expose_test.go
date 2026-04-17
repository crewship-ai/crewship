package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// saveCLIState snapshots the package-level CLI config/flag state used by
// command RunE paths and restores them at test end. These globals are set by
// the root Cobra PersistentPreRun in production; tests manipulate them
// directly to exercise validation paths without touching the network.
func saveCLIState(t *testing.T) {
	t.Helper()
	origCfg := cliCfg
	origServer := flagServer
	origWorkspace := flagWorkspace
	t.Cleanup(func() {
		cliCfg = origCfg
		flagServer = origServer
		flagWorkspace = origWorkspace
	})
}

func TestExposeCmdStructure(t *testing.T) {
	t.Parallel()

	if exposeCmd.Use != "expose" {
		t.Errorf("expose Use: got %q, want %q", exposeCmd.Use, "expose")
	}
	if !strings.Contains(strings.ToLower(exposeCmd.Short), "port") {
		t.Errorf("expose Short should mention port; got %q", exposeCmd.Short)
	}

	have := map[string]bool{}
	for _, sub := range exposeCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "revoke"} {
		if !have[want] {
			t.Errorf("expose missing subcommand %q; have %v", want, have)
		}
	}
}

func TestExposeListFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"crew", "status"} {
		if f := exposeListCmd.Flags().Lookup(name); f == nil {
			t.Errorf("expose list missing --%s flag", name)
		}
	}
}

func TestExposeRevokeFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"crew", "reason"} {
		if f := exposeRevokeCmd.Flags().Lookup(name); f == nil {
			t.Errorf("expose revoke missing --%s flag", name)
		}
	}
}

func TestExposeRevokeArgsValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"zero args", []string{}, true},
		{"one arg", []string{"exp-123"}, false},
		{"two args", []string{"a", "b"}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := exposeRevokeCmd.Args(exposeRevokeCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("args=%v: expected error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("args=%v: expected no error, got %v", tc.args, err)
			}
		})
	}
}

// TestExposeListRunE_NoAuth exercises the requireAuth short-circuit at the top
// of RunE — reached before any network call, so it is safe to run without a
// live server.
func TestExposeListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := exposeListCmd.RunE(exposeListCmd, nil)
	if err == nil {
		t.Fatal("expected 'not logged in' error; got nil")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in' error; got %v", err)
	}
}

func TestExposeListRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := exposeListCmd.RunE(exposeListCmd, nil)
	if err == nil {
		t.Fatal("expected 'no workspace' error; got nil")
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// TestExposeListRunE_CrewRequired exercises the --crew required check.
// This is reached only after auth + workspace pass, and happens before
// the HTTP client is constructed — so no network call is made.
func TestExposeListRunE_CrewRequired(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	// Cobra flags persist across RunE calls; reset to guarantee empty value.
	if err := exposeListCmd.Flags().Set("crew", ""); err != nil {
		t.Fatalf("reset --crew: %v", err)
	}

	err := exposeListCmd.RunE(exposeListCmd, nil)
	if err == nil {
		t.Fatal("expected '--crew is required' error; got nil")
	}
	if !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("expected '--crew is required'; got %v", err)
	}
}

func TestExposeRevokeRunE_CrewRequired(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	if err := exposeRevokeCmd.Flags().Set("crew", ""); err != nil {
		t.Fatalf("reset --crew: %v", err)
	}

	err := exposeRevokeCmd.RunE(exposeRevokeCmd, []string{"exp-123"})
	if err == nil {
		t.Fatal("expected '--crew is required' error; got nil")
	}
	if !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("expected '--crew is required'; got %v", err)
	}
}
