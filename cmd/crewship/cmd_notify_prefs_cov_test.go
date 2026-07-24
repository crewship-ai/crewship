package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covNotifyPrefsSubs mirrors covNotifyChannelSubs: enumerates the `notify
// prefs` subcommands with valid args/flags for the table-driven auth +
// transport sweeps.
func covNotifyPrefsSubs(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli9(t, notifyPrefsSetCmd, "category", "approvals")
	covSetFlagCli9(t, notifyPrefsSetCmd, "channel", "nch_1")
	covSetFlagCli9(t, notifyPrefsSetCmd, "state", "immediate")
	run := func(cmd *cobra.Command, args []string) func() error {
		return func() error { return cmd.RunE(cmd, args) }
	}
	return map[string]func() error{
		"get": run(notifyPrefsGetCmd, nil),
		"set": run(notifyPrefsSetCmd, nil),
	}
}

func TestNotifyPrefsCmds_AuthGates(t *testing.T) {
	covSaveState(t)
	for name, invoke := range covNotifyPrefsSubs(t) {
		cliCfg = &cli.CLIConfig{}
		if err := invoke(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected not-logged-in; got %v", name, err)
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := invoke(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error; got %v", name, err)
		}
	}
}

func TestNotifyPrefsCmds_TransportErrors(t *testing.T) {
	covStubDown(t)
	for name, invoke := range covNotifyPrefsSubs(t) {
		if err := invoke(); err == nil {
			t.Errorf("%s: expected transport error against dead server", name)
		}
	}
}

// TestNotifyPrefsCmds_HappyPath drives get/set through the CLI command
// layer against a stub server — the acceptance contract an agent hits.
func TestNotifyPrefsCmds_HappyPath(t *testing.T) {
	s := covStubCli9(t)
	s.OnPut("/api/v1/me/notification-prefs", clitest.JSONResponse(200, map[string]any{"ok": true}))
	s.OnGet("/api/v1/me/notification-prefs", clitest.JSONResponse(200, map[string]any{
		"cells": []map[string]any{
			{"category": "approvals", "channel_id": "nch_1", "state": "immediate"},
		},
	}))

	covSetFlagCli9(t, notifyPrefsSetCmd, "category", "approvals")
	covSetFlagCli9(t, notifyPrefsSetCmd, "channel", "nch_1")
	covSetFlagCli9(t, notifyPrefsSetCmd, "state", "immediate")

	out := covCaptureStdoutCli9(t, func() {
		if err := notifyPrefsSetCmd.RunE(notifyPrefsSetCmd, nil); err != nil {
			t.Errorf("set: %v", err)
		}
		if err := notifyPrefsGetCmd.RunE(notifyPrefsGetCmd, nil); err != nil {
			t.Errorf("get: %v", err)
		}
	})
	if !strings.Contains(out, "approvals") {
		t.Errorf("expected category in output; got:\n%s", out)
	}
	if !strings.Contains(out, "nch_1") {
		t.Errorf("expected channel id in output; got:\n%s", out)
	}
}

func TestNotifyPrefsSet_ValidationLocal(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "ws_1"}

	covSetFlagCli9(t, notifyPrefsSetCmd, "category", "")
	covSetFlagCli9(t, notifyPrefsSetCmd, "channel", "nch_1")
	covSetFlagCli9(t, notifyPrefsSetCmd, "state", "immediate")
	if err := notifyPrefsSetCmd.RunE(notifyPrefsSetCmd, nil); err == nil || !strings.Contains(err.Error(), "--category") {
		t.Errorf("missing category should fail locally; got %v", err)
	}

	covSetFlagCli9(t, notifyPrefsSetCmd, "category", "approvals")
	covSetFlagCli9(t, notifyPrefsSetCmd, "channel", "")
	if err := notifyPrefsSetCmd.RunE(notifyPrefsSetCmd, nil); err == nil || !strings.Contains(err.Error(), "--channel") {
		t.Errorf("missing channel should fail locally; got %v", err)
	}

	covSetFlagCli9(t, notifyPrefsSetCmd, "channel", "nch_1")
	covSetFlagCli9(t, notifyPrefsSetCmd, "state", "sometimes")
	if err := notifyPrefsSetCmd.RunE(notifyPrefsSetCmd, nil); err == nil || !strings.Contains(err.Error(), "--state") {
		t.Errorf("invalid state should fail locally; got %v", err)
	}
}
