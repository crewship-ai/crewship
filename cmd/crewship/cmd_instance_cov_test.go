package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestInstanceCmdTree(t *testing.T) {
	t.Parallel()
	if instanceCmd.Use != "instance" {
		t.Errorf("Use = %q", instanceCmd.Use)
	}
	have := map[string]bool{}
	for _, sub := range instanceSettingsCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "get", "set", "delete"} {
		if !have[want] {
			t.Errorf("instance settings missing subcommand %q (have %v)", want, have)
		}
	}
}

func TestInstanceSettingsList(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/instance/settings", clitest.JSONResponse(200, []instanceSettingItem{
		{Key: "smtp.host", Value: "mail.example.com", UpdatedAt: "2026-06-01T00:00:00Z"},
		{Key: "smtp.password", Value: "***"},
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := instanceSettingsListCmd.RunE(instanceSettingsListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"smtp.host", "mail.example.com", "***"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestInstanceSettingsGet(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/instance/settings/smtp.host", clitest.JSONResponse(200, instanceSettingItem{
		Key: "smtp.host", Value: "mail.example.com", UpdatedAt: "2026-06-01T00:00:00Z",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := instanceSettingsGetCmd.RunE(instanceSettingsGetCmd, []string{"smtp.host"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "smtp.host") || !strings.Contains(out, "mail.example.com") {
		t.Errorf("get output incomplete:\n%s", out)
	}
}

func TestInstanceSettingsSet(t *testing.T) {
	s := covStubCli9(t)
	s.OnPut("/api/v1/instance/settings/feature.flag", clitest.JSONResponse(200, instanceSettingItem{
		Key: "feature.flag", Value: "on",
	}))

	out := covCaptureStderrCli9(t, func() {
		if err := instanceSettingsSetCmd.RunE(instanceSettingsSetCmd, []string{"feature.flag", "on"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Setting saved: feature.flag = on") {
		t.Errorf("missing success line:\n%s", out)
	}

	calls := s.CallsFor("PUT", "/api/v1/instance/settings/feature.flag")
	if len(calls) != 1 {
		t.Fatalf("expected one PUT, got %d", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("PUT body not JSON: %v", err)
	}
	if body["value"] != "on" {
		t.Errorf("PUT body = %v, want value=on", body)
	}
}

func TestInstanceSettingsDelete_WithYes(t *testing.T) {
	s := covStubCli9(t)
	s.OnDelete("/api/v1/instance/settings/old.key", clitest.EmptyResponse(204))
	covSetFlagCli9(t, instanceSettingsDeleteCmd, "yes", "true")

	out := covCaptureStderrCli9(t, func() {
		if err := instanceSettingsDeleteCmd.RunE(instanceSettingsDeleteCmd, []string{"old.key"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Setting deleted.") {
		t.Errorf("missing delete confirmation:\n%s", out)
	}
	if got := len(s.CallsFor("DELETE", "/api/v1/instance/settings/old.key")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestInstanceSettingsDelete_AbortsWithoutConfirmation(t *testing.T) {
	s := covStubCli9(t)
	covSetFlagCli9(t, instanceSettingsDeleteCmd, "yes", "false")

	// Under `go test` stdin is /dev/null (not a TTY) so confirmAction's
	// plain-Scanln fallback reads EOF → empty answer → abort.
	err := instanceSettingsDeleteCmd.RunE(instanceSettingsDeleteCmd, []string{"old.key"})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted; got %v", err)
	}
	if got := len(s.CallsFor("DELETE", "/api/v1/instance/settings/old.key")); got != 0 {
		t.Errorf("aborted delete must not hit the API (%d calls)", got)
	}
}

func TestInstanceSettings_ServerErrors(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/instance/settings", clitest.ErrorResponse(403, "admin only"))
	s.OnGet("/api/v1/instance/settings/k", clitest.ErrorResponse(404, "no such setting"))
	s.OnPut("/api/v1/instance/settings/k", clitest.ErrorResponse(400, "bad value"))
	s.OnDelete("/api/v1/instance/settings/k", clitest.ErrorResponse(403, "protected key"))
	covSetFlagCli9(t, instanceSettingsDeleteCmd, "yes", "true")

	if err := instanceSettingsListCmd.RunE(instanceSettingsListCmd, nil); err == nil || !strings.Contains(err.Error(), "admin only") {
		t.Errorf("list: expected admin-only error; got %v", err)
	}
	if err := instanceSettingsGetCmd.RunE(instanceSettingsGetCmd, []string{"k"}); err == nil || !strings.Contains(err.Error(), "no such setting") {
		t.Errorf("get: expected 404 error; got %v", err)
	}
	if err := instanceSettingsSetCmd.RunE(instanceSettingsSetCmd, []string{"k", "v"}); err == nil || !strings.Contains(err.Error(), "bad value") {
		t.Errorf("set: expected 400 error; got %v", err)
	}
	if err := instanceSettingsDeleteCmd.RunE(instanceSettingsDeleteCmd, []string{"k"}); err == nil || !strings.Contains(err.Error(), "protected key") {
		t.Errorf("delete: expected 403 error; got %v", err)
	}
}

func TestInstanceSettings_EmptyKeyRejected(t *testing.T) {
	covStubCli9(t) // never reached: validation precedes the request
	if err := instanceSettingsGetCmd.RunE(instanceSettingsGetCmd, []string{""}); err == nil || !strings.Contains(err.Error(), "<key> is required") {
		t.Errorf("get: expected key-required error; got %v", err)
	}
	if err := instanceSettingsSetCmd.RunE(instanceSettingsSetCmd, []string{"", "v"}); err == nil || !strings.Contains(err.Error(), "<key> is required") {
		t.Errorf("set: expected key-required error; got %v", err)
	}
	covSetFlagCli9(t, instanceSettingsDeleteCmd, "yes", "true")
	if err := instanceSettingsDeleteCmd.RunE(instanceSettingsDeleteCmd, []string{""}); err == nil || !strings.Contains(err.Error(), "<key> is required") {
		t.Errorf("delete: expected key-required error; got %v", err)
	}
}

func TestInstanceSettings_TransportErrors(t *testing.T) {
	covStubDown(t)
	covSetFlagCli9(t, instanceSettingsDeleteCmd, "yes", "true")
	if err := instanceSettingsListCmd.RunE(instanceSettingsListCmd, nil); err == nil {
		t.Error("list: expected transport error")
	}
	if err := instanceSettingsGetCmd.RunE(instanceSettingsGetCmd, []string{"k"}); err == nil {
		t.Error("get: expected transport error")
	}
	if err := instanceSettingsSetCmd.RunE(instanceSettingsSetCmd, []string{"k", "v"}); err == nil {
		t.Error("set: expected transport error")
	}
	if err := instanceSettingsDeleteCmd.RunE(instanceSettingsDeleteCmd, []string{"k"}); err == nil {
		t.Error("delete: expected transport error")
	}
}

func TestInstanceSettings_DecodeErrors(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/instance/settings", clitest.TextResponse(200, "{nope"))
	s.OnGet("/api/v1/instance/settings/k", clitest.TextResponse(200, "{nope"))
	s.OnPut("/api/v1/instance/settings/k", clitest.TextResponse(200, "{nope"))

	if err := instanceSettingsListCmd.RunE(instanceSettingsListCmd, nil); err == nil {
		t.Error("list: expected decode error")
	}
	if err := instanceSettingsGetCmd.RunE(instanceSettingsGetCmd, []string{"k"}); err == nil {
		t.Error("get: expected decode error")
	}
	if err := instanceSettingsSetCmd.RunE(instanceSettingsSetCmd, []string{"k", "v"}); err == nil {
		t.Error("set: expected decode error")
	}
}

func TestInstanceSettings_AuthGates(t *testing.T) {
	subs := map[string]struct {
		cmd  *cobra.Command
		args []string
	}{
		"list":   {instanceSettingsListCmd, nil},
		"get":    {instanceSettingsGetCmd, []string{"k"}},
		"set":    {instanceSettingsSetCmd, []string{"k", "v"}},
		"delete": {instanceSettingsDeleteCmd, []string{"k"}},
	}
	for name, tc := range subs {
		t.Run(name+" no auth", func(t *testing.T) {
			covSaveState(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("expected not-logged-in; got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			covSaveState(t)
			cliCfg = &cli.CLIConfig{Token: "tok"}
			if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Errorf("expected workspace error; got %v", err)
			}
		})
	}
}
