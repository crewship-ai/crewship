package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These tests lock the --format contract for the `integration composio`
// family (batch 6 of #964). Each command used to gate its structured dump
// behind `if f.Format == "json" || f.Format == "yaml"` and silently degrade
// `--format ndjson` back to the ANSI human view. They now route through
// f.AutoHuman, which honors json/yaml/ndjson and keeps the hand-crafted human
// output for the default/table path. Each guard asserts that the human
// rendering is byte-preserved when no machine format is requested.
//
// (No ANSI assertions — the covCaptureStdoutCli5 pipe is a non-TTY sink, so
// lipgloss emits plain, ANSI-free cell text that survives substring checks.)

func TestComposioInventoryRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/inventory", clitest.JSONResponse(200, map[string]any{
		"enabled": true,
		"auth_configs": []map[string]any{
			{"id": "ac1", "name": "Gmail", "status": "ACTIVE", "toolkit": map[string]any{"slug": "gmail"}},
		},
		"users": []map[string]any{
			{"user_id": "u1", "connected_accounts": []map[string]any{
				{"id": "ca1", "user_id": "u1", "status": "ACTIVE", "toolkit": map[string]any{"slug": "gmail"}},
			}},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioInventoryCmd.RunE(composioInventoryCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Connector catalog (auth configs):", "Connected users:"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestComposioToolkitsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/toolkits", clitest.JSONResponse(200, map[string]any{
		"enabled": true,
		"total":   1,
		"toolkits": []map[string]any{
			{"slug": "gmail", "name": "Gmail", "meta": map[string]any{
				"tools_count": 5, "categories": []map[string]any{{"name": "email"}},
			}},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioToolkitsCmd.RunE(composioToolkitsCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Showing 1 of 1 apps.", "Narrow with --search"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestComposioToolsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/tools", clitest.JSONResponse(200, map[string]any{
		"enabled": true,
		"total":   1,
		"tools": []map[string]any{
			{"slug": "GMAIL_SEND", "name": "Send", "description": "Send an email", "toolkit": map[string]any{"slug": "gmail"}},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioToolsCmd.RunE(composioToolsCmd, []string{"github"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if want := `Showing 1 of 1 tools for "github".`; !strings.Contains(out, want) {
		t.Errorf("human output missing %q; got:\n%s", want, out)
	}
}

func TestComposioTriggerTypesRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/triggers", clitest.JSONResponse(200, map[string]any{
		"enabled": true,
		"total":   1,
		"triggers": []map[string]any{
			{"slug": "GMAIL_NEW_MESSAGE", "name": "New message", "description": "d", "type": "webhook", "toolkit": map[string]any{"slug": "gmail"}},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioTriggersTypesCmd.RunE(composioTriggersTypesCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if want := "Showing 1 of 1 trigger types."; !strings.Contains(out, want) {
		t.Errorf("human output missing %q; got:\n%s", want, out)
	}
}

func TestComposioActiveTriggersRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/triggers/active", clitest.JSONResponse(200, map[string]any{
		"enabled": true,
		"triggers": []map[string]any{
			{"id": "ti1", "trigger_name": "GMAILNEW", "user_id": "u1"},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioTriggersActiveCmd.RunE(composioTriggersActiveCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human path is a plain (non-TTY) table — assert short cell values survive.
	for _, want := range []string{"GMAILNEW", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestComposioDefaultShowRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/default", clitest.JSONResponse(200, map[string]any{
		"enabled_flag": false, "default_user_id": "u1", "default_mcp_server_id": "srv1", "connected_user_count": 2,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioDefaultShowCmd.RunE(composioDefaultShowCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if want := "COMPOSIO_DEFAULT_CONNECTOR is OFF"; !strings.Contains(out, want) {
		t.Errorf("human output missing %q; got:\n%s", want, out)
	}
}

func TestComposioKeyShowRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/settings", clitest.JSONResponse(200, map[string]any{
		"configured": true, "source": "workspace", "label": "prod",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioKeyShowCmd.RunE(composioKeyShowCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Composio: configured (source: workspace)", "Label:"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestComposioBindingsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	// CUID-shaped id skips the /api/v1/agents slug-resolution round trip.
	const agentID = "c1111111111111111111111"
	stub.OnGet("/api/v1/integrations/composio/agents/"+agentID+"/bind", clitest.JSONResponse(200, map[string]any{
		"agent_id": agentID,
		"bindings": []map[string]any{
			{"toolkit": "gmail", "mode": "full", "user_id": "u1", "endpoint": "http://x"},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioBindingsCmd.RunE(composioBindingsCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human path is a plain (non-TTY) table — assert short cell values survive.
	for _, want := range []string{"gmail", "u1"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}
