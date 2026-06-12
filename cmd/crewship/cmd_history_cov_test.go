package main

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covHistoryRuns(t *testing.T) []map[string]any {
	t.Helper()
	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	old := time.Now().UTC().Add(-96 * time.Hour).Format(time.RFC3339)
	return []map[string]any{
		{"id": "run-recent", "agent_slug": "viktor", "chat_id": "ch1", "status": "completed", "trigger_type": "chat", "created_at": recent},
		{"id": "run-old", "agent_slug": "ancient-agent", "chat_id": "ch2", "status": "failed", "trigger_type": "cron", "created_at": old},
		{"id": "run-nameonly", "agent_name": "Eva", "status": "running", "trigger_type": "api", "created_at": recent},
	}
}

func TestHistoryRunE_DefaultWindowFiltersOldRuns(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": covHistoryRuns(t)}))

	out := covCaptureStdoutCli9(t, func() {
		if err := historyCmd.RunE(historyCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "viktor") {
		t.Errorf("recent run missing:\n%s", out)
	}
	// agent_name fallback when slug is null.
	if !strings.Contains(out, "Eva") {
		t.Errorf("agent_name fallback missing:\n%s", out)
	}
	// 4-day-old run is outside the default 24h window.
	if strings.Contains(out, "ancient-agent") {
		t.Errorf("old run should be filtered by default --since 24h:\n%s", out)
	}

	calls := s.CallsFor("GET", "/api/v1/runs")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=20") {
		t.Errorf("expected default limit=20 in query: %+v", calls)
	}
}

func TestHistoryRunE_PromptsPreview(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": covHistoryRuns(t)[:1]}))
	s.OnGet("/api/v1/chats/ch1/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]string{
			{"role": "user", "content": "\n  fix the flaky test\nsecond line ignored"},
		},
	}))
	covSetFlagCli9(t, historyCmd, "prompts", "true")

	out := covCaptureStdoutCli9(t, func() {
		if err := historyCmd.RunE(historyCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"fix the flaky test"`) {
		t.Errorf("prompt preview missing (first non-empty line, quoted):\n%s", out)
	}
}

func TestHistoryRunE_StatusAndAgentFilters(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "cagentaaaaaaaaaaaaaaaa", "slug": "viktor"},
	}))
	covSetFlagCli9(t, historyCmd, "status", "failed")
	covSetFlagCli9(t, historyCmd, "agent", "viktor")

	out := covCaptureStdoutCli9(t, func() {
		if err := historyCmd.RunE(historyCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "No runs.") {
		t.Errorf("empty result line missing:\n%s", out)
	}
	calls := s.CallsFor("GET", "/api/v1/runs")
	if len(calls) != 1 {
		t.Fatalf("expected one runs GET, got %d", len(calls))
	}
	q := calls[0].Query
	if !strings.Contains(q, "status=failed") || !strings.Contains(q, "agent_id=cagentaaaaaaaaaaaaaaaa") {
		t.Errorf("filters not forwarded: %q", q)
	}
}

func TestHistoryRunE_AgentNotFound(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"id": "c1", "slug": "eva"}}))
	covSetFlagCli9(t, historyCmd, "agent", "no-such-agent")

	err := historyCmd.RunE(historyCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected agent-not-found; got %v", err)
	}
}

func TestHistoryRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": covHistoryRuns(t)[:1]}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := historyCmd.RunE(historyCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"run-recent"`) {
		t.Errorf("json output missing run id:\n%s", out)
	}
}

func TestHistoryRunE_Validation(t *testing.T) {
	t.Run("bad limit", func(t *testing.T) {
		covStubCli9(t)
		covSetFlagCli9(t, historyCmd, "limit", "0")
		err := historyCmd.RunE(historyCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "bad --limit") {
			t.Errorf("expected bad --limit; got %v", err)
		}
	})
	t.Run("bad since", func(t *testing.T) {
		covStubCli9(t)
		covSetFlagCli9(t, historyCmd, "since", "not-a-duration")
		err := historyCmd.RunE(historyCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "bad --since") {
			t.Errorf("expected bad --since; got %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/runs", clitest.ErrorResponse(500, "runs down"))
		err := historyCmd.RunE(historyCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "runs down") {
			t.Errorf("expected server error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := historyCmd.RunE(historyCmd, nil); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := historyCmd.RunE(historyCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("expected workspace error; got %v", err)
		}
	})
}
