package main

import (
	"strings"
	"testing"

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
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "run_1", "agent_slug": slug, "status": "completed", "trigger_type": "manual", "created_at": "2026-07-13T09:00:00Z"},
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
