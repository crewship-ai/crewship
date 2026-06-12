package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covInspectRunsStub(t *testing.T, s *clitest.StubServer, agentID, createdAt string) {
	t.Helper()
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "r1", "agent_id": agentID, "agent_slug": "viktor", "chat_id": "ch1", "created_at": createdAt},
		},
	}))
}

func covInspectJournal() map[string]any {
	// Server returns newest-first; fetchInspectEntries reverses.
	return map[string]any{"entries": []map[string]any{
		{"ts": "2026-06-10T10:03:00Z", "entry_type": "exec.error", "severity": "error", "summary": "step failed", "trace_id": "r1"},
		{"ts": "2026-06-10T10:02:00Z", "entry_type": "cost.incurred", "severity": "notice", "summary": "spent", "trace_id": "r1", "payload": map[string]any{"cost_usd": 0.5}},
		{"ts": "2026-06-10T10:01:30Z", "entry_type": "tool_call", "severity": "info", "summary": "called bash", "trace_id": "r1"},
		{"ts": "2026-06-10T10:01:00Z", "entry_type": "run.started", "severity": "warn", "summary": "kick off"}, // no trace_id → kept
		{"ts": "2026-06-10T10:00:00Z", "entry_type": "noise", "severity": "info", "summary": "other run noise", "trace_id": "r2"},
	}}
}

func TestInspectRunE_TableTimeline(t *testing.T) {
	s := covStubCli9(t)
	covInspectRunsStub(t, s, "ag1", "2026-06-10T10:00:00Z")
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covInspectJournal()))

	out := covCaptureStdoutCli9(t, func() {
		if err := inspectCmd.RunE(inspectCmd, []string{"r1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Run r1", "viktor", "step failed", "called bash", "kick off", "$0.5000"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other run noise") {
		t.Errorf("entry from another trace must be filtered:\n%s", out)
	}
	// 4 kept entries: 1 tool call, 1 error.
	if !strings.Contains(out, "(4 entries)") {
		t.Errorf("expected 4 entries after trace filter:\n%s", out)
	}

	// The journal request must be agent+window scoped.
	calls := s.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected one journal GET, got %d", len(calls))
	}
	q := calls[0].Query
	if !strings.Contains(q, "agent_id=ag1") || !strings.Contains(q, "limit=200") {
		t.Errorf("journal query missing scoping params: %q", q)
	}
	// Window starts 5 minutes before created_at.
	if !strings.Contains(q, "since=2026-06-10T09%3A55%3A00Z") {
		t.Errorf("journal since should be created_at-5m: %q", q)
	}
}

func TestInspectRunE_EmptyJournalWindow(t *testing.T) {
	s := covStubCli9(t)
	// Unparsable created_at → runWindowStart fails → 1h fallback window.
	covInspectRunsStub(t, s, "ag1", "not-a-time")
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []map[string]any{}}))

	out := covCaptureStdoutCli9(t, func() {
		if err := inspectCmd.RunE(inspectCmd, []string{"r1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "no entries in journal window") {
		t.Errorf("missing empty-window hint:\n%s", out)
	}
}

func TestInspectRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	covInspectRunsStub(t, s, "ag1", "2026-06-10T10:00:00Z")
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covInspectJournal()))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := inspectCmd.RunE(inspectCmd, []string{"r1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{`"run_id"`, `"agent_id"`, `"entries"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json payload missing %q:\n%s", want, out)
		}
	}
}

func TestInspectRunE_FilterFallsBackWithoutJQ(t *testing.T) {
	s := covStubCli9(t)
	covInspectRunsStub(t, s, "ag1", "2026-06-10T10:00:00Z")
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covInspectJournal()))
	covSetFlagCli9(t, inspectCmd, "filter", ".entries")

	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("jq not installed") }
	t.Cleanup(func() { lookPath = origLookPath })

	out := covCaptureStdoutCli9(t, func() {
		if err := inspectCmd.RunE(inspectCmd, []string{"r1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// jq missing → raw JSON still emitted.
	if !strings.Contains(out, `"entries"`) {
		t.Errorf("raw JSON fallback missing:\n%s", out)
	}
}

func TestInspectRunE_TypesFlagForwarded(t *testing.T) {
	s := covStubCli9(t)
	covInspectRunsStub(t, s, "ag1", "2026-06-10T10:00:00Z")
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []map[string]any{}}))
	covSetFlagCli9(t, inspectCmd, "types", "error,exec.error")

	_ = covCaptureStdoutCli9(t, func() {
		if err := inspectCmd.RunE(inspectCmd, []string{"r1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	calls := s.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "entry_type=error%2Cexec.error") {
		t.Errorf("entry_type not forwarded: %+v", calls)
	}
}

func TestInspectRunE_Errors(t *testing.T) {
	t.Run("run not found", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))
		err := inspectCmd.RunE(inspectCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "not found in last 100 runs") {
			t.Errorf("expected run-not-found; got %v", err)
		}
	})
	t.Run("run without agent", func(t *testing.T) {
		s := covStubCli9(t)
		covInspectRunsStub(t, s, "", "2026-06-10T10:00:00Z")
		err := inspectCmd.RunE(inspectCmd, []string{"r1"})
		if err == nil || !strings.Contains(err.Error(), "has no agent_id") {
			t.Errorf("expected no-agent error; got %v", err)
		}
	})
	t.Run("journal error", func(t *testing.T) {
		s := covStubCli9(t)
		covInspectRunsStub(t, s, "ag1", "2026-06-10T10:00:00Z")
		s.OnGet("/api/v1/journal", clitest.ErrorResponse(500, "journal down"))
		err := inspectCmd.RunE(inspectCmd, []string{"r1"})
		if err == nil || !strings.Contains(err.Error(), "journal down") {
			t.Errorf("expected journal error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := inspectCmd.RunE(inspectCmd, []string{"r1"}); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
}

func TestFilterEntriesByTrace(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		{"summary": "mine", "trace_id": "r1"},
		{"summary": "other", "trace_id": "r2"},
		{"summary": "no trace"},
		{"summary": "empty trace", "trace_id": ""},
		{"summary": "non-string trace", "trace_id": 42},
	}
	got := filterEntriesByTrace(entries, "r1")
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (drop only the foreign trace)", len(got))
	}
	for _, e := range got {
		if e["summary"] == "other" {
			t.Error("foreign-trace entry must be dropped")
		}
	}
}
