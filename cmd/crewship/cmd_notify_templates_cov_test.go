package main

// Coverage for `notify templates list/set/rm` (cmd_notify_templates.go,
// notifyTemplatesCmd).

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestNotifyTemplatesCmd_HasChildren(t *testing.T) {
	have := map[string]bool{}
	for _, c := range notifyTemplatesCmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"list", "set", "rm"} {
		if !have[want] {
			t.Errorf("notifyTemplatesCmd missing subcommand %q", want)
		}
	}
}

func TestNotifyTemplatesListRunE_Happy(t *testing.T) {
	covSetupCli5(t).OnGet("/api/v1/notification-templates", clitest.JSONResponse(200, map[string]any{
		"templates": []map[string]any{
			{"category": "routines.failed", "channel_id": "", "title": "{{ vars.pipeline_slug }} failed", "body": "after {{ vars.total_duration_ms }}ms"},
		},
	}))
	out := humanOut(t, func() error {
		return notifyTemplatesListCmd.RunE(notifyTemplatesListCmd, nil)
	})
	for _, want := range []string{"routines.failed", "(all)", "pipeline_slug"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestNotifyTemplatesSetRunE_Validation(t *testing.T) {
	covSetupCli5(t)
	err := notifyTemplatesSetCmd.RunE(notifyTemplatesSetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--category is required") {
		t.Errorf("want category-required error, got %v", err)
	}

	if err := notifyTemplatesSetCmd.Flags().Set("category", "routines.failed"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifyTemplatesSetCmd.Flags().Set("category", "") })
	err = notifyTemplatesSetCmd.RunE(notifyTemplatesSetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "give --title, --body, or both") {
		t.Errorf("want title/body-required error, got %v", err)
	}
}

func TestNotifyTemplatesSetRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPut("/api/v1/notification-templates", clitest.JSONResponse(200, map[string]any{}))
	if err := notifyTemplatesSetCmd.Flags().Set("category", "routines.failed"); err != nil {
		t.Fatal(err)
	}
	if err := notifyTemplatesSetCmd.Flags().Set("title", "{{ vars.pipeline_slug }} failed"); err != nil {
		t.Fatal(err)
	}
	if err := notifyTemplatesSetCmd.Flags().Set("channel", covNotifyChannelID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = notifyTemplatesSetCmd.Flags().Set("category", "")
		_ = notifyTemplatesSetCmd.Flags().Set("title", "")
		_ = notifyTemplatesSetCmd.Flags().Set("channel", "")
	})

	setFormatCov(t, "")
	var runErr error
	out := covCaptureAll(t, func() {
		runErr = notifyTemplatesSetCmd.RunE(notifyTemplatesSetCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, "Template set: routines.failed on channel "+covNotifyChannelID) {
		t.Errorf("output = %q", out)
	}
	calls := stub.CallsFor("PUT", "/api/v1/notification-templates")
	if len(calls) != 1 {
		t.Fatalf("want 1 PUT call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["category"] != "routines.failed" || body["channel_id"] != covNotifyChannelID {
		t.Errorf("body = %v", body)
	}
}

func TestNotifyTemplatesRmRunE_Validation(t *testing.T) {
	covSetupCli5(t)
	err := notifyTemplatesRmCmd.RunE(notifyTemplatesRmCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--category is required") {
		t.Errorf("want category-required error, got %v", err)
	}
}

func TestNotifyTemplatesRmRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnDelete("/api/v1/notification-templates", clitest.JSONResponse(200, map[string]any{}))
	if err := notifyTemplatesRmCmd.Flags().Set("category", "routines.failed"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifyTemplatesRmCmd.Flags().Set("category", "") })

	out := humanOut(t, func() error {
		return notifyTemplatesRmCmd.RunE(notifyTemplatesRmCmd, nil)
	})
	if !strings.Contains(out, "Template removed: routines.failed") {
		t.Errorf("output = %q", out)
	}
	calls := stub.CallsFor("DELETE", "/api/v1/notification-templates")
	if len(calls) != 1 {
		t.Fatalf("want 1 DELETE call, got %d; calls: %#v", len(calls), stub.Calls())
	}
	if !strings.Contains(calls[0].Query, "category=routines.failed") {
		t.Errorf("query = %q, want category param", calls[0].Query)
	}
}
