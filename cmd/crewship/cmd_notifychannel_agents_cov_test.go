package main

// Coverage for `notifychannel agents allow/deny/list` and
// `notifychannel test-draft` (cmd_notifychannel.go). Both
// notifyChannelAgentsAllowCmd and notifyChannelTestDraftCmd names are
// absent from the rest of the suite.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covNotifyChannelID = "nch_abc123"
const covNotifyAgentID = "cagent0123456789abcdefgh"

func TestNotifyChannelAgentsAllowRunE_RequiresAgent(t *testing.T) {
	covSetupCli5(t)
	err := notifyChannelAgentsAllowCmd.RunE(notifyChannelAgentsAllowCmd, []string{covNotifyChannelID})
	if err == nil || !strings.Contains(err.Error(), "--agent is required") {
		t.Errorf("want --agent required error, got %v", err)
	}
}

func TestNotifyChannelAgentsAllowRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/notification-channels/" + covNotifyChannelID + "/agents"
	stub.OnPost(path, clitest.JSONResponse(200, map[string]any{"agent_id": covNotifyAgentID}))
	if err := notifyChannelAgentsAllowCmd.Flags().Set("agent", covNotifyAgentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifyChannelAgentsAllowCmd.Flags().Set("agent", "") })

	setFormatCov(t, "")
	var runErr error
	out := covCaptureAll(t, func() {
		runErr = notifyChannelAgentsAllowCmd.RunE(notifyChannelAgentsAllowCmd, []string{covNotifyChannelID})
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, "may now send to channel "+covNotifyChannelID) {
		t.Errorf("output = %q", out)
	}
	calls := stub.CallsFor("POST", path)
	if len(calls) != 1 {
		t.Fatalf("want 1 POST call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["agent_id"] != covNotifyAgentID {
		t.Errorf("agent_id = %v", body["agent_id"])
	}

	doc := jsonOut(t, func() error {
		return notifyChannelAgentsAllowCmd.RunE(notifyChannelAgentsAllowCmd, []string{covNotifyChannelID})
	})
	row, ok := doc.(map[string]any)
	if !ok || row["allowed"] != true {
		t.Errorf("json output = %#v", doc)
	}
}

func TestNotifyChannelAgentsAllowRunE_ServerRejects(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/notification-channels/" + covNotifyChannelID + "/agents"
	stub.OnPost(path, clitest.ErrorResponse(403, "Forbidden: requires OWNER or ADMIN role"))
	if err := notifyChannelAgentsAllowCmd.Flags().Set("agent", covNotifyAgentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifyChannelAgentsAllowCmd.Flags().Set("agent", "") })

	err := notifyChannelAgentsAllowCmd.RunE(notifyChannelAgentsAllowCmd, []string{covNotifyChannelID})
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("want forbidden error surfaced, got %v", err)
	}
}

// notify test-draft — sends a synthetic notification without persisting a
// channel. See notifyChannelTestDraftCmd's RunE: --type chat is normalized
// to the "shoutrrr" wire value before it's posted.
func TestNotifyChannelTestDraftRunE_ChatTypeNormalized(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/notification-channels/test", clitest.JSONResponse(200, map[string]any{"ok": true}))
	if err := notifyChannelTestDraftCmd.Flags().Set("type", "chat"); err != nil {
		t.Fatal(err)
	}
	if err := notifyChannelTestDraftCmd.Flags().Set("provider", "ntfy"); err != nil {
		t.Fatal(err)
	}
	if err := notifyChannelTestDraftCmd.Flags().Set("field", "topic=my-alerts"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = notifyChannelTestDraftCmd.Flags().Set("type", "")
		_ = notifyChannelTestDraftCmd.Flags().Set("provider", "")
		_ = notifyChannelTestDraftCmd.Flags().Set("field", "")
	})

	setFormatCov(t, "")
	var runErr error
	out := covCaptureAll(t, func() {
		runErr = notifyChannelTestDraftCmd.RunE(notifyChannelTestDraftCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, "nothing was saved") {
		t.Errorf("output = %q", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/notification-channels/test")
	if len(calls) != 1 {
		t.Fatalf("want 1 POST call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["type"] != "shoutrrr" {
		t.Errorf("type = %v, want normalized shoutrrr", body["type"])
	}
	if body["provider"] != "ntfy" {
		t.Errorf("provider = %v", body["provider"])
	}
	fields, _ := body["fields"].(map[string]any)
	if fields["topic"] != "my-alerts" {
		t.Errorf("fields = %v", fields)
	}
}

func TestNotifyChannelTestDraftRunE_BadFieldFlag(t *testing.T) {
	covSetupCli5(t)
	if err := notifyChannelTestDraftCmd.Flags().Set("field", "not-a-kv-pair"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifyChannelTestDraftCmd.Flags().Set("field", "") })

	err := notifyChannelTestDraftCmd.RunE(notifyChannelTestDraftCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--field must be key=value") {
		t.Errorf("want field-parse error, got %v", err)
	}
}

func TestNotifyChannelTestDraftRunE_ServerError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/notification-channels/test", clitest.ErrorResponse(422, "unknown provider"))
	if err := notifyChannelTestDraftCmd.Flags().Set("type", "email"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = notifyChannelTestDraftCmd.Flags().Set("type", "") })

	err := notifyChannelTestDraftCmd.RunE(notifyChannelTestDraftCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("want server error surfaced, got %v", err)
	}
}
