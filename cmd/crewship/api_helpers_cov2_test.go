package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// covCmdCase describes one Cobra command invocation for the shared
// guard-rail runners below (auth / workspace / transport failures are
// identical boilerplate across ~25 commands, so they're table-driven).
type covCmdCase struct {
	name  string
	cmd   *cobra.Command
	args  []string
	flags map[string]string // optional flags to set before RunE
}

// covRunNoAuth asserts each command refuses to run without a token.
func covRunNoAuth(t *testing.T, cases []covCmdCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			covResetFlags(t, tc.cmd)
			cliCfg = &cli.CLIConfig{}
			covSetFlags(t, tc.cmd, tc.flags)
			err := tc.cmd.RunE(tc.cmd, tc.args)
			if err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Fatalf("expected not-logged-in error, got %v", err)
			}
		})
	}
}

// covRunNoWorkspace asserts each command refuses to run without a
// resolved workspace.
func covRunNoWorkspace(t *testing.T, cases []covCmdCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			covResetFlags(t, tc.cmd)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tk"}
			covSetFlags(t, tc.cmd, tc.flags)
			err := tc.cmd.RunE(tc.cmd, tc.args)
			if err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("expected workspace error, got %v", err)
			}
		})
	}
}

// covRunTransportError points each command at an unreachable server
// (closed local port — no real traffic) and asserts the transport
// failure surfaces as an error rather than a panic or silent success.
func covRunTransportError(t *testing.T, cases []covCmdCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			covResetFlags(t, tc.cmd)
			t.Setenv("CREWSHIP_SERVER", "")
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagServer = ""
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tk", Workspace: covWSCli3, Server: "http://127.0.0.1:1"}
			covSetFlags(t, tc.cmd, tc.flags)
			if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil {
				t.Fatal("expected transport error, got nil")
			}
		})
	}
}

// postJSON's transport-error branch (the one path api_helpers_cov_test.go
// didn't reach).
func TestPostJSON_TransportError(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	cliCfg = &cli.CLIConfig{Token: "tk", Workspace: covWSCli3, Server: "http://127.0.0.1:1"}
	if err := postJSON(newAPIClient(), "/api/v1/x", map[string]string{"a": "b"}, nil); err == nil {
		t.Fatal("expected transport error, got nil")
	}
}
