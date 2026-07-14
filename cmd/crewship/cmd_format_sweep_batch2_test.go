package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These guards lock the flip side of the batch-2 --format sweep (#964): the
// migrated commands now route json/yaml/ndjson through the structured
// renderers (Formatter.AutoHuman), but WITHOUT a machine format the
// hand-crafted human output must stay intact. Each test drives the command's
// RunE with the default (empty) format and asserts a few distinctive human
// substrings survive. No ANSI assertions — colors depend on the terminal.

func TestModelListRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/models", clitest.JSONResponse(200, map[string]any{
		"provider": "anthropic",
		"source":   "static",
		"models": []map[string]any{
			{"id": "claude-x", "display_name": "Claude X", "provider": "anthropic"},
		},
	}))
	covSetFlagCli5(t, modelListCmd, "provider", "anthropic")

	var err error
	out := covCaptureStdoutCli5(t, func() { err = modelListCmd.RunE(modelListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"anthropic models", "claude-x"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestPresenceRosterRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"agent_id": "ag_1", "crew_id": "cr_1", "status": "online", "since": "2026-07-13T00:00:00Z"},
		},
		"count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = presenceRosterCmd.RunE(presenceRosterCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"AGENT", "ag_1", "online"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestPresenceRosterRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// presenceRosterCmd routes body.Rows (a slice) through f.AutoHuman now, so
// --format yaml/ndjson must carry the roster instead of the ANSI table.
// Two rows are stubbed so the ndjson "one line per element" behavior is a
// real assertion, not a coincidence of a single-row payload.
func TestPresenceRosterRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/presence/roster", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"agent_id": "ag_1", "crew_id": "cr_1", "status": "online", "since": "2026-07-13T00:00:00Z"},
			{"agent_id": "ag_2", "crew_id": "cr_1", "status": "busy", "since": "2026-07-13T00:05:00Z"},
		},
		"count": 2,
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = presenceRosterCmd.RunE(presenceRosterCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["status"] != "online" || yamlRows[1]["status"] != "busy" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = presenceRosterCmd.RunE(presenceRosterCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per roster row (2), got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		var row map[string]any
		if uerr := json.Unmarshal([]byte(line), &row); uerr != nil {
			t.Fatalf("ndjson line %d is not valid JSON: %v\nline:\n%s", i, uerr, line)
		}
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if first["status"] != "online" || second["status"] != "busy" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
	}
}

func TestNotifyChannelAddRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/notification-channels", clitest.JSONResponse(200, map[string]any{
		"id": "nch_1", "type": "webhook", "url": "https://example.com/hook", "events": []string{"run.failed"},
	}))
	covSetFlagCli5(t, notifyChannelAddCmd, "type", "webhook")
	covSetFlagCli5(t, notifyChannelAddCmd, "url", "https://example.com/hook")

	var err error
	// PrintSuccess writes to stderr; the "Notifies on:" line goes to stdout.
	out := covCaptureAll(t, func() { err = notifyChannelAddCmd.RunE(notifyChannelAddCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Notification channel created: nch_1 (webhook)", "run.failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestEscalationPendingCountRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/escalations/pending-count", clitest.JSONResponse(200, map[string]any{
		"count": 7,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = escalationPendingCountCmd.RunE(escalationPendingCountCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.TrimSpace(out) != "7" {
		t.Errorf("human output want bare \"7\"; got:\n%s", out)
	}
}

func TestHooksListRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/hooks", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "hk_1", "event": "run.done", "handler_kind": "webhook", "target": "https://x", "enabled": true, "created_at": "2026-07-13"},
		},
		"count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = hooksListCmd.RunE(hooksListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"EVENT", "hk_1", "run.done"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestHooksListRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// hooksListCmd routes body.Rows (a slice) through f.AutoHuman now, so
// --format yaml/ndjson must carry the hook rows instead of the table.
// Two rows are stubbed so ndjson's one-line-per-element behavior is
// actually exercised.
func TestHooksListRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/hooks", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "hk_1", "event": "run.done", "handler_kind": "webhook", "target": "https://x", "enabled": true, "created_at": "2026-07-13"},
			{"id": "hk_2", "event": "run.failed", "handler_kind": "script", "target": "/bin/notify", "enabled": false, "created_at": "2026-07-12"},
		},
		"count": 2,
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = hooksListCmd.RunE(hooksListCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["event"] != "run.done" || yamlRows[1]["event"] != "run.failed" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = hooksListCmd.RunE(hooksListCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per hook row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["event"] != "run.done" || second["event"] != "run.failed" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
	}
}

func TestRecallRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-07-13T10:00:00Z", "entry_type": "peer.escalation", "summary": "db lock timeout", "crew_id": "cr_1", "agent_id": "ag_1"},
		},
		"count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = recallCmd.RunE(recallCmd, []string{"lock"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// The summary term matching the query gets ANSI-highlighted, so assert
	// substrings that don't straddle the highlight (the scope + entry type).
	for _, want := range []string{"1 match", "peer.escalation", "cr_1 / ag_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestHistoryRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	slug := "eva"
	// history's default --since is 24h and the window is filtered client-side
	// against time.Now (cmd_history.go), so the seed must be relative to now,
	// not a fixed literal — a hardcoded date silently ages out of the window
	// and starts failing 24h after it's written. Anchor one hour ago.
	createdAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "run_1", "agent_slug": slug, "status": "completed", "trigger_type": "manual", "created_at": createdAt},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = historyCmd.RunE(historyCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"eva", "completed", "manual"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestProjectStatsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/projects/proj_1/stats", clitest.JSONResponse(200, map[string]any{
		"total_issues":     10,
		"completed_issues": 4,
		"by_status":        map[string]any{"open": 6},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = projectStatsCmd.RunE(projectStatsCmd, []string{"proj_1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Issues: 10 total / 4 completed (40%)", "By status:"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestMemoryHealthRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/memory/health", clitest.JSONResponse(200, map[string]any{
		"overall": 82.0,
		"metrics": map[string]any{"freshness": 90.0, "coverage": 70.0},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = memoryHealthCmd.RunE(memoryHealthCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Memory health (workspace-wide)", "82/100", "freshness"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestMemoryHealthRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// memoryHealthCmd routes the single body struct through f.AutoHuman now, so
// --format yaml/ndjson must carry the health payload instead of the
// coloured summary lines. Unlike the roster/hooks list commands, this
// payload is a single object — ndjson must emit exactly one line.
func TestMemoryHealthRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/memory/health", clitest.JSONResponse(200, map[string]any{
		"overall": 82.0,
		"metrics": map[string]any{"freshness": 90.0, "coverage": 70.0},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = memoryHealthCmd.RunE(memoryHealthCmd, nil) })
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
	// yaml.v3 decodes a bare "82" as an int (not float64), so compare the
	// formatted string rather than the exact numeric type.
	if got := fmt.Sprintf("%v", yamlPayload["overall"]); got != "82" {
		t.Errorf("yaml payload overall = %v (%T), want 82", yamlPayload["overall"], yamlPayload["overall"])
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = memoryHealthCmd.RunE(memoryHealthCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The health body is a single struct (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the health object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["overall"] != 82.0 {
		t.Errorf("ndjson payload overall = %v, want 82", ndjsonPayload["overall"])
	}
}
