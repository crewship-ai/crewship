package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

// TestComposioToolkitsRunE_YAMLAndNDJSON is the flip side of
// DefaultStaysHuman: composioToolkitsCmd routes the single
// composioToolkitsResponse struct through f.AutoHuman now, so --format
// yaml/ndjson must carry the {enabled, total, toolkits} payload instead of
// the rendered catalog table. Previously ndjson fell through the old
// `f.Format == "json" || f.Format == "yaml"` gate straight to the human
// view — this is the machine-format regression the sweep fixed.
func TestComposioToolkitsRunE_YAMLAndNDJSON(t *testing.T) {
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

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioToolkitsCmd.RunE(composioToolkitsCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	// yaml.v3 decodes a bare "1" as an int, not float64 — compare the
	// formatted string rather than the exact numeric type.
	if got := fmt.Sprintf("%v", yamlPayload["total"]); got != "1" {
		t.Errorf("yaml payload total = %v (%T), want 1", yamlPayload["total"], yamlPayload["total"])
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = composioToolkitsCmd.RunE(composioToolkitsCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The toolkits response is a single struct (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the toolkits object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["total"] != 1.0 {
		t.Errorf("ndjson payload total = %v, want 1", ndjsonPayload["total"])
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

// TestComposioKeyShowRunE_YAMLAndNDJSON is the flip side of
// DefaultStaysHuman: composioKeyShowCmd routes the single
// composioSettingsResponse struct through f.AutoHuman now, so --format
// yaml/ndjson must carry the {configured, source, label} payload instead
// of the prose status lines.
func TestComposioKeyShowRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/integrations/composio/settings", clitest.JSONResponse(200, map[string]any{
		"configured": true, "source": "workspace", "label": "prod",
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioKeyShowCmd.RunE(composioKeyShowCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	if yamlPayload["source"] != "workspace" {
		t.Errorf("yaml payload missing source=workspace: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = composioKeyShowCmd.RunE(composioKeyShowCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The settings response is a single struct (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the settings object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["source"] != "workspace" {
		t.Errorf("ndjson payload missing source=workspace: %+v", ndjsonPayload)
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

// TestComposioBindingsRunE_YAMLAndNDJSON is the flip side of
// DefaultStaysHuman: composioBindingsCmd routes the single
// composioListBindingsResponse struct through f.AutoHuman now, so --format
// yaml/ndjson must carry the {agent_id, bindings} payload instead of the
// rendered table.
func TestComposioBindingsRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	const agentID = "c1111111111111111111111"
	stub.OnGet("/api/v1/integrations/composio/agents/"+agentID+"/bind", clitest.JSONResponse(200, map[string]any{
		"agent_id": agentID,
		"bindings": []map[string]any{
			{"toolkit": "gmail", "mode": "full", "user_id": "u1", "endpoint": "http://x"},
		},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = composioBindingsCmd.RunE(composioBindingsCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	bindings, ok := yamlPayload["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("yaml payload missing 1 binding: %+v", yamlPayload)
	}
	binding, ok := bindings[0].(map[string]any)
	if !ok || binding["toolkit"] != "gmail" {
		t.Errorf("yaml binding mismatch: %+v", bindings[0])
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = composioBindingsCmd.RunE(composioBindingsCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The bindings response is a single struct (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the bindings object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	ndBindings, ok := ndjsonPayload["bindings"].([]any)
	if !ok || len(ndBindings) != 1 {
		t.Fatalf("ndjson payload missing 1 binding: %+v", ndjsonPayload)
	}
	ndBinding, ok := ndBindings[0].(map[string]any)
	if !ok || ndBinding["toolkit"] != "gmail" {
		t.Errorf("ndjson binding mismatch: %+v", ndBindings[0])
	}
}
