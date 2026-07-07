package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covNotifyChannelSubs enumerates every notifychannel subcommand with
// valid args + required flags so the auth/transport sweeps stay
// table-driven, mirroring covCheckpointSubs.
func covNotifyChannelSubs(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli9(t, notifyChannelAddCmd, "type", "webhook")
	covSetFlagCli9(t, notifyChannelAddCmd, "url", "https://hooks.example.com/x")
	covSetFlagCli9(t, notifyChannelRmCmd, "yes", "true")
	run := func(cmd *cobra.Command, args []string) func() error {
		return func() error { return cmd.RunE(cmd, args) }
	}
	return map[string]func() error{
		"list": run(notifyChannelListCmd, nil),
		"add":  run(notifyChannelAddCmd, nil),
		"test": run(notifyChannelTestCmd, []string{"nch_1"}),
		"rm":   run(notifyChannelRmCmd, []string{"nch_1"}),
	}
}

func TestNotifyChannelCmds_AuthGates(t *testing.T) {
	covSaveState(t)
	for name, invoke := range covNotifyChannelSubs(t) {
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

func TestNotifyChannelCmds_TransportErrors(t *testing.T) {
	covStubDown(t)
	for name, invoke := range covNotifyChannelSubs(t) {
		if err := invoke(); err == nil {
			t.Errorf("%s: expected transport error against dead server", name)
		}
	}
}

// TestNotifyChannelCmds_HappyPath drives the full CRUD + test lifecycle
// through the CLI command layer against a stub server — the acceptance
// contract an agent hits, not a hand-rolled HTTP request.
func TestNotifyChannelCmds_HappyPath(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/notification-channels", clitest.JSONResponse(201, map[string]any{
		"id": "nch_1", "type": "webhook", "url": "https://hooks.example.com/x",
		"enabled": true, "secret": "deadbeefsecret",
	}))
	s.OnGet("/api/v1/notification-channels", clitest.JSONResponse(200, map[string]any{
		"channels": []map[string]any{
			{"id": "nch_1", "type": "webhook", "url": "https://hooks.example.com/x", "enabled": true},
		},
	}))
	s.OnPost("/api/v1/notification-channels/nch_1/test", clitest.JSONResponse(200, map[string]any{
		"ok": true, "channel_id": "nch_1",
	}))
	s.OnDelete("/api/v1/notification-channels/nch_1", clitest.JSONResponse(200, map[string]any{"deleted": "nch_1"}))

	covSetFlagCli9(t, notifyChannelAddCmd, "type", "webhook")
	covSetFlagCli9(t, notifyChannelAddCmd, "url", "https://hooks.example.com/x")
	covSetFlagCli9(t, notifyChannelRmCmd, "yes", "true")

	out := covCaptureStdoutCli9(t, func() {
		if err := notifyChannelAddCmd.RunE(notifyChannelAddCmd, nil); err != nil {
			t.Errorf("add: %v", err)
		}
		if err := notifyChannelListCmd.RunE(notifyChannelListCmd, nil); err != nil {
			t.Errorf("list: %v", err)
		}
		if err := notifyChannelTestCmd.RunE(notifyChannelTestCmd, []string{"nch_1"}); err != nil {
			t.Errorf("test: %v", err)
		}
		if err := notifyChannelRmCmd.RunE(notifyChannelRmCmd, []string{"nch_1"}); err != nil {
			t.Errorf("rm: %v", err)
		}
	})
	// The one-time signing secret must surface to the operator on add.
	if !strings.Contains(out, "deadbeefsecret") {
		t.Errorf("add should print the one-time webhook secret; got:\n%s", out)
	}
	if !strings.Contains(out, "nch_1") {
		t.Errorf("expected channel id in output; got:\n%s", out)
	}
}

// TestNotifyChannelAdd_ValidationLocal checks the client-side required-
// flag guards before any request is made.
func TestNotifyChannelAdd_ValidationLocal(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "ws_1"}

	covSetFlagCli9(t, notifyChannelAddCmd, "type", "webhook")
	covSetFlagCli9(t, notifyChannelAddCmd, "url", "")
	if err := notifyChannelAddCmd.RunE(notifyChannelAddCmd, nil); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Errorf("webhook without url should fail locally; got %v", err)
	}

	covSetFlagCli9(t, notifyChannelAddCmd, "type", "email")
	covSetFlagCli9(t, notifyChannelAddCmd, "to", "")
	if err := notifyChannelAddCmd.RunE(notifyChannelAddCmd, nil); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Errorf("email without to should fail locally; got %v", err)
	}

	covSetFlagCli9(t, notifyChannelAddCmd, "type", "sms")
	if err := notifyChannelAddCmd.RunE(notifyChannelAddCmd, nil); err == nil || !strings.Contains(err.Error(), "email' or 'webhook") {
		t.Errorf("bad type should fail locally; got %v", err)
	}
}
